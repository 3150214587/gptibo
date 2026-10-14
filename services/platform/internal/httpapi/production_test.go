package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPaymentHandoffRequiresAdminAndOrigin(t *testing.T) {
	s, h := fixture(t)
	calls := 0
	s.ManualCheckout = func(_ context.Context, actor, id string) (string, error) {
		calls++
		if actor != "admin" || id != "original" {
			t.Fatal("wrong handoff identity")
		}
		return "https://checkout.stripe.com/c/pay/cs_live_test#capability", nil
	}
	for _, token := range []string{"", "one-session"} {
		if got := call(h, "POST", "/api/admin/orders/original/payment-handoff", token, `{}`); got.Code != 401 && got.Code != 403 {
			t.Fatal(got.Code)
		}
	}
	req := httptest.NewRequest("POST", "/api/admin/orders/original/payment-handoff", nil)
	req.AddCookie(&http.Cookie{Name: "platform_session", Value: "admin-session"})
	req.Header.Set("Origin", "https://other.invalid")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 403 || calls != 0 {
		t.Fatal("handoff authorization bypassed")
	}
	got := call(h, "POST", "/api/admin/orders/original/payment-handoff", "admin-session", `{}`)
	if got.Code != 200 || calls != 1 || got.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(got.Code, got.Body.String())
	}
}

func TestStrictInputRejectsUnrecognizedPaymentFields(t *testing.T) {
	_, h := fixture(t)
	for _, body := range []string{`{"label":"key","supply_minor":1}`, `{"label":"key"} {"label":"other"}`} {
		if got := call(h, "POST", "/api/keys", "one-session", body); got.Code != 400 {
			t.Fatalf("unexpected payment field or trailing JSON accepted: %s", got.Body.String())
		}
	}
}

