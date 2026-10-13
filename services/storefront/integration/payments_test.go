package integration_test

import (
	"context"
	"errors"
	"gptibo/storefront/internal/httpapi"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"gptibo/storefront/internal/orders"
	"gptibo/storefront/internal/payments"
	"gptibo/storefront/internal/security"
)

type paymentFake struct {
	account          string
	query            payments.Result
	queryErr         error
	close            bool
	creates, refunds int
	refund           payments.Refunded
}

func (p *paymentFake) ID() string      { return "alipay" }
func (p *paymentFake) Account() string { return p.account }
func (p *paymentFake) Create(context.Context, payments.Order, string) (string, error) {
	p.creates++
	return "https://qr.alipay.com/fixture", nil
}
func (p *paymentFake) Query(context.Context, payments.Order) (payments.Result, error) {
	return p.query, p.queryErr
}
func (p *paymentFake) Close(context.Context, payments.Order) (bool, error) { return p.close, nil }
func (p *paymentFake) Refund(context.Context, payments.Order, payments.Refund) (payments.Refunded, error) {
	p.refunds++
	return p.refund, nil
}
func (p *paymentFake) QueryRefund(context.Context, payments.Order, payments.Refund) (payments.Refunded, error) {
	return p.refund, nil
}
func (p *paymentFake) Notification(*http.Request) (payments.Result, error) {
	return p.query, p.queryErr
}
func TestPaymentReceiptsAreAtomicAndMonotonic(t *testing.T) {
	owner, app := fixture(t, true)
	now := time.Now().UnixMilli()
	execSQL(t, owner, `INSERT INTO catalog_plans(id,product_id,name,price_cents) VALUES('plus','chatgpt','Plus',1000)`)
	execSQL(t, owner, `INSERT INTO orders(id,last_event_id,owner_hash,idempotency_key,plan,plan_name,price_cents,mode,status,created_at,updated_at,customer_email) VALUES('YMX-PAY','fixture','fixture','fixture','plus','Plus',1000,'live','pending',$1,$1,'buyer@example.test')`, now)
	execSQL(t, owner, `INSERT INTO payments(order_id,provider,account,amount_cents,expires_at,created_at,updated_at) VALUES('YMX-PAY','alipay','original',1000,$1,$2,$2)`, now+900000, now)
	s := payments.Service{Store: app}
	ctx := context.Background()
	p, e := payments.Find(ctx, app.DB, "YMX-PAY", false)
	if e != nil {
		t.Fatal(e)
	}
	result := payments.Result{OrderID: p.OrderID, Amount: 1001, Status: "paid", TradeNo: "TRADE-1"}
	if e = s.Apply(ctx, p, result); e == nil {
		t.Fatal("wrong amount accepted")
	}
	result.Amount = 1000
	execSQL(t, owner, `CREATE FUNCTION reject_payment_event() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic failure'; END $$; CREATE TRIGGER reject_payment BEFORE INSERT ON order_events FOR EACH ROW EXECUTE FUNCTION reject_payment_event()`)
	if e = s.Apply(ctx, p, result); e == nil {
		t.Fatal("audit failure ignored")
	}
	fresh, e := payments.Find(ctx, app.DB, p.OrderID, false)
	if e != nil || fresh.State != "pending" {
		t.Fatal("receipt committed without order event", e)
	}
	execSQL(t, owner, `DROP TRIGGER reject_payment ON order_events`)
	for range 2 {
		if e = s.Apply(ctx, p, result); e != nil {
			t.Fatal(e)
		}
	}
	result.Status = "closed"
	if e = s.Apply(ctx, p, result); e != nil {
		t.Fatal(e)
	}
	o, e := orders.Get(ctx, app.DB, p.OrderID, nil, false)
	if e != nil || o.Payment != "paid" || o.Status != "session" || o.Version != 1 {
		t.Fatal("duplicate/late notification corrupted order", e)
	}
	result.TradeNo = "WRONG"
	if e = s.Apply(ctx, p, result); e == nil {
		t.Fatal("conflicting trade accepted")
	}
	var events, reminders int
	_ = owner.DB.QueryRow(ctx, `SELECT count(*) FROM order_events WHERE order_id=$1`, p.OrderID).Scan(&events)
	_ = owner.DB.QueryRow(ctx, `SELECT count(*) FROM recharge_reminders WHERE order_id=$1`, p.OrderID).Scan(&reminders)
	if events != 1 || reminders != 1 {
		t.Fatal("duplicate effects")
	}
}
func TestPaymentUnknownResultsAndOriginalMerchantBinding(t *testing.T) {
	owner, app := fixture(t, true)
	ctx := context.Background()
	now := time.Now().UnixMilli()
	ownerHash := security.Hash(security.Token())
	access := orders.Access{OwnerHash: ownerHash}
	execSQL(t, owner, `INSERT INTO catalog_plans(id,product_id,name,price_cents) VALUES('gmail-account','gmail','Gmail',1000)`)
	execSQL(t, owner, `INSERT INTO orders(id,last_event_id,owner_hash,idempotency_key,plan,plan_name,price_cents,mode,status,created_at,updated_at) VALUES('YMX-GATEWAY','fixture',$1,'fixture','gmail-account','Gmail',1000,'live','pending',$2,$2)`, ownerHash, now)
	fake := &paymentFake{account: "original", query: payments.Result{OrderID: "YMX-GATEWAY", Amount: 1000, Status: "not_found"}}
	s := payments.Service{Store: app, Provider: fake, Checkout: true, NotifyOrigin: "https://storefront.example.test"}
	for range 2 {
		if _, e := s.Action(ctx, "YMX-GATEWAY", &access, "create", false); e != nil {
			t.Fatal(e)
		}
	}
	if fake.creates != 1 {
		t.Fatal("repeated QR creation")
	}
	if _, e := s.Action(ctx, "YMX-GATEWAY", &access, "close", false); e != nil {
		t.Fatal(e)
	}
	p, e := payments.Find(ctx, app.DB, "YMX-GATEWAY", false)
	if e != nil || p.State != "pending" {
		t.Fatal("fresh unscanned QR closed as missing")
	}
	fake.queryErr = errors.New("synthetic timeout")
	if _, e = s.Action(ctx, p.OrderID, &access, "query", false); e == nil {
		t.Fatal("timeout became success")
	}
	p, _ = payments.Find(ctx, app.DB, p.OrderID, false)
	if p.State != "pending" || p.Lease != nil {
		t.Fatal("unknown result changed state or leaked lease")
	}
	fake.queryErr = nil
	fake.account = "replacement"
	if _, e = s.Action(ctx, p.OrderID, &access, "query", false); e == nil {
		t.Fatal("old order rerouted to new merchant")
	}
	fake.account = "original"
	execSQL(t, owner, `UPDATE payments SET expires_at=$1 WHERE order_id=$2`, now-130000, p.OrderID)
	if _, e = s.Action(ctx, p.OrderID, &access, "close", false); e != nil {
		t.Fatal(e)
	}
	p, _ = payments.Find(ctx, app.DB, p.OrderID, false)
	if p.State != "closed" {
		t.Fatal("expired missing QR not retired")
	}
	fake.query.Status = "paid"
	fake.query.TradeNo = "TRADE-LATE"
	if e = s.Apply(ctx, p, fake.query); e != nil {
		t.Fatal(e)
	}
	o, _ := orders.Get(ctx, app.DB, p.OrderID, nil, false)
	if o.Payment != "paid" || o.Status != "session" {
		t.Fatal("late verified payment lost")
	}
}

