package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"gptibo/storefront/internal/domainbilling"
	"gptibo/storefront/internal/domains"
	"gptibo/storefront/internal/fault"
	"gptibo/storefront/internal/httpapi"
	"gptibo/storefront/internal/payments"
	"gptibo/storefront/internal/security"
	"gptibo/storefront/internal/store"
)

func enableTestDomains(opts *httpapi.Options) {
	opts.Domains = &domains.Service{MonthlyFeeCents: 500, Store: opts.Auth.Store, Provider: &domainProvider{}, Target: "shops.shop.example.invalid", Zone: "shop.example.invalid", Limit: 100}
	opts.TrustedProxies = []string{"127.0.0.1/32"}
}

func TestDomainInitialBalancePaymentAPI(t *testing.T) {
	f := hierarchySetup(t, enableTestDomains)
	ctx := context.Background()
	execSQL(t, f.owner, `INSERT INTO partner_domains(id,partner_id,hostname,verification,free) VALUES('activation',$1,'activation.example.com','v',false)`, f.childID)
	path := "/api/partner/domains/activation/payment"
	body := map[string]any{"action": "balance", "version": 0}
	object(t, request(f.h, "POST", path, body, nil), 401)
	object(t, request(f.h, "POST", path, body, f.parent), 404)
	object(t, request(f.h, "POST", path, body, f.child), 409)
	f.seed(t, "initial-funds", "plus", &http.Cookie{Value: security.Token()}) // child earns exactly ¥20
	execSQL(t, f.owner, `UPDATE orders SET refund_hold=1 WHERE id='initial-funds'`)
	object(t, request(f.h, "POST", path, body, f.child), 409)
	execSQL(t, f.owner, `UPDATE orders SET refund_hold=0 WHERE id='initial-funds'`)
	var wg sync.WaitGroup
	results := make(chan *httptest.ResponseRecorder, 5)
	for range 5 {
		wg.Add(1)
		go func() { defer wg.Done(); results <- request(f.h, "POST", path, body, f.child) }()
	}
	wg.Wait()
	close(results)
	var receiptID string
	for r := range results {
		payment := object(t, r, 200)["payment"].(map[string]any)
		if payment["source"] != "balance" || payment["state"] != "paid" || payment["amount_cents"] != float64(500) {
			t.Fatal(payment)
		}
		if receiptID != "" && receiptID != payment["id"] {
			t.Fatal("concurrent requests returned different receipts")
		}
		receiptID = payment["id"].(string)
	}
	var count int
	var amount int64
	var activated bool
	if e := f.owner.DB.QueryRow(ctx, `SELECT count(*),sum(amount_cents)::bigint FROM partner_debits WHERE partner_id=$1`, f.childID).Scan(&count, &amount); e != nil || count != 1 || amount != 500 {
		t.Fatal("duplicate activation debit", count, amount, e)
	}
	if e := f.owner.DB.QueryRow(ctx, `SELECT billing_version=1 AND expires_at>now()+interval '27 days' FROM partner_domains WHERE id='activation'`).Scan(&activated); e != nil || !activated {
		t.Fatal("activation period invalid", e)
	}
	f.balance(t, f.child, 1500, 500)
	f.balance(t, f.parent, 1000, 0)
	object(t, request(f.h, "POST", path, map[string]any{"action": "balance", "version": 1}, f.child), 409)
	// The original balance receipt also prevents a stale external-payment click from charging again.
	paid := object(t, request(f.h, "POST", path, map[string]any{"action": "create", "version": 0}, f.child), 200)["payment"].(map[string]any)
	if paid["id"] != receiptID {
		t.Fatal("stale external click generated another charge")
	}
}