func TestPaymentRegenerationRequiresAdminAndOrigin(t *testing.T) {
	s, h := fixture(t)
	calls := 0
	s.RegenerateCheckout = func(_ context.Context, actor, id string, confirmed bool) (string, error) {
		calls++
		if actor != "admin" || id != "original" || !confirmed {
			t.Fatal("wrong regeneration request")
		}
		return "https://checkout.stripe.com/c/pay/cs_live_new#capability", nil
	}
	path := "/api/admin/orders/original/payment-regenerate"
	for _, token := range []string{"", "one-session"} {
		if got := call(h, "POST", path, token, `{"confirmed_unpaid":true}`); got.Code != 401 && got.Code != 403 {
			t.Fatal(got.Code)
		}
	}
	req := httptest.NewRequest("POST", path, nil)
	req.AddCookie(&http.Cookie{Name: "platform_session", Value: "admin-session"})
	req.Header.Set("Origin", "https://other.invalid")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 403 || calls != 0 {
		t.Fatal("regeneration authorization bypassed")
	}
	got := call(h, "POST", path, "admin-session", `{"confirmed_unpaid":true}`)
	if got.Code != 200 || calls != 1 || got.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(got.Code, got.Body.String())
	}
}
func TestOriginalMerchantLookupIsExactAndTenantScoped(t *testing.T) {
	s, h := fixture(t)
	ctx := context.Background()
	for _, row := range []struct{ id, user, merchant string }{{"original", "one", "merchant-original"}, {"other", "two", "merchant-other"}} {
		if _, err := s.Store.DB.Exec(ctx, "INSERT INTO orders(id,user_id,merchant_order_no,idempotency_key,request_hash,product_code,business,supply_minor,config_cipher) VALUES($1,$2,$3,$1,'hash','gpt-plus','gpt',1000,$4)", row.id, row.user, row.merchant, s.Store.Vault.Seal("order:"+row.id, []byte(`{}`))); err != nil {
			t.Fatal(err)
		}
	}
	if got := call(h, "GET", "/api/orders/by-merchant/merchant-original", "one-session", ""); got.Code != 200 {
		t.Fatal(got.Body.String())
	}
	for _, merchant := range []string{"merchant-other", "merchant-orig", "missing"} {
		if got := call(h, "GET", "/api/orders/by-merchant/"+merchant, "one-session", ""); got.Code != 404 {
			t.Fatal("original lookup leaked other channel or fuzzy matched", got.Body.String())
		}
	}
}
func TestReinvitationInvalidatesQueuedOldEmail(t *testing.T) {
	s, h := fixture(t)
	ctx := context.Background()
	got := call(h, "POST", "/api/admin/channels", "admin-session", `{"name":"test","email":"reinvite@test.invalid"}`)
	if got.Code != 200 {
		t.Fatal(got.Body.String())
	}
	var result struct {
		Data struct {
			ID string `json:"id"`
		}
	}
	if err := json.Unmarshal(got.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if got = call(h, "POST", "/api/admin/channels/"+result.Data.ID+"/invite", "admin-session", `{}`); got.Code != 200 {
		t.Fatal(got.Body.String())
	}
	var pending, dead, valid int
	if err := s.Store.DB.QueryRow(ctx, "SELECT count(*) FILTER(WHERE status='pending'),count(*) FILTER(WHERE status='dead' AND last_error='invitation_replaced') FROM outbox WHERE user_id=$1", result.Data.ID).Scan(&pending, &dead); err != nil {
		t.Fatal(err)
	}
	if err := s.Store.DB.QueryRow(ctx, "SELECT count(*) FROM invitations WHERE user_id=$1 AND used_at IS NULL", result.Data.ID).Scan(&valid); err != nil {
		t.Fatal(err)
	}
	if pending != 1 || dead != 1 || valid != 1 {
		t.Fatal("old mail or invitation still live", pending, dead, valid)
	}
}
func TestAccountToggleRetainsCookieAndAllowsExplicitProxyRemoval(t *testing.T) {
	s, h := fixture(t)
	ctx := context.Background()
	cookie := `{"auth_token":"synthetic","ct0":"synthetic","proxy":"http://proxy.test.invalid:8080"}`
	if _, err := s.Store.DB.Exec(ctx, "INSERT INTO x_accounts(id,name,payload_cipher,daily_limit) VALUES('account','test',$1,10)", s.Store.Vault.Seal("x-account:account", []byte(cookie))); err != nil {
		t.Fatal(err)
	}
	for _, clear := range []bool{false, true} {
		body := `{"id":"account","name":"test","enabled":false,"daily_limit":10,"auth_token":"","ct0":"","proxy":"","clear_proxy":false}`
		if clear {
			body = `{"id":"account","name":"test","enabled":true,"daily_limit":10,"clear_proxy":true,"proxy":"http://ignored.test.invalid:8080"}`
		}
		if got := call(h, "POST", "/api/admin/x-accounts", "admin-session", body); got.Code != 200 {
			t.Fatal(got.Body.String())
		}
		var cipher string
		if err := s.Store.DB.QueryRow(ctx, "SELECT payload_cipher FROM x_accounts WHERE id='account'").Scan(&cipher); err != nil {
			t.Fatal(err)
		}
		data, err := s.Store.Vault.Open("x-account:account", cipher)
		if err != nil {
			t.Fatal(err)
		}
		var account map[string]string
		if err = json.Unmarshal(data, &account); err != nil {
			t.Fatal(err)
		}
		if account["auth_token"] != "synthetic" || account["ct0"] != "synthetic" || (!clear && account["proxy"] == "") || (clear && account["proxy"] != "") {
			t.Fatal("toggle destroyed secrets or failed proxy removal")
		}
	}
}
func TestPreflightRateWindowEnforcesBoundary(t *testing.T) {
	s, _ := fixture(t)
	ctx := context.Background()
	for i := 0; i < 12; i++ {
		if err := s.allowRate(ctx, "preflight:test", 12, time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	if s.allowRate(ctx, "preflight:test", 12, time.Minute) == nil {
		t.Fatal("expensive preflight rate unrestricted")
	}
}
