package commerce

import (
	"context"
	"encoding/json"
	"gptibo/platform/internal/security"
	"gptibo/platform/internal/testutil"
	"sync"
	"testing"
)

func setup(t *testing.T) (*Service, context.Context) {
	t.Helper()
	s := testutil.Database(t)
	ctx := context.Background()
	if _, err := s.DB.Exec(ctx, "INSERT INTO worker_heartbeats(id) VALUES('test-worker')"); err != nil {
		t.Fatal(err)
	}
	_, e := s.DB.Exec(ctx, "INSERT INTO users(id,email,name,role,status) VALUES('channel','test@example.com','测试','channel','active'); INSERT INTO wallets(user_id,available_minor) VALUES('channel',10000); UPDATE products SET supply_minor=1000,enabled=true,upstream_plan='explicit-plan' WHERE code='gpt-plus'; INSERT INTO card_rules(id,business,card_product,card_id,funding_minor,max_order_minor,daily_limit_minor) VALUES('rule','gpt','TEST-BIN',42,2000,1000,2000)")
	if e != nil {
		t.Fatal(e)
	}
	b, _ := json.Marshal(ProviderConfig{APIBase: "https://cards.example.invalid/openapi/v1", Environment: "sandbox", APIKey: "sk_test", WritesEnabled: true, PaymentCountry: "PH", PaymentCurrency: "PHP"})
	_, e = s.DB.Exec(ctx, "INSERT INTO settings(id,payload_cipher) VALUES('provider',$1)", s.Vault.Seal("setting:provider", b))
	if e != nil {
		t.Fatal(e)
	}
	return &Service{Store: s}, ctx
}
func request(no string) Request {
	return Request{MerchantOrderNo: no, ProductCode: "gpt-plus", Credential: Credential{Mode: "access_token", AccessToken: "synthetic-test-token"}}
}
func TestCardLimitPolicyIsFixedWithOrderSnapshot(t *testing.T) {
	svc, ctx := setup(t)
	if _, err := svc.Store.DB.Exec(ctx, "UPDATE card_rules SET use_provider_limits=true WHERE id='rule'"); err != nil {
		t.Fatal(err)
	}
	o, _, err := svc.Create(ctx, "channel", "provider-default-policy", request("provider-default-policy"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Store.DB.Exec(ctx, "UPDATE card_rules SET use_provider_limits=false WHERE id='rule'"); err != nil {
		t.Fatal(err)
	}
	var cipher string
	if err = svc.Store.DB.QueryRow(ctx, "SELECT config_cipher FROM orders WHERE id=$1", o.ID).Scan(&cipher); err != nil {
		t.Fatal(err)
	}
	data, err := svc.Store.Vault.Open("order:"+o.ID, cipher)
	if err != nil {
		t.Fatal(err)
	}
	var snap Snapshot
	if err = json.Unmarshal(data, &snap); err != nil || !snap.Rule.UseProviderLimits {
		t.Fatal("changed rule altered pending order policy")
	}
	o2, _, err := svc.Create(ctx, "channel", "strict-policy", request("strict-policy"))
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.Store.DB.QueryRow(ctx, "SELECT config_cipher FROM orders WHERE id=$1", o2.ID).Scan(&cipher); err != nil {
		t.Fatal(err)
	}
	data, err = svc.Store.Vault.Open("order:"+o2.ID, cipher)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &snap); err != nil || snap.Rule.UseProviderLimits {
		t.Fatal("new order did not use current policy")
	}
}

func TestConcurrentIdempotencyAndTerminalAccounting(t *testing.T) {
	svc, ctx := setup(t)
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	ids := make(chan string, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			o, _, e := svc.Create(ctx, "channel", "same-request", request("merchant-one"))
			errs <- e
			ids <- o.ID
		}()
	}
	wg.Wait()
	close(errs)
	close(ids)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	var id string
	for v := range ids {
		if id != "" && id != v {
			t.Fatal("duplicate orders")
		}
		id = v
	}
	var available, frozen int64
	if e := svc.Store.DB.QueryRow(ctx, "SELECT available_minor,frozen_minor FROM wallets WHERE user_id='channel'").Scan(&available, &frozen); e != nil {
		t.Fatal(e)
	}
	if available != 9000 || frozen != 1000 {
		t.Fatalf("double freeze: %d/%d", available, frozen)
	}
	_, e := svc.Store.DB.Exec(ctx, "UPDATE orders SET lease_token='lease' WHERE id=$1", id)
	if e != nil {
		t.Fatal(e)
	}
	if e = svc.Finish(ctx, id, "lease", "unknown", "submitting", "timeout", ""); e != nil {
		t.Fatal(e)
	}
	if e = svc.Store.DB.QueryRow(ctx, "SELECT frozen_minor FROM wallets WHERE user_id='channel'").Scan(&frozen); e != nil || frozen != 1000 {
		t.Fatal("unknown result released money")
	}
	_, _ = svc.Store.DB.Exec(ctx, "UPDATE orders SET lease_token='lease2' WHERE id=$1", id)
	if e = svc.Finish(ctx, id, "lease2", "succeeded", "completed", "", "receipt"); e != nil {
		t.Fatal(e)
	}
	var count int
	if e = svc.Store.DB.QueryRow(ctx, "SELECT count(*) FROM ledger WHERE order_id=$1", id).Scan(&count); e != nil || count != 2 {
		t.Fatal("incorrect ledger")
	}
	if _, e = svc.Store.DB.Exec(ctx, "UPDATE ledger SET note='changed' WHERE order_id=$1", id); e == nil {
		t.Fatal("ledger editable")
	}
	if _, e = svc.Store.DB.Exec(ctx, "UPDATE orders SET status='failed' WHERE id=$1", id); e == nil {
		t.Fatal("terminal overwritten")
	}
	if e = svc.Store.DB.QueryRow(ctx, "SELECT available_minor,frozen_minor FROM wallets WHERE user_id='channel'").Scan(&available, &frozen); e != nil || available != 9000 || frozen != 0 {
		t.Fatal("incorrect consume")
	}
}
func TestFailureReleaseAndCreditDeduplication(t *testing.T) {
	svc, ctx := setup(t)
	o, _, e := svc.Create(ctx, "channel", "fail-request", request("merchant-two"))
	if e != nil {
		t.Fatal(e)
	}
	_, _ = svc.Store.DB.Exec(ctx, "UPDATE orders SET lease_token='lease' WHERE id=$1", o.ID)
	if e = svc.Finish(ctx, o.ID, "lease", "failed", "preflight", "invalid_account", ""); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 2; i++ {
		if e = svc.Credit(ctx, "admin", "channel", "bank-transfer-1", "核实凭证", 2500); e != nil {
			t.Fatal(e)
		}
	}
	if e = svc.Credit(ctx, "admin", "channel", "bank-transfer-1", "核实凭证", 3000); e == nil {
		t.Fatal("credit conflict accepted")
	}
	var available, frozen int64
	if e = svc.Store.DB.QueryRow(ctx, "SELECT available_minor,frozen_minor FROM wallets WHERE user_id='channel'").Scan(&available, &frozen); e != nil || available != 12500 || frozen != 0 {
		t.Fatalf("incorrect balance %d %d: %v", available, frozen, e)
	}
}
func TestBanPriceSnapshotAndRequestConflict(t *testing.T) {
	svc, ctx := setup(t)
	o, _, e := svc.Create(ctx, "channel", "snapshot-request", request("merchant-three"))
	if e != nil {
		t.Fatal(e)
	}
	_, _ = svc.Store.DB.Exec(ctx, "UPDATE products SET supply_minor=5000 WHERE code='gpt-plus'; UPDATE card_rules SET card_product='OTHER-BIN'")
	var encrypted string
	if e = svc.Store.DB.QueryRow(ctx, "SELECT config_cipher FROM orders WHERE id=$1", o.ID).Scan(&encrypted); e != nil {
		t.Fatal(e)
	}
	b, e := svc.Store.Vault.Open("order:"+o.ID, encrypted)
	if e != nil {
		t.Fatal(e)
	}
	var snap Snapshot
	_ = json.Unmarshal(b, &snap)
	if snap.Rule.CardProduct != "TEST-BIN" || o.SupplyMinor != 1000 {
		t.Fatal("snapshot changed")
	}
	changed := request("merchant-three")
	changed.Credential.AccessToken = "other"
	if _, _, e = svc.Create(ctx, "channel", "snapshot-request", changed); e == nil {
		t.Fatal("request conflict accepted")
	}
	_, _ = svc.Store.DB.Exec(ctx, "UPDATE users SET status='banned' WHERE id='channel'")
	if _, _, e = svc.Create(ctx, "channel", security.Token(), request("new-order")); e == nil {
		t.Fatal("banned channel can order")
	}
}

