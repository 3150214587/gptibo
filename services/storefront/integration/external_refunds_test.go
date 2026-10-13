package integration_test

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"

	"gptibo/storefront/internal/refunds"
	"gptibo/storefront/internal/security"
)

func externalRefund(t *testing.T, f customerRefundFixture, status, amount, margin string) refunds.Service {
	t.Helper()
	f.fake.order.Payment, f.fake.order.Refunded, f.fake.order.Margin = status, amount, margin
	if err := f.supplier.VerifyExternalRefund(context.Background(), f.id); err != nil {
		t.Fatal(err)
	}
	return refunds.Service{Store: f.app, Supplier: f.supplier}
}

func TestExternalRefundRecordsOnceWithoutIssuingMoney(t *testing.T) {
	f := newCustomerRefundFixture(t)
	service := externalRefund(t, f, "refunded", "100.00", "-60.00")
	f.supplier.Client.HTTP.Transport = rechargeTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" || r.URL.Path != "/v1/orders/original" {
			t.Errorf("unexpected money/recharge request: %s %s", r.Method, r.URL.Path)
			return nil, errors.New("unexpected request")
		}
		return f.fake.round(r)
	})
	// Before bookkeeping, customer sees the verified refund without admin-only IDs.
	view := object(t, request(f.h, "GET", "/api/orders/"+f.id, nil, f.buyer), 200)["order"].(map[string]any)
	if view["refundedCents"] != float64(10000) || view["refundHold"] != false || view["platformStatus"] != nil {
		t.Fatal("wrong public refund view", view)
	}
	before := object(t, request(f.h, "GET", f.path(), nil, f.buyer), 200)
	if before["refundableCents"] != float64(0) {
		t.Fatal("already returned money offered for refund", before)
	}
	for range 2 {
		if err := service.ReconcileExternal(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	f.ledger(t, "refunded", 10000, 0)
	var hold, events, intents int
	if err := f.owner.DB.QueryRow(context.Background(), `SELECT financial_hold,(SELECT count(*) FROM refund_events),(SELECT count(*) FROM recharge_refunds) FROM recharge_orders WHERE order_id=$1`, f.id).Scan(&hold, &events, &intents); err != nil || hold != 0 || events != 1 || intents != 0 {
		t.Fatal("incomplete or duplicate reconciliation", hold, events, intents, err)
	}
	summary, err := service.AdminNotifications(context.Background())
	if err != nil || summary["version"] != int64(0) || summary["pending"] != int64(0) {
		t.Fatal("repair must not announce a new customer request", summary, err)
	}
	if f.fake.refundCalls != 0 || f.fake.submitCalls != 0 {
		t.Fatal("reconciliation sent money or submitted recharge")
	}
	row := f.read(t)
	if row["status"] != "refunded" || row["approved_cents"] != float64(10000) {
		t.Fatal("customer history missing refund", row)
	}
}

func TestExternalRefundPartialThenFullAndClosedHistory(t *testing.T) {
	f := newCustomerRefundFixture(t)
	execSQL(t, f.owner, `INSERT INTO refund_cases(id,order_id,request_key,reason,status,requested_cents,created_at,updated_at,last_event_id) VALUES($1,$2,$1,'旧申请','cancelled',10000,1,1,$1)`, security.ID(), f.id)
	service := externalRefund(t, f, "partially_refunded", "30.00", "10.00")
	if err := service.ReconcileExternal(context.Background()); err != nil {
		t.Fatal(err)
	}
	service = externalRefund(t, f, "refunded", "100.00", "-60.00")
	for range 2 {
		if err := service.ReconcileExternal(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	var total, amount, cancelled, complete int64
	if err := f.owner.DB.QueryRow(context.Background(), `SELECT o.refunded_cents,COALESCE(sum(r.approved_cents) FILTER (WHERE r.status='refunded'),0)::bigint,count(*) FILTER (WHERE r.status='cancelled'),count(*) FILTER (WHERE r.status='refunded') FROM orders o JOIN refund_cases r ON r.order_id=o.id WHERE o.id=$1 GROUP BY o.id`, f.id).Scan(&total, &amount, &cancelled, &complete); err != nil || total != 10000 || amount != 10000 || cancelled != 1 || complete != 2 {
		t.Fatal("partial delta or history incorrect", total, amount, cancelled, complete, err)
	}
}

func TestExternalRefundGuardsAndRecovery(t *testing.T) {
	for _, scenario := range []string{"offline", "stale-paid", "wrong-order", "active-case", "pending-payout", "audit-failure"} {
		t.Run(scenario, func(t *testing.T) {
			f := newCustomerRefundFixture(t)
			service := externalRefund(t, f, "refunded", "100.00", "-60.00")
			switch scenario {
			case "offline":
				f.supplier.Client.HTTP.Transport = rechargeTransport(func(*http.Request) (*http.Response, error) { return nil, errors.New("offline") })
			case "stale-paid":
				f.fake.order.Payment, f.fake.order.Refunded, f.fake.order.Margin = "paid", "0.00", "40.00"
			case "wrong-order":
				f.fake.order.Merchant = "different-order"
			case "active-case":
				execSQL(t, f.owner, `INSERT INTO refund_cases(id,order_id,request_key,reason,status,requested_cents,created_at,updated_at,last_event_id) VALUES($1,$2,$1,'待审核','pending',10000,1,1,$1)`, security.ID(), f.id)
			case "pending-payout":
				execSQL(t, f.owner, `INSERT INTO partners(id,slug,name,active) VALUES('external-agent','EXT123','Agent',true); INSERT INTO partner_payouts(id,partner_id,amount_cents,status,recipient,created_at,request_key) VALUES('external-payout','external-agent',4000,'pending','fixture',1,'fixture')`)
				execSQL(t, f.owner, `UPDATE orders SET partner_id='external-agent',payout_id='external-payout',cost_cents=6000 WHERE id=$1`, f.id)
			case "audit-failure":
				execSQL(t, f.owner, `CREATE FUNCTION reject_external_event() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'audit unavailable'; END $$; CREATE TRIGGER reject_external_event BEFORE INSERT ON refund_events FOR EACH ROW EXECUTE FUNCTION reject_external_event()`)
			}
			_ = service.ReconcileExternal(context.Background())
			var amount, hold, count int64
			if err := f.owner.DB.QueryRow(context.Background(), `SELECT o.refunded_cents,t.financial_hold,(SELECT count(*) FROM refund_cases WHERE order_id=o.id AND status='refunded') FROM orders o JOIN recharge_orders t ON t.order_id=o.id WHERE o.id=$1`, f.id).Scan(&amount, &hold, &count); err != nil || amount != 0 || hold != 1 || count != 0 {
				t.Fatal("unsafe refund settled", amount, hold, count, err)
			}
			if scenario == "audit-failure" {
				execSQL(t, f.owner, `DROP TRIGGER reject_external_event ON refund_events`)
				if err := service.ReconcileExternal(context.Background()); err != nil {
					t.Fatal(err)
				}
				f.ledger(t, "refunded", 10000, 0)
			}
		})
	}
}

func TestExternalRefundPaidPartnerDebitAndConcurrency(t *testing.T) {
	f := newCustomerRefundFixture(t)
	execSQL(t, f.owner, `INSERT INTO partners(id,slug,name,active) VALUES('external-agent','EXT123','Agent',true); INSERT INTO partner_payouts(id,partner_id,amount_cents,status,recipient,created_at,request_key) VALUES('external-payout','external-agent',4000,'paid','fixture',1,'fixture')`)
	execSQL(t, f.owner, `UPDATE orders SET partner_id='external-agent',payout_id='external-payout',cost_cents=6000,status='success' WHERE id=$1`, f.id)
	service := externalRefund(t, f, "refunded", "100.00", "-60.00")
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() { _ = service.ReconcileExternal(context.Background()) })
	}
	wg.Wait()
	if err := service.ReconcileExternal(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.ledger(t, "refunded", 10000, 0)
	var debit, count int64
	if err := f.owner.DB.QueryRow(context.Background(), `SELECT COALESCE(sum(amount_cents),0)::bigint,count(*) FROM partner_debits`).Scan(&debit, &count); err != nil || debit != 4000 || count != 1 {
		t.Fatal("paid commission must be deducted exactly once", debit, count, err)
	}
}
