package providers

import (
	"context"
	"encoding/json"
	"errors"
	"gptibo/platform/internal/commerce"
	"net/http"
	"strings"
	"testing"
)

func TestGPTLostSubmissionRecoversFromDurableCheckpointAndSettlesOnce(t *testing.T) {
	e, o, s := orderFixture(t)
	ctx := context.Background()
	s.Plan = "plan"
	s.Provider.WritesEnabled = true
	s.Provider.PaymentCountry = "PH"
	s.Provider.PaymentCurrency = "PHP"
	b, _ := json.Marshal(s)
	cfg := e.Store.Vault.Seal("order:"+o.ID, b)
	cred := e.Store.Vault.Seal("credential:"+o.ID, []byte(`{"mode":"access_token","accessToken":"synthetic"}`))
	if _, err := e.Store.DB.Exec(ctx, "ALTER TABLE orders DISABLE TRIGGER order_guard"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Store.DB.Exec(ctx, "UPDATE orders SET config_cipher=$1,credential_cipher=$2,lease_token='first',lease_until=now()+interval '5 minutes'", cfg, cred); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Store.DB.Exec(ctx, "ALTER TABLE orders ENABLE TRIGGER order_guard; INSERT INTO ledger(id,user_id,order_id,kind,available_delta,frozen_delta,reference,note,actor_id) VALUES('reserve','user','test-order','reserve',-1000,1000,'test-order:reserve','test','user')"); err != nil {
		t.Fatal(err)
	}
	b, _ = json.Marshal(s.Provider)
	if _, err := e.Store.DB.Exec(ctx, "INSERT INTO settings(id,payload_cipher) VALUES('provider',$1)", e.Store.Vault.Seal("setting:provider", b)); err != nil {
		t.Fatal(err)
	}
	old := client
	defer func() { client = old }()
	posts := 0
	recovering := false
	client = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if recovering && r.Method != "GET" {
			t.Fatal("recovery created a second side effect")
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/plans"):
			return response(`{"code":0,"data":{"version":1,"registry":[{"product":"gpt","key":"plan","purchasable":true}],"plans":{"plan":{"enabled":true}}}}`), nil
		case strings.HasSuffix(r.URL.Path, "/preflight"):
			return response(`{"code":0,"data":{"email":"customer@test.invalid","preflight_token":"token"}}`), nil
		case strings.HasSuffix(r.URL.Path, "/cards/42"):
			return response(`{"code":0,"data":{"id":42,"status":"ACTIVE","product_code":"TEST-BIN","available_amount":20,"transaction_limit":10,"transaction_limit_type":"limited"}}`), nil
		case r.Method == "POST":
			posts++
			if r.Header.Get("Idempotency-Key") != o.ID {
				t.Fatal("upstream reference changed")
			}
			return nil, errors.New("lost response after acceptance")
		case strings.HasSuffix(r.URL.Path, "/orders"):
			return response(`{"code":0,"data":{"list":[{"id":"original-upstream","client_request_id":"test-order"}]}}`), nil
		default:
			return response(`{"code":0,"data":{"id":"original-upstream","client_request_id":"test-order","card_id":42,"plan":"plan","account_email":"customer@test.invalid","status":"completed"}}`), nil
		}
	})}
	result := e.Execute(ctx, o, "first", cfg, cred, "")
	svc := &commerce.Service{Store: e.Store}
	if result.Status != "unknown" || result.Stage != "submitting" {
		t.Fatal("lost submission treated as failure", result)
	}
	if err := svc.Finish(ctx, o.ID, "first", result.Status, result.Stage, result.Failure, ""); err != nil {
		t.Fatal(err)
	}
	var available, frozen int64
	if err := e.Store.DB.QueryRow(ctx, "SELECT available_minor,frozen_minor FROM wallets WHERE user_id='user'").Scan(&available, &frozen); err != nil || available != 4000 || frozen != 1000 {
		t.Fatal("unknown result released money", err)
	}
	recovering = true
	var job string
	if err := e.Store.DB.QueryRow(ctx, "SELECT job_cipher FROM orders WHERE id=$1", o.ID).Scan(&job); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Store.DB.Exec(ctx, "UPDATE orders SET lease_token='second',lease_until=now()+interval '5 minutes'"); err != nil {
		t.Fatal(err)
	}
	result = e.Execute(ctx, o, "second", cfg, cred, job)
	if result.Status != "succeeded" || posts != 1 {
		t.Fatal("original recovery failed", result, posts)
	}
	if err := svc.Finish(ctx, o.ID, "second", result.Status, result.Stage, "", result.Receipt); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.DB.QueryRow(ctx, "SELECT available_minor,frozen_minor FROM wallets WHERE user_id='user'").Scan(&available, &frozen); err != nil || available != 4000 || frozen != 0 {
		t.Fatal("incorrect final settlement", err)
	}
}

