package integration_test

import (
	"context"
	"gptibo/storefront/internal/security"
	"testing"
	"time"
)

func TestPayoutReservationReviewAndCooldownIdempotency(t *testing.T) {
	owner, _, _, h, mail := identityFixture(t)
	admin := login(t, h, mail, "admin", "")
	execSQL(t, owner, `INSERT INTO catalog_plans(id,product_id,name,price_cents,default_cost_cents) VALUES('plus','chatgpt','Plus',10000,6000)`)
	partnerID := object(t, request(h, "POST", "/api/admin/partners", map[string]any{"email": "payout@example.test"}, admin), 200)["id"].(string)
	partner := login(t, h, mail, "partner", "payout@example.test")
	now := time.Now().UnixMilli()
	seed := func(id string) {
		execSQL(t, owner, `INSERT INTO orders(id,owner_hash,idempotency_key,plan,plan_name,price_cents,cost_cents,partner_id,mode,payment_status,status,created_at,updated_at,last_event_id) VALUES($1,'fixture',$1,'plus','Plus',10000,6000,$2,'live','paid','success',$3,$3,'fixture')`, id, partnerID, now)
	}
	seed("PAYOUT-ORDER-A")
	object(t, request(h, "POST", "/api/partner/payout-account", map[string]any{"phone": "13800138000", "name": "测试收款人", "version": 0}, partner), 200)
	requestPayout := map[string]any{"key": security.ID(), "accountVersion": 1}
	object(t, request(h, "POST", "/api/partner/payouts", requestPayout, partner), 201)
	object(t, request(h, "POST", "/api/partner/payouts", requestPayout, partner), 200)
	list := object(t, request(h, "GET", "/api/admin/partner-payouts", nil, admin), 200)
	payout := list["rows"].([]any)[0].(map[string]any)
	id := payout["id"].(string)
	if payout["amount_cents"] != float64(4000) {
		t.Fatal("wrong reserved earnings")
	}
	object(t, request(h, "GET", "/api/admin/partner-payouts/"+id, nil, admin), 200)
	object(t, request(h, "POST", "/api/admin/partner-payouts/"+id, map[string]any{"action": "paid", "version": 0, "note": "transfer", "reference": "fixture"}, admin), 409)
	object(t, request(h, "POST", "/api/admin/partner-payouts/"+id, map[string]any{"action": "approve", "version": 0, "note": "review"}, admin), 200)
	object(t, request(h, "POST", "/api/admin/partner-payouts/"+id, map[string]any{"action": "paid", "version": 1, "note": "recorded transfer", "reference": "fixture"}, admin), 200)
	seed("PAYOUT-ORDER-B")
	requestPayout["key"] = security.ID()
	object(t, request(h, "POST", "/api/partner/payouts", requestPayout, partner), 409)
	reset := map[string]any{"key": security.ID(), "note": "fixture reset"}
	path := "/api/admin/partners/" + partnerID + "/payout-cooldown/reset"
	out := object(t, request(h, "POST", path, reset, admin), 200)
	if out["resetPayouts"] != float64(1) {
		t.Fatal("wrong reset receipt")
	}
	object(t, request(h, "POST", "/api/partner/payouts", requestPayout, partner), 201)
	object(t, request(h, "POST", path, reset, admin), 200)
	var unexempt int
	_ = owner.DB.QueryRow(context.Background(), `SELECT count(*) FROM partner_payouts WHERE partner_id=$1 AND cooldown_exempt=0`, partnerID).Scan(&unexempt)
	if unexempt != 1 {
		t.Fatal("reset replay affected future payout")
	}
	var queued int
	_ = owner.DB.QueryRow(context.Background(), `SELECT count(*) FROM payout_notifications`).Scan(&queued)
	if queued != 2 {
		t.Fatal("notification queue not atomic/idempotent")
	}
}
func TestAccountingSnapshotsAndLaterRefundAdjustments(t *testing.T) {
	owner, _, _, h, mail := identityFixture(t)
	admin := login(t, h, mail, "admin", "")
	execSQL(t, owner, `INSERT INTO catalog_plans(id,product_id,name,price_cents,default_cost_cents) VALUES('plus','chatgpt','Plus',10000,6000)`)
	partner := object(t, request(h, "POST", "/api/admin/partners", map[string]any{"email": "accounting@example.test"}, admin), 200)["id"].(string)
	now := time.Now().UnixMilli()
	execSQL(t, owner, `INSERT INTO orders(id,owner_hash,idempotency_key,plan,plan_name,price_cents,cost_cents,partner_id,mode,payment_status,status,created_at,updated_at,last_event_id) VALUES('ACCOUNT-ORDER','fixture','fixture','plus','Plus',10000,6000,$1,'live','paid','success',$2,$2,'fixture')`, partner, now)
	preview := object(t, request(h, "GET", "/api/admin/accounting", nil, admin), 200)
	if preview["totals"].(map[string]any)["missing_cost_count"] != float64(1) {
		t.Fatal("missing procurement cost silently defaulted")
	}
	close := map[string]any{"key": security.ID(), "version": 0}
	object(t, request(h, "POST", "/api/admin/accounting/close", close, admin), 409)
	object(t, request(h, "POST", "/api/admin/accounting/costs", map[string]any{"version": 0, "costs": []any{map[string]any{"plan": "plus", "cost_cents": 3000}}}, admin), 200)
	close["version"] = 1
	settlement := object(t, request(h, "POST", "/api/admin/accounting/close", close, admin), 201)["settlement"].(map[string]any)
	if settlement["profit_cents"] != float64(7000) {
		t.Fatal("close snapshot formula differs from legacy")
	}
	object(t, request(h, "POST", "/api/admin/accounting/close", close, admin), 200)
	report := object(t, request(h, "GET", "/api/admin/accounting/report", nil, admin), 200)["totals"].(map[string]any)
	if report["commission_cents"] != float64(4000) || report["cost_cents"] != float64(3000) || report["profit_cents"] != float64(3000) {
		t.Fatal("report mixed procurement with partner price")
	}
	object(t, request(h, "POST", "/api/admin/accounting/costs", map[string]any{"version": 2, "costs": []any{map[string]any{"plan": "plus", "cost_cents": 3500}}}, admin), 200)
	execSQL(t, owner, `UPDATE orders SET refunded_cents=2500 WHERE id='ACCOUNT-ORDER'`)
	second := map[string]any{"key": security.ID(), "version": 3}
	adjust := object(t, request(h, "POST", "/api/admin/accounting/close", second, admin), 201)["settlement"].(map[string]any)
	if adjust["order_count"] != float64(0) || adjust["adjustment_count"] != float64(1) || adjust["refund_cents"] != float64(2500) || adjust["profit_cents"] != float64(-2500) {
		t.Fatal("later refund not recorded as adjustment")
	}
	object(t, request(h, "GET", "/api/admin/accounting/"+close["key"].(string), nil, admin), 200)
	report = object(t, request(h, "GET", "/api/admin/accounting/report?status=refunded", nil, admin), 200)["totals"].(map[string]any)
	if report["cost_cents"] != float64(3000) || report["commission_cents"] != float64(3000) || report["profit_cents"] != float64(1500) {
		t.Fatal("immutable procurement snapshot or partial commission wrong")
	}
	execSQL(t, owner, `UPDATE orders SET refund_hold=1 WHERE id='ACCOUNT-ORDER'`)
	report = object(t, request(h, "GET", "/api/admin/accounting/report", nil, admin), 200)["totals"].(map[string]any)
	if report["profit_cents"] != nil || report["held_count"] != float64(1) {
		t.Fatal("held profit presented as confirmed")
	}
}
