package providers

import (
	"context"
	"encoding/json"
	"gptibo/platform/internal/commerce"
	"gptibo/platform/internal/testutil"
	"io"
	"net/http"
	"strings"
	"testing"
)

func orderFixture(t *testing.T) (*Engine, commerce.Order, commerce.Snapshot) {
	s := testutil.Database(t)
	ctx := context.Background()
	_, err := s.DB.Exec(ctx, "INSERT INTO users(id,email,name,role,status) VALUES('user','synthetic@example.com','测试','channel','active'); INSERT INTO wallets(user_id,available_minor) VALUES('user',5000)")
	if err != nil {
		t.Fatal(err)
	}
	cfg := s.Vault.Seal("order:test-order", []byte(`{}`))
	_, err = s.DB.Exec(ctx, "INSERT INTO orders(id,user_id,merchant_order_no,idempotency_key,request_hash,product_code,business,supply_minor,config_cipher) VALUES('test-order','user','merchant','request-id','hash','gpt-plus','gpt',1000,$1)", cfg)
	if err != nil {
		t.Fatal(err)
	}
	snap := commerce.Snapshot{Business: "gpt", Provider: commerce.ProviderConfig{APIBase: "https://cards.example.invalid/openapi/v1", Environment: "sandbox", APIKey: "sk_synthetic"}, Rule: commerce.CardRule{ID: "rule", CardProduct: "TEST-BIN", CardID: 42, AutoOpen: true, FundingMinor: 2000, MaxOrderMinor: 1000, DailyLimitMinor: 2000}}
	return &Engine{Store: s}, commerce.Order{ID: "test-order", UserID: "user"}, snap
}
func TestUnknownFundingIsNeverReissued(t *testing.T) {
	e, o, s := orderFixture(t)
	_, err := e.Store.DB.Exec(context.Background(), "INSERT INTO card_operations(reference,order_id,rule_id,operation,amount_minor,card_id,status) VALUES('test-order:card','test-order','rule','fund',2000,42,'unknown')")
	if err != nil {
		t.Fatal(err)
	}
	old := client
	defer func() { client = old }()
	client = &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("queried or funded after unknown card operation")
		return nil, nil
	})}
	_, err = e.card(context.Background(), o, s, &Job{Stage: "funding", CardID: 42}, func() error { return nil })
	if err == nil {
		t.Fatal("unknown operation bypassed")
	}
}
func TestOpenUsesChosenCardHeadAndHardLimit(t *testing.T) {
	e, o, s := orderFixture(t)
	s.Rule.CardID = 0
	s.Provider.Billing.FirstName = "Test"
	s.Provider.Billing.LastName = "Owner"
	old := client
	defer func() { client = old }()
	posts := 0
	client = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/products") {
			return response(`{"code":0,"data":[{"product_code":"TEST-BIN","min_amount":10,"open_fee":0,"recharge_fee":0}]}`), nil
		}
		if strings.HasSuffix(r.URL.Path, "/balance") {
			return response(`{"code":0,"data":{"currency":"USD","spendable_balance":100}}`), nil
		}
		if r.Method == "POST" {
			posts++
			if r.URL.Path != "/openapi/v1/cards/open" {
				t.Fatalf("wrong card endpoint %s", r.URL.Path)
			}
			b, _ := io.ReadAll(r.Body)
			var data Object
			_ = json.Unmarshal(b, &data)
			if str(data["product_code"]) != "TEST-BIN" || number(data["init_amount"]) != 20 || number(data["transaction_limit"]) != 10 || str(data["transaction_limit_type"]) != "limited" {
				t.Fatalf("incorrect card request %s", b)
			}
			return response(`{"code":0,"data":{"id":42,"status":"ACTIVE","product_code":"TEST-BIN"}}`), nil
		}
		return response(`{"code":0,"data":{"id":42,"status":"ACTIVE","product_code":"TEST-BIN","available_amount":20,"transaction_limit":10,"transaction_limit_type":"limited"}}`), nil
	})}
	j := Job{Stage: "card"}
	id, err := e.card(context.Background(), o, s, &j, func() error { return nil })
	if err != nil || id != 42 || posts != 1 {
		t.Fatalf("card failed %d %v", id, err)
	}
	if err = e.bindCard(context.Background(), "sandbox", 42, "x"); err == nil {
		t.Fatal("card reused by another business")
	}
	_, err = e.card(context.Background(), o, s, &j, func() error { return nil })
	if err != nil || posts != 1 {
		t.Fatal("reopened card after successful checkpoint")
	}
}
func TestProviderDefaultLimitsDoNotOverrideCardOrBypassIdentity(t *testing.T) {
	e, o, s := orderFixture(t)
	s.Rule.CardID = 0
	s.Rule.UseProviderLimits = true
	s.Provider.Billing.FirstName = "Test"
	s.Provider.Billing.LastName = "Owner"
	old := client
	defer func() { client = old }()
	posts := 0
	wrongHead := false
	client = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/products") {
			return response(`{"code":0,"data":[{"product_code":"TEST-BIN","min_amount":10,"open_fee":0,"recharge_fee":0}]}`), nil
		}
		if strings.HasSuffix(r.URL.Path, "/balance") {
			return response(`{"code":0,"data":{"currency":"USD","spendable_balance":100}}`), nil
		}
		if r.Method == "POST" {
			posts++
			var body Object
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if r.URL.Path != "/openapi/v1/cards/open" || str(body["product_code"]) != "TEST-BIN" || number(body["init_amount"]) != 20 {
				t.Fatal("wrong card or funding")
			}
			for _, key := range []string{"max_on_daily", "max_on_monthly", "max_on_percent", "transaction_limit", "transaction_limit_type"} {
				if _, present := body[key]; present {
					t.Fatalf("provider default overridden: %s", key)
				}
			}
			return response(`{"code":0,"data":{"id":42}}`), nil
		}
		if wrongHead {
			return response(`{"code":0,"data":{"id":42,"status":"ACTIVE","product_code":"OTHER","available_amount":20}}`), nil
		}
		return response(`{"code":0,"data":{"id":42,"status":"ACTIVE","product_code":"TEST-BIN","available_amount":20,"spend_limits_supported":true,"limit":{"single_limit":0}}}`), nil
	})}
	j := Job{Stage: "card"}
	id, err := e.card(context.Background(), o, s, &j, func() error { return nil })
	if err != nil || id != 42 || posts != 1 {
		t.Fatalf("provider defaults rejected: %d %v", id, err)
	}
	s.Rule.UseProviderLimits = false
	if _, err = e.card(context.Background(), o, s, &j, func() error { return nil }); err == nil {
		t.Fatal("zero limit bypassed strict policy")
	}
	s.Rule.UseProviderLimits = true
	wrongHead = true
	if _, err = e.card(context.Background(), o, s, &j, func() error { return nil }); err == nil || posts != 1 {
		t.Fatal("default policy bypassed card identity")
	}
}