func TestSuccessfulFundingWithLowBalanceCannotFundAgain(t *testing.T) {
	e, o, s := orderFixture(t)
	ctx := context.Background()
	if _, err := e.Store.DB.Exec(ctx, "INSERT INTO card_operations(reference,order_id,rule_id,operation,amount_minor,card_id,status) VALUES('test-order:card','test-order','rule','fund',2000,42,'succeeded')"); err != nil {
		t.Fatal(err)
	}
	old := client
	defer func() { client = old }()
	client = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" || r.URL.Path != "/openapi/v1/cards/42" || r.URL.Query().Get("sync") != "1" {
			t.Fatal("funded again or used stale balance", r.Method, r.URL)
		}
		return response(`{"code":0,"data":{"id":42,"status":"ACTIVE","product_code":"TEST-BIN","available_amount":1,"transaction_limit":10,"transaction_limit_type":"limited"}}`), nil
	})}
	if _, err := e.card(ctx, o, s, &Job{}, func() error { return nil }); err == nil {
		t.Fatal("low balance after prior funding accepted")
	}
}
func TestGPTPreflightAccountChangeStopsBeforeRechargeSubmission(t *testing.T) {
	for _, phase := range []string{"initial", "after-card"} {
		t.Run(phase, func(t *testing.T) {
			e, o, s := orderFixture(t)
			s.Plan = "plan"
			s.Provider.PaymentCountry = "PH"
			s.Provider.PaymentCurrency = "PHP"
			s.ConfirmedAccount = "customer@test.invalid"
			old := client
			defer func() { client = old }()
			preflights := 0
			client = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
				if strings.HasSuffix(r.URL.Path, "/plans") {
					return response(`{"code":0,"data":{"version":1,"registry":[{"product":"gpt","key":"plan","purchasable":true}],"plans":{"plan":{"enabled":true}}}}`), nil
				}
				if strings.HasSuffix(r.URL.Path, "/preflight") {
					preflights++
					email := "customer@test.invalid"
					if phase == "initial" || preflights > 1 {
						email = "different@test.invalid"
					}
					return response(`{"code":0,"data":{"email":"` + email + `","preflight_token":"token"}}`), nil
				}
				if r.Method != "GET" || r.URL.Path != "/openapi/v1/cards/42" {
					t.Fatal("submitted recharge despite account change")
				}
				return response(`{"code":0,"data":{"id":42,"status":"ACTIVE","product_code":"TEST-BIN","available_amount":20,"transaction_limit":10,"transaction_limit_type":"limited"}}`), nil
			})}
			_, err := e.gpt(context.Background(), o, s, commerce.Credential{Mode: "access_token", AccessToken: "synthetic"}, &Job{Stage: "preflight"}, func() error { return nil })
			if err == nil || err.Error() != "account_changed" {
				t.Fatal("wrong account not stopped", err)
			}
		})
	}
}
func TestCheckpointBlocksNewEffectsAfterLeaseLossOrPause(t *testing.T) {
	e, o, s := orderFixture(t)
	ctx := context.Background()
	s.Provider.WritesEnabled = true
	cfg, _ := json.Marshal(s.Provider)
	if _, err := e.Store.DB.Exec(ctx, "INSERT INTO settings(id,payload_cipher) VALUES('provider',$1)", e.Store.Vault.Seal("setting:provider", cfg)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Store.DB.Exec(ctx, "UPDATE orders SET lease_token='lease',lease_until=now()+interval '1 minute'"); err != nil {
		t.Fatal(err)
	}
	if err := e.checkpoint(ctx, o.ID, "lease", Job{Stage: "submitting"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Store.DB.Exec(ctx, "UPDATE orders SET lease_until=now()-interval '1 second'"); err != nil {
		t.Fatal(err)
	}
	if e.checkpoint(ctx, o.ID, "lease", Job{Stage: "submitting"}) == nil {
		t.Fatal("lost lease dispatched")
	}
	if _, err := e.Store.DB.Exec(ctx, "UPDATE orders SET lease_until=now()+interval '1 minute'"); err != nil {
		t.Fatal(err)
	}
	s.Provider.WritesEnabled = false
	cfg, _ = json.Marshal(s.Provider)
	if _, err := e.Store.DB.Exec(ctx, "UPDATE settings SET payload_cipher=$1", e.Store.Vault.Seal("setting:provider", cfg)); err != nil {
		t.Fatal(err)
	}
	if e.checkpoint(ctx, o.ID, "lease", Job{Stage: "submitting"}) == nil {
		t.Fatal("paused provider dispatched")
	}
}
func TestManualOriginalCardReconciliationNeverPostsAndCannotOverwrite(t *testing.T) {
	e, o, s := orderFixture(t)
	ctx := context.Background()
	data, _ := json.Marshal(s)
	// Fixture has not run any external operation; it represents a lost original funding response.
	if _, err := e.Store.DB.Exec(ctx, "ALTER TABLE orders DISABLE TRIGGER order_guard"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Store.DB.Exec(ctx, "UPDATE orders SET config_cipher=$1,stage='funding',status='unknown'", e.Store.Vault.Seal("order:"+o.ID, data)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Store.DB.Exec(ctx, "ALTER TABLE orders ENABLE TRIGGER order_guard"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Store.DB.Exec(ctx, "INSERT INTO card_operations(reference,order_id,rule_id,operation,amount_minor,card_id,status) VALUES('test-order:card','test-order','rule','fund',2000,42,'unknown')"); err != nil {
		t.Fatal(err)
	}
	old := client
	defer func() { client = old }()
	calls := 0
	client = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != "GET" {
			t.Fatal("manual resolution repeated charge")
		}
		return response(`{"code":0,"data":{"id":42,"status":"ACTIVE","product_code":"TEST-BIN","available_amount":20,"transaction_limit":10,"transaction_limit_type":"limited"}}`), nil
	})}
	r := commerce.CardResolution{Reference: o.ID + ":card", Status: "succeeded", CardID: 999, Confirmation: "RESOLVE", Note: "original provider receipt verified"}
	if e.ResolveCardOperation(ctx, "admin", r) == nil || calls != 0 {
		t.Fatal("different card accepted")
	}
	r.CardID = 42
	if _, err := e.Store.DB.Exec(ctx, "UPDATE orders SET lease_until=now()+interval '1 minute'"); err != nil {
		t.Fatal(err)
	}
	if e.ResolveCardOperation(ctx, "admin", r) == nil {
		t.Fatal("active worker raced manual resolution")
	}
	if _, err := e.Store.DB.Exec(ctx, "UPDATE orders SET lease_until=NULL"); err != nil {
		t.Fatal(err)
	}
	if err := e.ResolveCardOperation(ctx, "admin", r); err != nil {
		t.Fatal(err)
	}
	if err := e.ResolveCardOperation(ctx, "admin", r); err != nil {
		t.Fatal("identical receipt not idempotent", err)
	}
	r.Status = "failed"
	if e.ResolveCardOperation(ctx, "admin", r) == nil {
		t.Fatal("resolved result overwritten")
	}
	var audits int
	if err := e.Store.DB.QueryRow(ctx, "SELECT count(*) FROM audit WHERE action='resolve_card_original'").Scan(&audits); err != nil || audits != 1 {
		t.Fatal("receipt not audited exactly once", err, audits)
	}
}
