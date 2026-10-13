package integration_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gptibo/storefront/internal/httpapi"
	"gptibo/storefront/internal/refunds"
	"gptibo/storefront/internal/security"
	"gptibo/storefront/internal/store"
	"gptibo/storefront/internal/recharge"
)

type customerRefundFixture struct {
	owner, app *store.Store
	supplier   *recharge.Service
	fake       *rechargeFake
	h          http.Handler
	buyer      *http.Cookie
	id         string
	input      map[string]any
}

func newCustomerRefundFixture(t *testing.T) customerRefundFixture {
	t.Helper()
	owner, app, auth, _, _ := identityFixture(t)
	client, fake := newRechargeFake(t)
	supplier := &recharge.Service{Store: app, Client: client, Checkout: true, Products: map[string]string{"plus": "plus"}, Notify: "https://dev.example.test/api/recharge/webhook"}
	h := httpapi.Router(app, "fixture", slog.New(slog.NewTextHandler(io.Discard, nil)), httpapi.Options{Origin: testOrigin, Auth: auth, Recharge: supplier})
	execSQL(t, owner, `INSERT INTO catalog_plans(id,product_id,name,price_cents,enabled) VALUES('plus','chatgpt','Plus',10000,true)`)
	buyer := &http.Cookie{Name: "__Host-sf_buyer", Value: security.Token()}
	created := object(t, request(h, "POST", "/api/orders", map[string]any{"plan": "plus", "email": "buyer@example.test", "key": security.ID(), "expectedPrice": 10000}, buyer), 201)["order"].(map[string]any)
	id := created["id"].(string)
	fake.order.Payment = "paid"
	if e := supplier.Sync(context.Background(), id, true); e != nil {
		t.Fatal(e)
	}
	return customerRefundFixture{owner, app, supplier, fake, h, buyer, id, map[string]any{"key": security.ID(), "version": 1, "reason": "未开通，申请全额退款"}}
}

func (f customerRefundFixture) path() string { return "/api/orders/" + f.id + "/refunds" }
func (f customerRefundFixture) read(t *testing.T) map[string]any {
	t.Helper()
	return object(t, request(f.h, "GET", f.path(), nil, f.buyer), 200)["cases"].([]any)[0].(map[string]any)
}
func (f customerRefundFixture) reconcile() error {
	if _, e := f.owner.DB.Exec(context.Background(), `UPDATE recharge_refunds SET updated_at=0 WHERE order_id=$1`, f.id); e != nil {
		return e
	}
	s := refunds.Service{Store: f.app, Supplier: f.supplier}
	return s.Reconcile(context.Background())
}
func (f customerRefundFixture) ledger(t *testing.T, status string, amount, hold int64) {
	t.Helper()
	var gotStatus string
	var gotAmount, gotHold, count int64
	e := f.owner.DB.QueryRow(context.Background(), `SELECT r.status,o.refunded_cents,o.refund_hold,(SELECT count(*) FROM refund_cases WHERE order_id=o.id) FROM orders o JOIN refund_cases r ON r.order_id=o.id WHERE o.id=$1`, f.id).Scan(&gotStatus, &gotAmount, &gotHold, &count)
	if e != nil || gotStatus != status || gotAmount != amount || gotHold != hold || count != 1 {
		t.Fatalf("ledger: status=%s amount=%d hold=%d count=%d err=%v", gotStatus, gotAmount, gotHold, count, e)
	}
}

func TestRechargeCustomerRefundImmediateSuccessAndDuplicate(t *testing.T) {
	f := newCustomerRefundFixture(t)
	f.fake.refundStatus, f.fake.refundAuto = "succeeded", true
	policy := object(t, request(f.h, "GET", f.path(), nil, f.buyer), 200)
	if policy["automaticPlatformRefund"] != true {
		t.Fatal("unfinished order lost automatic submission", policy)
	}
	object(t, request(f.h, "POST", f.path(), f.input, &http.Cookie{Name: f.buyer.Name, Value: security.Token()}), 404)
	if f.fake.refundCalls != 0 {
		t.Fatal("unauthorized refund submitted")
	}
	object(t, request(f.h, "POST", f.path(), f.input, f.buyer), 201)
	f.ledger(t, "refunded", 10000, 0)
	object(t, request(f.h, "POST", f.path(), f.input, f.buyer), 200)
	if e := f.reconcile(); e != nil {
		t.Fatal(e)
	}
	if f.fake.refundCalls != 1 || !strings.Contains(f.fake.refundBodies[0], "未开通，申请全额退款") {
		t.Fatal("claim not submitted once with original reason")
	}
	f.input["reason"] = "changed"
	object(t, request(f.h, "POST", f.path(), f.input, f.buyer), 409)
	row := f.read(t)
	if row["platform_status"] != "succeeded" || row["remote_id"] != nil {
		t.Fatal("customer status missing or remote ID exposed", row)
	}
}