func TestConcurrentCreditReceiptCannotFundTwoChannels(t *testing.T) {
	svc, ctx := setup(t)
	if _, e := svc.Store.DB.Exec(ctx, "INSERT INTO users(id,email,name,role,status) VALUES('other','other@example.com','另一渠道','channel','active'); INSERT INTO wallets(user_id) VALUES('other')"); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		user := "channel"
		if i%2 == 1 {
			user = "other"
		}
		wg.Add(1)
		go func(user string) {
			defer wg.Done()
			errs <- svc.Credit(ctx, "admin", user, "shared-bank-transfer-1", "已核实凭证", 2500)
		}(user)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			conflict, ok := e.(*Error)
			if !ok || conflict.Code != "credit_conflict" {
				t.Fatal(e)
			}
		}
	}
	var credits int
	var available int64
	if e := svc.Store.DB.QueryRow(ctx, "SELECT count(*) FROM ledger WHERE kind='credit'").Scan(&credits); e != nil || credits != 1 {
		t.Fatalf("bank receipt credited more than once: %d %v", credits, e)
	}
	if e := svc.Store.DB.QueryRow(ctx, "SELECT sum(available_minor) FROM wallets").Scan(&available); e != nil || available != 12500 {
		t.Fatalf("incorrect total wallet funds: %d %v", available, e)
	}
}