func TestXAccountAssignmentSurvivesBeforeJobCheckpoint(t *testing.T) {
	e, o, _ := orderFixture(t)
	account := `{"auth_token":"synthetic-auth","ct0":"synthetic-csrf"}`
	cipher := e.Store.Vault.Seal("x-account:account", []byte(account))
	_, err := e.Store.DB.Exec(context.Background(), "INSERT INTO x_accounts(id,name,payload_cipher,daily_limit) VALUES('account','测试',$1,10)", cipher)
	if err != nil {
		t.Fatal(err)
	}
	j := Job{}
	if err = e.assignAccount(context.Background(), o, &j); err != nil {
		t.Fatal(err)
	}
	_, _ = e.Store.DB.Exec(context.Background(), "UPDATE x_accounts SET payload_cipher=$1 WHERE id='account'", e.Store.Vault.Seal("x-account:account", []byte(`{"auth_token":"changed"}`)))
	j = Job{}
	if err = e.assignAccount(context.Background(), o, &j); err != nil {
		t.Fatal(err)
	}
	if j.AccountID != "account" || str(j.Account["auth_token"]) != "synthetic-auth" {
		t.Fatal("recovery changed account identity")
	}
}
func TestInventoryNeverReturnsCardSecrets(t *testing.T) {
	e, _, s := orderFixture(t)
	data, _ := json.Marshal(s.Provider)
	_, err := e.Store.DB.Exec(context.Background(), "INSERT INTO settings(id,payload_cipher) VALUES('provider',$1)", e.Store.Vault.Seal("setting:provider", data))
	if err != nil {
		t.Fatal(err)
	}
	old := client
	defer func() { client = old }()
	client = &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		return response(`{"code":0,"data":{"list":[{"id":42,"product_code":"TEST-BIN","cvv":"123","card_number":"4111111111111111"}]}}`), nil
	})}
	result, err := e.Inventory(context.Background(), "cards")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(result)
	if strings.Contains(string(b), "cvv") || strings.Contains(string(b), "411111") {
		t.Fatal("card secrets exposed")
	}
}

func TestBanStopsUndispatchedOrderBeforeCallingProvider(t *testing.T) {
	e, o, s := orderFixture(t)
	data, _ := json.Marshal(s)
	cfg := e.Store.Vault.Seal("order:"+o.ID, data)
	credential := e.Store.Vault.Seal("credential:"+o.ID, []byte(`{"mode":"access_token","accessToken":"synthetic"}`))
	_, err := e.Store.DB.Exec(context.Background(), "UPDATE users SET status='banned' WHERE id='user'")
	if err != nil {
		t.Fatal(err)
	}
	old := client
	defer func() { client = old }()
	client = &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("called provider for undispatched banned order")
		return nil, nil
	})}
	result := e.Execute(context.Background(), o, "lease", cfg, credential, "")
	if result.Status != "failed" || result.Failure != "channel_disabled" {
		t.Fatalf("banned order was not stopped %+v", result)
	}
}
