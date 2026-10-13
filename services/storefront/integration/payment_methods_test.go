package integration_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"gptibo/storefront/internal/fault"
	"gptibo/storefront/internal/httpapi"
	"gptibo/storefront/internal/orders"
	"gptibo/storefront/internal/payments"
	"gptibo/storefront/internal/security"
)

func TestSimultaneousPaymentMethodsAndSingleIntent(t *testing.T) {
	owner, app, a, base, mail := identityFixture(t)
	admin := login(t, base, mail, "admin", "")
	ctx := context.Background()
	official := &paymentFake{account: "official"}
	ali := &channelFake{paymentFake: paymentFake{account: "merchant"}, id: "gatewaya-alipay"}
	wx := &channelFake{paymentFake: paymentFake{account: "merchant"}, id: "gatewaya-wxpay"}
	s := payments.Service{Store: app, Provider: official, Providers: map[string]payments.Provider{ali.ID(): ali, wx.ID(): wx}, NotifyOrigin: testOrigin, Checkout: true}
	h := httpapi.Router(app, "fixture", slog.New(slog.NewTextHandler(io.Discard, nil)), httpapi.Options{Origin: testOrigin, Auth: a, Payments: &s})
	for _, ids := range [][]string{{}, {"alipay", "gatewaya-alipay"}, {"gatewaya-wxpay", "gatewaya-wxpay"}, {"unknown"}} {
		object(t, request(h, "POST", "/api/admin/payments/settings", map[string]any{"providers": ids, "version": 0}, admin), 400)
	}
	object(t, request(h, "POST", "/api/admin/payments/settings", map[string]any{"providers": []string{ali.ID(), wx.ID()}, "version": 0}, nil), 401)
	settings := object(t, request(h, "POST", "/api/admin/payments/settings", map[string]any{"providers": []string{ali.ID(), wx.ID()}, "version": 0}, admin), 200)
	if len(settings["enabledProviders"].([]any)) != 2 {
		t.Fatal(settings)
	}
	execSQL(t, owner, `INSERT INTO catalog_plans(id,product_id,name,price_cents) VALUES('plus','chatgpt','Plus',1000)`)
	for _, id := range []string{"YMX-ALI", "YMX-WX", "YMX-RACE", "YMX-DISABLED"} {
		execSQL(t, owner, `INSERT INTO orders(id,last_event_id,owner_hash,idempotency_key,plan,plan_name,price_cents,mode,status,created_at,updated_at,customer_email) VALUES($1,'fixture','fixture',$1,'plus','Plus',1000,'live','pending',$2,$2,'buyer@example.test')`, id, time.Now().UnixMilli())
	}
	v, e := s.Action(ctx, "YMX-ALI", &orders.Access{OwnerHash: "fixture"}, "options", false)
	if e != nil || v["payment"] != nil || len(v["methods"].([]payments.MethodOption)) != 2 {
		t.Fatal(v, e)
	}
	var count int
	if e = owner.DB.QueryRow(ctx, `SELECT count(*) FROM payments`).Scan(&count); e != nil || count != 0 {
		t.Fatal("options created payment", count, e)
	}
	if _, e = s.Action(ctx, "YMX-ALI", &orders.Access{OwnerHash: "stranger"}, "options", false); fault.Status(e) != 404 {
		t.Fatal("ownership bypass", e)
	}
	if _, e = s.Action(ctx, "YMX-ALI", nil, "create", false, "bogus"); fault.Status(e) != 400 {
		t.Fatal("invalid method accepted", e)
	}
	for method, id := range map[string]string{"alipay": "YMX-ALI", "wxpay": "YMX-WX"} {
		v, e = s.Action(ctx, id, nil, "create", false, method)
		if e != nil || v["payment"].(map[string]any)["method"] != method {
			t.Fatal(v, e)
		}
	}
	if ali.creates != 1 || wx.creates != 1 || official.creates != 0 {
		t.Fatal("wrong provider created payment")
	}
	// Reading options never contacts the provider or changes the original binding.
	ali.queryErr = errors.New("upstream unavailable")
	v, e = s.Action(ctx, "YMX-ALI", nil, "options", false)
	if e != nil || len(v["methods"].([]payments.MethodOption)) != 0 || v["payment"].(map[string]any)["provider"] != ali.ID() {
		t.Fatal(v, e)
	}
	if _, e = s.Action(ctx, "YMX-ALI", nil, "create", false, "wxpay"); fault.Status(e) != 409 {
		t.Fatal("second intent accepted", e)
	}
	results := make(chan error, 2)
	start := make(chan struct{})
	for _, method := range []string{"alipay", "wxpay"} {
		go func(method string) {
			<-start
			_, err := s.Action(ctx, "YMX-RACE", nil, "create", false, method)
			results <- err
		}(method)
	}
	close(start)
	success := 0
	for range 2 {
		if err := <-results; err == nil {
			success++
		} else if fault.Status(err) != 409 {
			t.Fatal(err)
		}
	}
	if success != 1 || ali.creates+wx.creates != 3 {
		t.Fatal("concurrent choices created multiple intents", success)
	}
	object(t, request(h, "POST", "/api/admin/payments/settings", map[string]any{"providers": []string{ali.ID()}, "version": 1}, admin), 200)
	if _, e = s.Action(ctx, "YMX-DISABLED", nil, "create", false, "wxpay"); fault.Status(e) != 409 {
		t.Fatal("disabled method accepted", e)
	}
	wx.query = payments.Result{OrderID: "YMX-WX", Amount: 1000, Status: "paid", TradeNo: "WX-PAID"}
	if _, e = s.Action(ctx, "YMX-WX", nil, "query", false); e != nil {
		t.Fatal("disabled channel lost existing payment", e)
	}
	p, e := payments.Find(ctx, app.DB, "YMX-WX", false)
	if e != nil || p.State != "paid" || p.Provider != wx.ID() {
		t.Fatal(p, e)
	}
	// Invoice fees expose the same choices and preserve the chosen intent.
	object(t, request(h, "POST", "/api/admin/payments/settings", map[string]any{"providers": []string{ali.ID(), wx.ID()}, "version": 2}, admin), 200)
	object(t, request(h, "POST", "/api/admin/partners", map[string]any{"email": "methods@example.test"}, admin), 200)
	partner := login(t, base, mail, "partner", "methods@example.test")
	for _, method := range []string{"alipay", "wxpay"} {
		id := security.ID()
		object(t, request(h, "POST", "/api/partner/invoices", map[string]any{"key": id, "baseCents": 100, "includeFee": true, "title": "发票", "taxNumber": "TEST-TAX", "invoiceType": "ordinary", "email": "invoice@example.test"}, partner), 201)
		path := "/api/partner/invoices/" + id + "/payment"
		before := ali.creates + wx.creates
		v = object(t, request(h, "POST", path, map[string]any{"action": "options"}, partner), 200)
		if v["payment"] != nil || len(v["methods"].([]any)) != 2 || ali.creates+wx.creates != before {
			t.Fatal("invoice options created upstream payment", v)
		}
		v = object(t, request(h, "POST", path, map[string]any{"action": "create", "method": method}, partner), 200)
		if v["payment"].(map[string]any)["provider"] != "gatewaya-"+method {
			t.Fatal(v)
		}
		other := "wxpay"
		if method == "wxpay" {
			other = "alipay"
		}
		object(t, request(h, "POST", path, map[string]any{"action": "create", "method": other}, partner), 409)
	}
}