func TestRechargeCustomerRefundPendingStatesRemainFrozen(t *testing.T) {
	for _, status := range []string{"requested", "approved", "processing", "failed"} {
		t.Run(status, func(t *testing.T) {
			f := newCustomerRefundFixture(t)
			f.fake.refundStatus = status
			object(t, request(f.h, "POST", f.path(), f.input, f.buyer), 201)
			f.ledger(t, "approved", 0, 1)
			row := f.read(t)
			if row["platform_status"] != status {
				t.Fatal(row)
			}
			object(t, request(f.h, "POST", f.path(), map[string]any{"action": "cancel", "id": row["id"], "version": row["version"]}, f.buyer), 409)
			object(t, request(f.h, "POST", f.path(), f.input, f.buyer), 200)
			object(t, request(f.h, "POST", f.path(), map[string]any{"key": security.ID(), "reason": "new claim", "version": 2}, f.buyer), 409)
			f.fake.refundStatus = "succeeded"
			f.fake.order.Payment, f.fake.order.Refunded, f.fake.order.Margin = "refunded", "100.00", "-60.00"
			if e := f.reconcile(); e != nil {
				t.Fatal(e)
			}
			if e := f.reconcile(); e != nil {
				t.Fatal(e)
			}
			f.ledger(t, "refunded", 10000, 0)
			if f.fake.refundCalls != 1 {
				t.Fatal("polling created another refund")
			}
		})
	}
}

func TestRechargeCustomerRefundLostSuccessResponseRecoversOriginal(t *testing.T) {
	f := newCustomerRefundFixture(t)
	f.fake.refundStatus, f.fake.refundAuto = "succeeded", true
	f.supplier.Client.HTTP.Transport = rechargeTransport(func(r *http.Request) (*http.Response, error) {
		resp, e := f.fake.round(r)
		if r.URL.Path == "/v1/orders/original/refunds" && f.fake.refundCalls == 1 {
			resp.Body.Close()
			return nil, errors.New("response lost after refund")
		}
		return resp, e
	})
	object(t, request(f.h, "POST", f.path(), f.input, f.buyer), 201)
	f.ledger(t, "approved", 0, 1)
	if e := f.supplier.Sync(context.Background(), f.id, false); e != nil {
		t.Fatal(e)
	}
	if e := f.reconcile(); e != nil {
		t.Fatal(e)
	}
	f.ledger(t, "refunded", 10000, 0)
	if f.fake.refundCalls != 2 || f.fake.refundKeys[0] != f.fake.refundKeys[1] || f.fake.refundBodies[0] != f.fake.refundBodies[1] {
		t.Fatal("lost response did not reuse original business key and bytes")
	}
}

