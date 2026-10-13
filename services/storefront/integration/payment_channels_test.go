package integration_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"gptibo/storefront/internal/httpapi"
	"gptibo/storefront/internal/payments"
	"gptibo/storefront/internal/security"
)

type channelFake struct {
	paymentFake
	id     string
	notify string
}

func (p *channelFake) ID() string { return p.id }
func (p *channelFake) Create(ctx context.Context, o payments.Order, notify string) (string, error) {
	p.notify = notify
	return p.paymentFake.Create(ctx, o, notify)
}

func TestChannelSwitchPreservesOriginalPaymentsAndInvoices(t *testing.T) {
	owner, app, a, base, mail := identityFixture(t)
	admin := login(t, base, mail, "admin", "")
	ctx := context.Background()
	old := &paymentFake{account: "official"}
	newChannel := &channelFake{paymentFake: paymentFake{account: "gatewaya-merchant"}, id: "gatewaya-wxpay"}
	s := payments.Service{Store: app, Provider: old, Providers: map[string]payments.Provider{"gatewaya-wxpay": newChannel}, NotifyOrigin: testOrigin, Checkout: true}
	h := httpapi.Router(app, "fixture", slog.New(slog.NewTextHandler(io.Discard, nil)), httpapi.Options{Origin: testOrigin, Auth: a, Payments: &s})
	object(t, request(h, "GET", "/api/admin/payments/settings", nil, nil), 401)
	object(t, request(h, "POST", "/api/admin/payments/settings", map[string]any{"provider": "gatewaya-wxpay", "version": 0}, nil), 401)
	initial := object(t, request(h, "GET", "/api/admin/payments/settings", nil, admin), 200)
	if initial["provider"] != "alipay" || initial["version"] != float64(0) {
		t.Fatal(initial)
	}
	object(t, request(h, "POST", "/api/admin/payments/settings", map[string]any{"provider": "gatewaya-alipay", "version": 0}, admin), 409)
	object(t, request(h, "POST", "/api/admin/payments/settings", map[string]any{"provider": "gatewaya-wxpay"}, admin), 400)
	execSQL(t, owner, `INSERT INTO catalog_plans(id,product_id,name,price_cents) VALUES('plus','chatgpt','Plus',1000)`)
	for _, id := range []string{"YMX-OLD", "YMX-NEW"} {
		execSQL(t, owner, `INSERT INTO orders(id,last_event_id,owner_hash,idempotency_key,plan,plan_name,price_cents,mode,status,created_at,updated_at,customer_email) VALUES($1,'fixture','fixture',$1,'plus','Plus',1000,'live','pending',$2,$2,'buyer@example.test')`, id, time.Now().UnixMilli())
	}
	if _, e := s.Action(ctx, "YMX-OLD", nil, "create", false); e != nil {
		t.Fatal(e)
	}
	object(t, request(h, "POST", "/api/admin/payments/settings", map[string]any{"provider": "gatewaya-wxpay", "version": 0}, admin), 200)
	object(t, request(h, "POST", "/api/admin/payments/settings", map[string]any{"provider": "alipay", "version": 0}, admin), 409)
	if _, e := s.Action(ctx, "YMX-NEW", nil, "create", false); e != nil {
		t.Fatal(e)
	}
	if newChannel.notify != testOrigin+"/api/payments/notify/gatewaya-wxpay" {
		t.Fatal(newChannel.notify)
	}
	for id, provider := range map[string]string{"YMX-OLD": "alipay", "YMX-NEW": "gatewaya-wxpay"} {
		p, e := payments.Find(ctx, app.DB, id, false)
		if e != nil || p.Provider != provider {
			t.Fatal(p, e)
		}
	}
	old.query = payments.Result{OrderID: "YMX-OLD", Amount: 1000, Status: "paid", TradeNo: "OLD-TRADE"}
	if _, e := s.Action(ctx, "YMX-OLD", nil, "query", false); e != nil {
		t.Fatal("old channel lost", e)
	}
	newChannel.query = payments.Result{OrderID: "YMX-NEW", Amount: 1000, Status: "paid", TradeNo: "NEW-TRADE"}
	for range 2 {
		req := httptest.NewRequest(http.MethodGet, testOrigin+"/api/payments/notify/gatewaya-wxpay", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 200 || rec.Body.String() != "success" {
			t.Fatal("originless callback failed", rec.Code)
		}
	}
	newChannel.query.OrderID = "YMX-OLD"
	if s.Notify(httptest.NewRequest("GET", testOrigin, nil), "gatewaya-wxpay") {
		t.Fatal("cross-channel callback accepted")
	}
	// Refunds still resolve the merchant snapshot, even after switching defaults.
	execSQL(t, owner, `UPDATE orders SET refund_hold=1 WHERE id='YMX-OLD'`)
	old.refund = payments.Refunded{Confirmed: true, TradeNo: "OLD-TRADE"}
	if _, e := s.RefundOriginal(ctx, "YMX-OLD", "CASE-OLD", 1000); e != nil {
		t.Fatal(e)
	}
	object(t, request(h, "POST", "/api/admin/partners", map[string]any{"email": "channels@example.test"}, admin), 200)
	partner := login(t, base, mail, "partner", "channels@example.test")
	id := security.ID()
	in := map[string]any{"key": id, "baseCents": 100, "includeFee": true, "title": "发票", "taxNumber": "TEST-TAX", "invoiceType": "ordinary", "email": "invoice@example.test"}
	object(t, request(h, "POST", "/api/partner/invoices", in, partner), 201)
	v := object(t, request(h, "POST", "/api/partner/invoices/"+id+"/payment", map[string]any{"action": "create"}, partner), 200)
	if v["payment"].(map[string]any)["provider"] != "gatewaya-wxpay" || newChannel.notify != testOrigin+"/api/invoices/notify/gatewaya-wxpay" {
		t.Fatal("invoice did not use chosen channel")
	}
	object(t, request(h, "POST", "/api/admin/payments/settings", map[string]any{"provider": "alipay", "version": 1}, admin), 200)
	var pid string
	if e := owner.DB.QueryRow(ctx, `SELECT id FROM invoice_payments WHERE invoice_id=$1`, id).Scan(&pid); e != nil {
		t.Fatal(e)
	}
	newChannel.query = payments.Result{OrderID: pid, Amount: 5, Status: "paid", TradeNo: "INVOICE-NEW"}
	for range 2 {
		req := httptest.NewRequest("GET", testOrigin+"/api/invoices/notify/gatewaya-wxpay", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatal("old invoice callback lost", rec.Code)
		}
	}
	var count int
	if e := owner.DB.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE action='payment-channel-changed'`).Scan(&count); e != nil || count != 2 {
		t.Fatal("switch audit missing", count, e)
	}
	execSQL(t, owner, `CREATE FUNCTION reject_channel_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic failure'; END $$; CREATE TRIGGER reject_channel_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_channel_audit()`)
	if e := s.SetChannel(ctx, a, "expired-admin-token", "gatewaya-wxpay", 2); e == nil {
		t.Fatal("unauthenticated direct write accepted")
	}
	// A failed audit must roll back the channel change as well.
	object(t, request(h, "POST", "/api/admin/payments/settings", map[string]any{"provider": "gatewaya-wxpay", "version": 2}, admin), 503)
	settings, e := s.Settings(ctx)
	if e != nil || settings.Provider != "alipay" || settings.Version != 2 {
		t.Fatal("channel committed without audit", settings, e)
	}
	// A removed original merchant must fail closed, never fall back to default.
	delete(s.Providers, "gatewaya-wxpay")
	if _, e := s.Resolve("gatewaya-wxpay", "gatewaya-merchant"); e == nil {
		t.Fatal("missing provider silently replaced")
	}
}

type refundFirstFake struct {
	channelFake
	keys       map[string]bool
	attempts   int
	wrongTrade bool
}

func (p *refundFirstFake) SubmitRefundFirst() bool { return true }
func (p *refundFirstFake) Refund(_ context.Context, _ payments.Order, f payments.Refund) (payments.Refunded, error) {
	p.attempts++
	p.keys[f.ID] = true
	return payments.Refunded{}, errors.New("response lost after upstream refund")
}
func (p *refundFirstFake) QueryRefund(_ context.Context, _ payments.Order, f payments.Refund) (payments.Refunded, error) {
	if !p.keys[f.ID] {
		return payments.Refunded{}, errors.New("unknown upstream error")
	}
	trade := "TRADE-REFUND"
	if p.wrongTrade {
		trade = "ANOTHER-TRADE"
	}
	return payments.Refunded{Confirmed: true, TradeNo: trade}, nil
}
func TestRefundFirstUsesStableKeyAndReconcilesAmbiguousSubmission(t *testing.T) {
	owner, app := fixture(t, true)
	ctx := context.Background()
	now := time.Now().UnixMilli()
	execSQL(t, owner, `INSERT INTO catalog_plans(id,product_id,name,price_cents) VALUES('plus','chatgpt','Plus',1000)`)
	execSQL(t, owner, `INSERT INTO orders(id,last_event_id,owner_hash,idempotency_key,plan,plan_name,price_cents,mode,status,payment_status,refund_hold,created_at,updated_at,customer_email) VALUES('YMX-RF','fixture','fixture','fixture','plus','Plus',1000,'live','session','paid',1,$1,$1,'buyer@example.test')`, now)
	execSQL(t, owner, `INSERT INTO payments(order_id,provider,account,amount_cents,state,trade_no,expires_at,created_at,updated_at) VALUES('YMX-RF','gatewaya-alipay','merchant',1000,'paid','TRADE-REFUND',$1,$1,$1)`, now)
	p := &refundFirstFake{channelFake: channelFake{paymentFake: paymentFake{account: "merchant"}, id: "gatewaya-alipay"}, keys: map[string]bool{}}
	s := payments.Service{Store: app, Provider: &paymentFake{account: "default"}, Providers: map[string]payments.Provider{p.ID(): p}}
	p.wrongTrade = true
	if _, e := s.RefundOriginal(ctx, "YMX-RF", "CASE-ONE", 1000); e == nil {
		t.Fatal("wrong transaction refund accepted")
	}
	p.wrongTrade = false
	for range 2 {
		if _, e := s.RefundOriginal(ctx, "YMX-RF", "CASE-ONE", 1000); e != nil {
			t.Fatal(e)
		}
	}
	if len(p.keys) != 1 || !p.keys["CASE-ONE"] || p.attempts != 2 {
		t.Fatal("retry changed refund identity")
	}
	if _, e := s.RefundOriginal(ctx, "YMX-RF", "CASE-ONE", 999); e == nil {
		t.Fatal("confirmed refund amount changed")
	}
}
