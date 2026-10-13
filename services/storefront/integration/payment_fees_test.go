package integration_test

import (
	"context"
	"errors"
	"gptibo/storefront/internal/httpapi"
	"gptibo/storefront/internal/orders"
	"gptibo/storefront/internal/payments"
	"gptibo/storefront/internal/security"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"
)

type feeProvider struct {
	channelFake
	created           payments.Order
	amounts           []int64
	single, uncertain bool
}

func (p *feeProvider) Create(ctx context.Context, o payments.Order, notify string) (string, error) {
	p.created = o
	return p.channelFake.Create(ctx, o, notify)
}
func (p *feeProvider) SingleAttemptRefund() bool { return p.single }
func (p *feeProvider) QueryRefund(context.Context, payments.Order, payments.Refund) (payments.Refunded, error) {
	return payments.Refunded{}, nil
}
func (p *feeProvider) Refund(_ context.Context, _ payments.Order, r payments.Refund) (payments.Refunded, error) {
	p.amounts = append(p.amounts, r.Amount)
	if p.uncertain {
		return payments.Refunded{}, errors.New("synthetic timeout after submission")
	}
	return payments.Refunded{Confirmed: true, TradeNo: p.query.TradeNo}, nil
}
func TestFeeSnapshotPaymentAndProportionalRefunds(t *testing.T) {
	owner, app, a, base, mail := identityFixture(t)
	admin := login(t, base, mail, "admin", "")
	ctx := context.Background()
	fake := &feeProvider{channelFake: channelFake{paymentFake: paymentFake{account: "original"}, id: "gatewayb-alipay"}}
	wx := &channelFake{paymentFake: paymentFake{account: "original"}, id: "gatewayb-wxpay"}
	s := payments.Service{Store: app, Provider: fake, Providers: map[string]payments.Provider{fake.ID(): fake, wx.ID(): wx}, NotifyOrigin: testOrigin, Checkout: true}
	h := httpapi.Router(app, "fixture", slog.New(slog.NewTextHandler(io.Discard, nil)), httpapi.Options{Origin: testOrigin, Auth: a, Payments: &s})
	for _, rates := range []any{map[string]int{"gatewayb-alipay": -1}, map[string]int{"gatewayb-alipay": 10001}, map[string]int{"unknown": 140}, map[string]float64{"gatewayb-alipay": 1.4}} {
		object(t, request(h, "POST", "/api/admin/payments/settings", map[string]any{"providers": []string{fake.ID(), wx.ID()}, "version": 0, "feeRatesBps": rates}, admin), 400)
	}
	object(t, request(h, "POST", "/api/admin/payments/settings", map[string]any{"providers": []string{fake.ID(), wx.ID()}, "version": 0, "feeRatesBps": map[string]int{"gatewayb-alipay": 140, "gatewayb-wxpay": 200}}, admin), 200)
	methods, e := s.Methods(ctx, app.DB)
	if e != nil || len(methods) != 2 || methods[0].FeeBPS != 140 || methods[1].FeeBPS != 200 {
		t.Fatal(methods, e)
	}
	buyer := &http.Cookie{Name: "__Host-sf_buyer", Value: security.Token()}
	execSQL(t, owner, `INSERT INTO catalog_plans(id,product_id,name,price_cents) VALUES('gmail-account','gmail','Gmail',10000)`)
	execSQL(t, owner, `INSERT INTO orders(id,last_event_id,owner_hash,idempotency_key,plan,plan_name,price_cents,mode,status,created_at,updated_at) VALUES('YMX-FEE','fixture',$1,'fixture','gmail-account','Gmail',10000,'live','pending',$2,$2)`, security.Hash(buyer.Value), time.Now().UnixMilli())
	path := "/api/orders/YMX-FEE/payment"
	object(t, request(h, "POST", path, map[string]any{"action": "create", "method": "alipay"}, buyer), 409)
	object(t, request(h, "POST", path, map[string]any{"action": "create", "method": "alipay", "expectedFeeBps": 139}, buyer), 409)
	// Email-step preparation saves the exact same amount without an upstream call.
	tx, e := app.DB.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	o, e := orders.Get(ctx, tx, "YMX-FEE", nil, true)
	if e != nil {
		t.Fatal(e)
	}
	bps := int64(140)
	if e = s.Prepare(ctx, tx, o, orders.CreateInput{PaymentMethod: "alipay", ExpectedFeeBPS: &bps}); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	if fake.creates != 0 {
		t.Fatal("email step contacted provider")
	}
	object(t, request(h, "POST", path, map[string]any{"action": "create", "method": "alipay", "expectedFeeBps": 140}, buyer), 200)
	if fake.created.Amount != 10140 {
		t.Fatal("wrong gross amount", fake.created)
	}
	object(t, request(h, "POST", "/api/admin/payments/settings", map[string]any{"providers": []string{fake.ID()}, "version": 1, "feeRatesBps": map[string]int{"gatewayb-alipay": 220}}, admin), 200)
	object(t, request(h, "POST", path, map[string]any{"action": "create", "method": "alipay", "expectedFeeBps": 220}, buyer), 200)
	p, e := payments.Find(ctx, app.DB, "YMX-FEE", false)
	if e != nil || p.Amount != 10140 || p.Fee != 140 || p.FeeBPS != 140 || fake.creates != 1 {
		t.Fatal(p, e)
	}
	r := payments.Result{OrderID: o.ID, Amount: 10000, Status: "paid", TradeNo: "GATEWAYB-FEE"}
	if e = s.Apply(ctx, p, r); e == nil {
		t.Fatal("base amount accepted instead of gross")
	}
	r.Amount = 10140
	fake.query = r
	for range 2 {
		if e = s.Apply(ctx, p, r); e != nil {
			t.Fatal(e)
		}
	}
	execSQL(t, owner, `UPDATE orders SET status='success' WHERE id='YMX-FEE'`)
	var sale int64
	if e = owner.DB.QueryRow(ctx, `SELECT sale_cents FROM accounting_candidates WHERE order_id='YMX-FEE'`).Scan(&sale); e != nil || sale != 10140 {
		t.Fatal("platform bookkeeping omitted fee", sale, e)
	}
	stats := object(t, request(h, "GET", "/api/admin/overview", nil, admin), 200)["stats"].(map[string]any)
	if stats["paid_amount"] != float64(10140) {
		t.Fatal("overview omitted fee", stats)
	}
	for i, amount := range []int64{3333, 6667} {
		var version int64
		if e = owner.DB.QueryRow(ctx, `SELECT version FROM orders WHERE id='YMX-FEE'`).Scan(&version); e != nil {
			t.Fatal(e)
		}
		object(t, request(h, "POST", "/api/orders/YMX-FEE/refunds", map[string]any{"key": security.ID(), "reason": "申请退款", "version": version}, buyer), 201)
		var id string
		if e = owner.DB.QueryRow(ctx, `SELECT id FROM refund_cases WHERE order_id='YMX-FEE' AND status='pending'`).Scan(&id); e != nil {
			t.Fatal(e)
		}
		object(t, request(h, "POST", "/api/admin/refunds/"+id, map[string]any{"action": "approve", "version": 0, "amountCents": amount, "note": "批准商品款退款"}, admin), 200)
		object(t, request(h, "POST", "/api/admin/refunds/"+id, map[string]any{"action": "execute-refund", "version": 1, "note": "原路退还商品与手续费"}, admin), 200)
		want := []int64{3380, 6760}[i]
		if fake.amounts[i] != want {
			t.Fatal("wrong proportional gross refund", fake.amounts)
		}
	}
	o, e = orders.Get(ctx, app.DB, o.ID, nil, false)
	if e != nil || o.Price != 10000 || o.Refunded != 10000 || o.Hold != 0 {
		t.Fatal(o, e)
	}
	var feeTotal int64
	if e = owner.DB.QueryRow(ctx, `SELECT sum(fee_cents)::bigint FROM payment_refund_attempts WHERE order_id='YMX-FEE' AND state='confirmed'`).Scan(&feeTotal); e != nil || feeTotal != 140 {
		t.Fatal(feeTotal, e)
	}
	stats = object(t, request(h, "GET", "/api/admin/overview", nil, admin), 200)["stats"].(map[string]any)
	if stats["paid_amount"] != float64(0) || stats["refund_amount"] != float64(10140) {
		t.Fatal("overview refund omitted fee", stats)
	}
	var returned int64
	if e = owner.DB.QueryRow(ctx, `SELECT refund_cents FROM accounting_candidates WHERE order_id='YMX-FEE'`).Scan(&returned); e != nil || returned != 10140 {
		t.Fatal("bookkeeping refund omitted fee", returned, e)
	}
	detail := object(t, request(h, "GET", "/api/orders/YMX-FEE", nil, buyer), 200)["order"].(map[string]any)
	if detail["price"] != float64(100) || detail["paymentSummary"].(map[string]any)["refundedFeeCents"] != float64(140) {
		t.Fatal("fee mixed into merchandise accounting", detail)
	}
}