func TestRechargeCustomerRefundQueuedBeforeFirstCallAndAuditRollback(t *testing.T) {
	f := newCustomerRefundFixture(t)
	f.fake.refundStatus = "requested"
	execSQL(t, f.owner, `CREATE FUNCTION reject_refund_event() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.actor='system' THEN RAISE EXCEPTION 'fixture'; END IF; RETURN NEW; END $$`)
	execSQL(t, f.owner, `CREATE TRIGGER reject_refund_event BEFORE INSERT ON refund_events FOR EACH ROW EXECUTE FUNCTION reject_refund_event()`)
	object(t, request(f.h, "POST", f.path(), f.input, f.buyer), 503)
	var count, hold int64
	if e := f.owner.DB.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM refund_cases),refund_hold FROM orders WHERE id=$1`, f.id).Scan(&count, &hold); e != nil || count != 0 || hold != 0 || f.fake.refundCalls != 0 {
		t.Fatal("failed audit leaked intent or hold", e)
	}
	execSQL(t, f.owner, `DROP TRIGGER reject_refund_event ON refund_events`)
	// Simulate a process that persisted the intent but cannot reach the supplier.
	f.supplier.Client.HTTP.Transport = rechargeTransport(func(*http.Request) (*http.Response, error) { return nil, errors.New("offline") })
	object(t, request(f.h, "POST", f.path(), f.input, f.buyer), 201)
	f.ledger(t, "approved", 0, 1)
	if f.fake.refundCalls != 0 {
		t.Fatal("unexpected upstream call")
	}
	f.supplier.Client.HTTP.Transport = rechargeTransport(f.fake.round)
	if e := f.reconcile(); e != nil {
		t.Fatal(e)
	}
	if f.fake.refundCalls != 1 || f.read(t)["platform_status"] != "requested" {
		t.Fatal("worker did not submit durable intent")
	}
}

func TestRechargeCustomerRefundRejectAndAmountMismatch(t *testing.T) {
	for _, status := range []string{"rejected", "cancelled", "succeeded"} {
		t.Run(status, func(t *testing.T) {
			f := newCustomerRefundFixture(t)
			f.fake.refundStatus = status
			object(t, request(f.h, "POST", f.path(), f.input, f.buyer), 201)
			if status == "succeeded" {
				// The receipt alone cannot settle money missing from the order.
				f.ledger(t, "approved", 0, 1)
				if e := f.reconcile(); e == nil {
					t.Fatal("unverified money accepted")
				}
			} else {
				f.ledger(t, "rejected", 0, 0)
			}
		})
	}
}

func TestRechargeCustomerRefundConcurrentReplayAndWorker(t *testing.T) {
	f := newCustomerRefundFixture(t)
	f.fake.refundStatus = "requested"
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan *httptest.ResponseRecorder, 1)
	f.supplier.Client.HTTP.Transport = rechargeTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/v1/orders/original/refunds" {
			close(entered)
			<-release
		}
		return f.fake.round(r)
	})
	go func() { done <- request(f.h, "POST", f.path(), f.input, f.buyer) }()
	<-entered
	// Release the original call even if an assertion fails.
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	object(t, request(f.h, "POST", f.path(), f.input, f.buyer), 200)
	worker := refunds.Service{Store: f.app, Supplier: f.supplier}
	if e := worker.Reconcile(context.Background()); e != nil {
		t.Fatal(e)
	}
	f.ledger(t, "approved", 0, 1)
	close(release)
	object(t, <-done, 201)
	if f.fake.refundCalls != 1 {
		t.Fatal("concurrent requests submitted multiple refunds")
	}
	// An unrelated wakeup must not immediately poll this refund again.
	f.supplier.Client.HTTP.Transport = rechargeTransport(func(*http.Request) (*http.Response, error) {
		t.Error("refund cooldown ignored")
		return nil, errors.New("unexpected query")
	})
	if e := worker.Reconcile(context.Background()); e != nil {
		t.Fatal(e)
	}
}

func TestRechargeCustomerRefundCompletedOrdersRequireLocalReview(t *testing.T) {
	for _, scenario := range []string{"completed", "earlier-success"} {
		t.Run(scenario, func(t *testing.T) {
			f := newCustomerRefundFixture(t)
			if scenario == "completed" {
				_, e := f.owner.DB.Exec(context.Background(), `UPDATE orders SET status='success' WHERE id=$1`, f.id)
				if e != nil {
					t.Fatal(e)
				}
			} else {
				_, e := f.owner.DB.Exec(context.Background(), `INSERT INTO recharge_redemptions(id,order_id,attempt_no,request_hash,credential_mode,status,created_at) VALUES($1,$2,1,'','session','succeeded',1)`, security.ID(), f.id)
				if e != nil {
					t.Fatal(e)
				}
			}
			f.fake.refundStatus, f.fake.refundAuto = "succeeded", true
			policy := object(t, request(f.h, "GET", f.path(), nil, f.buyer), 200)
			if policy["automaticPlatformRefund"] != false {
				t.Fatal("completed order advertised automatic submission", policy)
			}
			object(t, request(f.h, "POST", f.path(), f.input, f.buyer), 201)
			object(t, request(f.h, "POST", f.path(), f.input, f.buyer), 200)
			if e := f.reconcile(); e != nil {
				t.Fatal(e)
			}
			f.ledger(t, "pending", 0, 1)
			var count int
			if e := f.owner.DB.QueryRow(context.Background(), `SELECT count(*) FROM recharge_refunds WHERE order_id=$1`, f.id).Scan(&count); e != nil {
				t.Fatal(e)
			}
			if count != 0 || f.fake.refundCalls != 0 {
				t.Fatal("completed order created an upstream refund intent or request")
			}
			row := f.read(t)
			object(t, request(f.h, "POST", f.path(), map[string]any{"action": "cancel", "id": row["id"], "version": row["version"]}, f.buyer), 200)
			f.ledger(t, "cancelled", 0, 0)
		})
	}
}