func TestAdminPaymentTestRoutesAndCallbackWithoutBrowserOrigin(t *testing.T) {
	owner, app, a, base, mail := identityFixture(t)
	admin := login(t, base, mail, "admin", "")
	execSQL(t, owner, `INSERT INTO catalog_plans(id,product_id,name,price_cents) VALUES('plus','chatgpt','Plus',1000)`)
	fake := &paymentFake{account: "fixture"}
	s := payments.Service{Store: app, Provider: fake, NotifyOrigin: testOrigin}
	h := httpapi.Router(app, "fixture", slog.New(slog.NewTextHandler(io.Discard, nil)), httpapi.Options{Origin: testOrigin, Auth: a, Payments: &s, PaymentTesting: true, TrafficProtection: true})
	in := map[string]any{"key": security.ID(), "email": "payment-test@example.test"}
	object(t, request(h, "POST", "/api/admin/payments/test", in, nil), 401)
	created := object(t, request(h, "POST", "/api/admin/payments/test", in, admin), 201)["order"].(map[string]any)
	id := created["id"].(string)
	object(t, request(h, "POST", "/api/admin/payments/test", in, admin), 200)
	in["key"] = security.ID()
	object(t, request(h, "POST", "/api/admin/payments/test", in, admin), 409)
	object(t, request(h, "POST", "/api/admin/payments/test/"+id, map[string]any{"action": "create"}, admin), 200)
	fake.query = payments.Result{OrderID: id, Amount: 1, Status: "paid", TradeNo: "TEST-TRADE"}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/api/payments/notify/alipay", nil))
	if w.Code != 200 || w.Body.String() != "success" {
		t.Fatal("valid server callback rejected", w.Code)
	}
	fake.refund = payments.Refunded{Confirmed: true, TradeNo: "TEST-TRADE"}
	object(t, request(h, "POST", "/api/admin/payments/test/"+id, map[string]any{"action": "refund"}, admin), 200)
	object(t, request(h, "POST", "/api/admin/payments/test/"+id, map[string]any{"action": "refund"}, admin), 200)
	got := object(t, request(h, "GET", "/api/admin/payments/test/"+id, nil, admin), 200)["order"].(map[string]any)
	if got["refundedCents"] != float64(1) {
		t.Fatal("one cent test refund missing")
	}
	overview := object(t, request(h, "GET", "/api/admin/overview", nil, admin), 200)
	if overview["stats"].(map[string]any)["total"] != float64(0) {
		t.Fatal("payment test counted as sale")
	}
}