func TestDomainInitialBalanceAndWithdrawalSerialize(t *testing.T) {
	f := hierarchySetup(t, enableTestDomains)
	f.bind(t)
	f.seed(t, "withdrawal-funds", "plus", &http.Cookie{Value: security.Token()})
	execSQL(t, f.owner, `INSERT INTO partner_domains(id,partner_id,hostname,verification,free) VALUES('activation',$1,'activation.example.com','v',false)`, f.childID)
	var wg sync.WaitGroup
	var pay, withdraw *httptest.ResponseRecorder
	wg.Add(2)
	go func() {
		defer wg.Done()
		pay = request(f.h, "POST", "/api/partner/domains/activation/payment", map[string]any{"action": "balance", "version": 0}, f.child)
	}()
	go func() {
		defer wg.Done()
		withdraw = request(f.h, "POST", "/api/partner/payouts", map[string]any{"key": security.ID(), "accountVersion": 1}, f.child)
	}()
	wg.Wait()
	if !((pay.Code == 200 && withdraw.Code == 201) || (pay.Code == 409 && withdraw.Code == 201)) {
		t.Fatal("unexpected concurrent result", pay.Code, pay.Body.String(), withdraw.Code, withdraw.Body.String())
	}
	var payout, debit int64
	if e := f.owner.DB.QueryRow(context.Background(), `SELECT (SELECT COALESCE(sum(amount_cents),0)::bigint FROM partner_payouts WHERE partner_id=$1),(SELECT COALESCE(sum(amount_cents),0)::bigint FROM domain_charges WHERE partner_id=$1 AND source='balance')`, f.childID).Scan(&payout, &debit); e != nil || payout+debit != 2000 {
		t.Fatal("activation double-spent withdrawal balance", payout, debit, e)
	}
}

func TestDomainInitialPaymentMethodAndSupplierIsolation(t *testing.T) {
	var app *store.Store
	f := hierarchySetup(t, func(opts *httpapi.Options) { enableTestDomains(opts); app = opts.Auth.Store })
	ctx := context.Background()
	f.seed(t, "supplier-funds", "plus", &http.Cookie{Value: security.Token()})
	execSQL(t, f.owner, `INSERT INTO recharge_orders(order_id,account,product_code,collection_mode,create_body,updated_at,merchant_margin_cents,financial_hold) VALUES('supplier-funds','fixture','fixture','platform_collect','{}',1,3000,0)`)
	execSQL(t, f.owner, `INSERT INTO partner_domains(id,partner_id,hostname,verification,free) VALUES('activation',$1,'activation.example.com','v',false)`, f.childID)
	execSQL(t, f.owner, `UPDATE payment_settings SET enabled_providers=ARRAY['gatewayb-alipay']`)
	object(t, request(f.h, "POST", "/api/partner/domains/activation/payment", map[string]any{"action": "balance", "version": 0}, f.child), 503)
	fake := &domainPaymentFake{paymentFake: paymentFake{account: "original"}}
	billing := domainbilling.Service{MonthlyFeeCents: 500, Store: app, Payments: payments.Service{Store: app, Provider: fake, Providers: map[string]payments.Provider{fake.ID(): fake}, Checkout: true, NotifyOrigin: "https://example.test"}, VerifySupplier: func(context.Context, string) error { return nil }}
	pending, e := billing.Payment(ctx, f.childID, "activation", "create", 0)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = billing.Payment(ctx, f.childID, "activation", "balance", 0); fault.Status(e) != 409 {
		t.Fatal("pending external receipt allowed double debit", e)
	}
	chargeID := pending["payment"].(map[string]any)["id"].(string)
	if e = billing.Apply(ctx, fake, payments.Result{OrderID: chargeID, Status: "paid", Amount: 500, TradeNo: "INITIAL-EXTERNAL"}); e != nil {
		t.Fatal(e)
	}
	if _, e = billing.Payment(ctx, f.childID, "activation", "balance", 0); fault.Status(e) != 409 {
		t.Fatal("externally activated domain debited balance", e)
	}
	var count int
	if e = f.owner.DB.QueryRow(ctx, `SELECT count(*) FROM partner_debits`).Scan(&count); e != nil || count != 0 {
		t.Fatal("external activation also consumed balance", count, e)
	}
	// New supplier earnings must be checked before a separate balance activation.
	execSQL(t, f.owner, `INSERT INTO partner_domains(id,partner_id,hostname,verification,free) VALUES('verified',$1,'verified.example.com','v',false)`, f.childID)
	checked := false
	billing.VerifySupplier = func(ctx context.Context, id string) error { checked = id == "supplier-funds"; return nil }
	if _, e = billing.Payment(ctx, f.childID, "verified", "balance", 0); e != nil || !checked {
		t.Fatal("supplier verification missing", checked, e)
	}
	// Retrying the receipt succeeds even if future supplier verification is unavailable.
	billing.VerifySupplier = nil
	if _, e = billing.Payment(ctx, f.childID, "verified", "balance", 0); e != nil {
		t.Fatal("paid retry required supplier again", e)
	}
}