func TestGATEWAYBRefundTimeoutIsNeverResubmittedAndCanBeReconciled(t *testing.T) {
	owner, app, a, base, mail := identityFixture(t)
	admin := login(t, base, mail, "admin", "")
	fake := &feeProvider{channelFake: channelFake{paymentFake: paymentFake{account: "original"}, id: "gatewayb-wxpay"}, single: true, uncertain: true}
	s := payments.Service{Store: app, Provider: fake}
	h := httpapi.Router(app, "fixture", slog.New(slog.NewTextHandler(io.Discard, nil)), httpapi.Options{Origin: testOrigin, Auth: a, Payments: &s})
	buyer := &http.Cookie{Name: "__Host-sf_buyer", Value: security.Token()}
	now := time.Now().UnixMilli()
	execSQL(t, owner, `INSERT INTO catalog_plans(id,product_id,name,price_cents) VALUES('gmail-account','gmail','Gmail',10000)`)
	execSQL(t, owner, `INSERT INTO orders(id,last_event_id,owner_hash,idempotency_key,plan,plan_name,price_cents,mode,payment_status,status,created_at,updated_at) VALUES('YMX-UNKNOWN','fixture',$1,'fixture','gmail-account','Gmail',10000,'live','paid','session',$2,$2)`, security.Hash(buyer.Value), now)
	execSQL(t, owner, `INSERT INTO payments(order_id,provider,account,amount_cents,fee_cents,fee_bps,state,trade_no,expires_at,created_at,updated_at) VALUES('YMX-UNKNOWN','gatewayb-wxpay','original',10140,140,140,'paid','GATEWAYB-UNKNOWN',$1,$1,$1)`, now)
	object(t, request(h, "POST", "/api/orders/YMX-UNKNOWN/refunds", map[string]any{"key": security.ID(), "reason": "退款", "version": 0}, buyer), 201)
	var id string
	if e := owner.DB.QueryRow(context.Background(), `SELECT id FROM refund_cases WHERE order_id='YMX-UNKNOWN'`).Scan(&id); e != nil {
		t.Fatal(e)
	}
	path := "/api/admin/refunds/" + id
	object(t, request(h, "POST", path, map[string]any{"action": "approve", "version": 0, "amountCents": 10000, "note": "同意"}, admin), 200)
	for range 2 {
		object(t, request(h, "POST", path, map[string]any{"action": "execute-refund", "version": 1, "note": "原路退回"}, admin), 409)
	}
	if len(fake.amounts) != 1 || fake.amounts[0] != 10140 {
		t.Fatal("ambiguous refund replayed", fake.amounts)
	}
	o, e := orders.Get(context.Background(), app.DB, "YMX-UNKNOWN", nil, false)
	if e != nil || o.Hold != 1 || o.Refunded != 0 {
		t.Fatal("uncertain result released hold", e)
	}
	in := map[string]any{"action": "confirm-payment-refund", "version": 1, "amountCents": 10000, "reference": "REAL-REFUND-RECEIPT", "note": "商户后台已核对退款成功"}
	object(t, request(h, "POST", path, in, nil), 401)
	object(t, request(h, "POST", path, in, admin), 409)
	in["amountCents"] = 10140
	// A local audit failure rolls back both money state and manual confirmation.
	execSQL(t, owner, `CREATE FUNCTION reject_refund_event() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture'; END $$; CREATE TRIGGER reject_refund BEFORE INSERT ON refund_events FOR EACH ROW EXECUTE FUNCTION reject_refund_event()`)
	object(t, request(h, "POST", path, in, admin), 503)
	execSQL(t, owner, `DROP TRIGGER reject_refund ON refund_events`)
	object(t, request(h, "POST", path, in, admin), 200)
	object(t, request(h, "POST", path, in, admin), 409)
	if len(fake.amounts) != 1 {
		t.Fatal("manual reconciliation submitted another transfer")
	}
	o, e = orders.Get(context.Background(), app.DB, "YMX-UNKNOWN", nil, false)
	if e != nil || o.Hold != 0 || o.Refunded != 10000 {
		t.Fatal(o, e)
	}
}
