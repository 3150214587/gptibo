package integration_test

import (
	"context"
	"errors"
	"gptibo/storefront/internal/httpapi"
	"gptibo/storefront/internal/orders"
	"gptibo/storefront/internal/payments"
	"gptibo/storefront/internal/procurement"
	"gptibo/storefront/internal/security"
	"gptibo/storefront/internal/recharge"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

func TestCatalogSurvivesSupplierOutageAndCheckoutStillChecks(t *testing.T) {
	owner, app, a, _, _ := identityFixture(t)
	execSQL(t, owner, `INSERT INTO catalog_plans(id,product_id,name,price_cents,enabled) VALUES('plus','chatgpt','Plus',16900,true),('gmail-account','gmail','Gmail',1000,true)`)
	calls := 0
	transport := rechargeTransport(func(*http.Request) (*http.Response, error) { calls++; return nil, errors.New("supplier offline") })
	client, e := recharge.NewClient("https://supplier.example.test/v1", "fixture", "fixture", "fixture")
	if e != nil {
		t.Fatal(e)
	}
	client.HTTP.Transport = transport
	gmail, e := procurement.NewAccountSupplier("https://gmail.example.test", "12345", "fixture")
	if e != nil {
		t.Fatal(e)
	}
	gmail.HTTP.Transport = transport
	supplier := &recharge.Service{CredentialSecret: strings.Repeat("ab", 32), Store: app, Client: client, Checkout: true, Products: map[string]string{"plus": "gpt-plus"}, Notify: testOrigin + "/api/recharge/webhook"}
	pc := &procurement.Service{Store: app, AccountSupplier: gmail, EmailEnabled: true, GmailEnabled: true, GoodsID: 1, MaxCents: 400}
	pay := &payments.Service{Store: app, Provider: &paymentFake{account: "fixture"}, Checkout: true, NotifyOrigin: testOrigin}
	h := httpapi.Router(app, "fixture", slog.New(slog.NewTextHandler(io.Discard, nil)), httpapi.Options{Origin: testOrigin, Auth: a, Recharge: supplier, Procurement: pc, Payments: pay, MailSender: &frozenMail{}})
	result := object(t, request(h, "GET", "/api/catalog", nil, nil), 200)
	if calls != 0 || result["checkoutEnabled"] != true {
		t.Fatal("catalog depends on external service")
	}
	statistics, e := (orders.Service{Store: app, Auth: a, PaymentReady: true, SupplierReady: true, SupplierCheckout: true}).Overview(context.Background())
	if e != nil || statistics["mode"] != "live" || statistics["integrations"].(map[string]any)["supplierCheckout"] != true {
		t.Fatal("incorrect integration status", e)
	}
	for _, p := range result["plans"].([]any) {
		if p.(map[string]any)["enabled"] != float64(1) {
			t.Fatal("configured product hidden")
		}
	}
	buyer := &http.Cookie{Name: "__Host-sf_buyer", Value: security.Token()}
	for _, p := range []struct {
		plan  string
		price int
	}{{"plus", 16900}, {"gmail-account", 1000}} {
		w := request(h, "POST", "/api/orders", map[string]any{"plan": p.plan, "expectedPrice": p.price, "email": "buyer@example.test", "key": security.ID()}, buyer)
		if w.Code < 400 {
			t.Fatal("checkout skipped supplier validation")
		}
	}
	if calls != 2 {
		t.Fatal("checkout did not verify both suppliers", calls)
	}
	var n int
	app.DB.QueryRow(context.Background(), `SELECT count(*) FROM orders`).Scan(&n)
	if n != 0 {
		t.Fatal("outage created billable orders")
	}
}

func TestLegacyBuyerAndLimits(t *testing.T) {
	owner, app, a, _, _ := identityFixture(t)
	old := security.Token()
	execSQL(t, owner, `INSERT INTO catalog_plans(id,product_id,name,price_cents) VALUES('plus','chatgpt','Plus',16900)`)
	execSQL(t, owner, `INSERT INTO orders(id,owner_hash,idempotency_key,plan,plan_name,price_cents,status,created_at,updated_at,last_event_id) VALUES('YMX-IMPORTED',$1,'legacy','plus','Plus',16900,'pending',1,1,'legacy-event')`, security.Hash(old))
	handler := func(enabled bool) http.Handler {
		return httpapi.Router(app, "fixture", slog.New(slog.NewTextHandler(io.Discard, nil)), httpapi.Options{Origin: testOrigin, Auth: a, LegacyBuyer: enabled})
	}
	cookie := &http.Cookie{Name: "__Host-ymx_buyer", Value: old}
	object(t, request(handler(false), "GET", "/api/orders/YMX-IMPORTED", nil, cookie), 401)
	h := handler(true)
	v := object(t, request(h, "GET", "/api/orders/YMX-IMPORTED", nil, cookie), 200)
	if v["order"].(map[string]any)["id"] != "YMX-IMPORTED" {
		t.Fatal("lost legacy order")
	}
	w := request(h, "POST", "/api/orders/client", map[string]any{}, cookie)
	object(t, w, 200)
	found := false
	for _, c := range w.Result().Cookies() {
		if c.Name == "__Host-sf_buyer" && c.Value == old {
			found = true
		}
	}
	if !found {
		t.Fatal("buyer bridge replaced original token")
	}
	execSQL(t, owner, `DELETE FROM auth_limits`)
	for i := 0; i < 90; i++ {
		object(t, request(h, "GET", "/api/orders", nil, cookie), 200)
	}
	w = request(h, "GET", "/api/orders?bypass=1", nil, cookie)
	object(t, w, 429)
	if w.Header().Get("Retry-After") != "60" {
		t.Fatal("missing retry header")
	}
	// Callback budget is separate and still requires channel verification.
	object(t, request(h, "POST", "/api/recharge/webhook", map[string]any{}, nil), 503)
}

func TestAdminReconciliationFrontendContract(t *testing.T) {
	_, app, a, base, mail := identityFixture(t)
	admin := login(t, base, mail, "admin", "")
	s := &recharge.Service{CredentialSecret: strings.Repeat("ab", 32), Store: app}
	h := httpapi.Router(app, "fixture", slog.New(slog.NewTextHandler(io.Discard, nil)), httpapi.Options{Origin: testOrigin, Auth: a, Recharge: s})
	object(t, request(h, "POST", "/api/admin/recharge/reconcile", map[string]any{}, nil), 401)
	v := object(t, request(h, "POST", "/api/admin/recharge/reconcile", map[string]any{}, admin), 200)
	if v["checked"] != float64(0) || v["failed"] != float64(0) {
		t.Fatal("frontend result mismatch", v)
	}
	if e := s.Reconcile(context.Background()); e != nil {
		t.Fatal(e)
	}
}