func TestChildReferralUsesParentDomainAndOwnStore(t *testing.T) {
	f := hierarchySetup(t, enableTestDomains)
	ctx := context.Background()
	execSQL(t, f.owner, `INSERT INTO partner_domains(id,partner_id,hostname,verification,provider_id,state,checked_at,free) VALUES('parent-domain',$1,'parent.example.com','v','cf-parent','active',now(),true)`, f.parentID)
	checkOrigin := func(want string) {
		t.Helper()
		for _, test := range []struct {
			path   string
			cookie *http.Cookie
			nested bool
		}{
			{"/api/partner/dashboard", f.child, true}, {"/api/partner/stores", f.child, false}, {"/api/partner/children/" + f.childID, f.parent, true}, {"/api/admin/partners/" + f.childID, f.admin, true},
		} {
			out := object(t, request(f.h, "GET", test.path, nil, test.cookie), 200)
			if test.nested {
				out = out["partner"].(map[string]any)
			}
			if out["referralOrigin"] != want {
				t.Fatalf("%s: got %v want %s", test.path, out["referralOrigin"], want)
			}
		}
	}
	checkOrigin("https://parent.example.com")
	buyer := &http.Cookie{Name: "__Host-sf_buyer", Value: security.Token()}
	selectedShop := f.slug
	call := func(method, path string, body any) *httptest.ResponseRecorder {
		data, _ := json.Marshal(body)
		r := httptest.NewRequest(method, path, bytes.NewReader(data))
		r.RemoteAddr = "127.0.0.1:1234"
		r.Header.Set("X-Storefront-Host", "parent.example.com")
		if r.URL.Query().Get("partner") == "" {
			r.Header.Set("X-Storefront-Shop", selectedShop)
		}
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "https://parent.example.com")
		r.AddCookie(buyer)
		w := httptest.NewRecorder()
		f.h.ServeHTTP(w, r)
		return w
	}
	shop := makeShop(t, f, "Child custom prices")
	id, slug := shop["id"].(string), shop["slug"].(string)
	object(t, request(f.h, "POST", "/api/partner/stores/prices", map[string]any{"storeId": id, "plan": "plus", "priceCents": 15000, "enabled": true, "version": 0}, f.child), 200)
	object(t, request(f.h, "POST", "/api/partner/coupons", couponInput("CHILD", []map[string]string{{"storeId": id, "plan": "plus"}}, 1000, 2), f.child), 200)
	for code, price := range map[string]float64{f.slug: 13000, slug: 15000} {
		object(t, call("GET", "/api/internal/domain-route?partner="+code, nil), 200)
		catalog := object(t, call("GET", "/api/catalog?partner="+code, nil), 200)
		if catalog["partner"].(map[string]any)["slug"] != code || catalog["plans"].([]any)[0].(map[string]any)["price_cents"] != price {
			t.Fatal("parent host changed child storefront", catalog)
		}
	}
	selectedShop = slug
	quote := map[string]any{"partner": slug, "plan": "plus", "email": "buyer@example.test", "code": "CHILD"}
	if object(t, call("POST", "/api/orders/coupon-quote", quote), 200)["priceCents"] != float64(14000) {
		t.Fatal("coupon not scoped to child shop")
	}
	selectedShop = f.slug
	quote["partner"] = f.slug
	object(t, call("POST", "/api/orders/coupon-quote", quote), 400)
	selectedShop = slug
	o := object(t, call("POST", "/api/orders", map[string]any{"key": security.ID(), "plan": "plus", "email": "buyer@example.test", "partner": slug, "expectedPrice": 14000, "couponCode": "CHILD"}), 201)["order"].(map[string]any)
	object(t, call("GET", "/api/orders/"+o["id"].(string), nil), 200)
	list := object(t, call("GET", "/api/orders", nil), 200)["orders"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["id"] != o["id"] {
		t.Fatal("child order missing from parent-host customer list", list)
	}
	object(t, request(f.h, "GET", "/api/orders/"+o["id"].(string), nil, buyer), 404)
	originalToken := buyer.Value
	buyer.Value = security.Token()
	object(t, call("GET", "/api/orders/"+o["id"].(string), nil), 404)
	if len(object(t, call("GET", "/api/orders", nil), 200)["orders"].([]any)) != 0 {
		t.Fatal("parent host exposed another customer's orders")
	}
	buyer.Value = originalToken
	var seller, upstream, shopID string
	var price, cost, upCost int64
	if e := f.owner.DB.QueryRow(ctx, `SELECT partner_id,upstream_partner_id,store_id,price_cents,cost_cents,upstream_cost_cents FROM orders WHERE id=$1`, o["id"]).Scan(&seller, &upstream, &shopID, &price, &cost, &upCost); e != nil || seller != f.childID || upstream != f.parentID || shopID != id || price != 14000 || cost != 11000 || upCost != 10000 {
		t.Fatal("inherited host changed order attribution", seller, upstream, shopID, price, cost, upCost, e)
	}
	object(t, call("POST", "/api/visitors", map[string]any{"visitorId": security.ID(), "path": "/r/" + slug}), 200)
	var visits int
	if e := f.owner.DB.QueryRow(ctx, `SELECT count(*) FROM visitor_days WHERE shop_id=$1`, id).Scan(&visits); e != nil || visits != 1 {
		t.Fatal("child store lost visitor attribution", visits, e)
	}
	// A usable fallback parent domain is preferred to unavailable or expired bindings.
	execSQL(t, f.owner, `INSERT INTO partner_domains(id,partner_id,hostname,verification,provider_id,state,checked_at,free,expires_at) VALUES('fallback',$1,'fallback.example.com','v','cf-fallback','active',now(),false,now()+interval '1 month')`, f.parentID)
	checkOrigin("https://parent.example.com")
	execSQL(t, f.owner, `UPDATE partner_domains SET checked_at=now()-interval '25 hours' WHERE id='parent-domain'`)
	checkOrigin("https://fallback.example.com")
	execSQL(t, f.owner, `UPDATE partner_domains SET expires_at=now()-interval '4 days' WHERE id='fallback'`)
	checkOrigin("")
	object(t, request(f.h, "GET", "/api/catalog?partner="+slug, nil, nil), 200)
	// Once the relationship changes, the old parent hostname cannot serve this child.
	execSQL(t, f.owner, `UPDATE partner_domains SET checked_at=now() WHERE id='parent-domain'`)
	execSQL(t, f.owner, `UPDATE partners SET parent_id=NULL WHERE id=$1`, f.childID)
	checkOriginAfter := object(t, request(f.h, "GET", "/api/partner/dashboard", nil, f.child), 200)["partner"].(map[string]any)
	if checkOriginAfter["referralOrigin"] != "" {
		t.Fatal("old parent still displayed")
	}
	if w := call("GET", "/api/internal/domain-route?partner="+slug, nil); w.Code != 404 {
		t.Fatal("former child still routed", w.Code)
	}
	object(t, call("GET", "/api/catalog?partner="+slug, nil), 404)
	object(t, call("GET", "/api/orders/"+o["id"].(string), nil), 404)
	object(t, call("GET", "/api/orders", nil), 404)
}
