package integration_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"gptibo/storefront/internal/security"
)

// Exercise the public refund/payout APIs with a delivered Duolingo order.
// No payment, supplier purchase, or transfer is sent to an external service.
func TestDuolingoWarrantyRefundDeductsFuturePayout(t *testing.T) {
	owner, _, _, h, mail := identityFixture(t)
	admin := login(t, h, mail, "admin", "")
	execSQL(t, owner, `INSERT INTO catalog_plans(id,product_id,name,price_cents,default_cost_cents) VALUES('duolingo-12m','duolingo','多邻国一年',10000,5999)`)
	partnerID := object(t, request(h, "POST", "/api/admin/partners", map[string]any{"email": "warranty@example.test"}, admin), 200)["id"].(string)
	partner := login(t, h, mail, "partner", "warranty@example.test")
	object(t, request(h, "POST", "/api/partner/payout-account", map[string]any{"phone": "13800138000", "name": "测试收款人", "version": 0}, partner), 200)
	buyer := &http.Cookie{Name: "__Host-sf_buyer", Value: security.Token()}
	now := time.Now().UnixMilli()
	seed := func(id string) {
		execSQL(t, owner, `INSERT INTO orders(id,owner_hash,idempotency_key,plan,plan_name,price_cents,cost_cents,partner_id,mode,payment_status,status,created_at,updated_at,last_event_id) VALUES($1,$2,$1,'duolingo-12m','多邻国一年',10000,5999,$3,'live','paid','success',$4,$4,'fixture')`, id, security.Hash(buyer.Value), partnerID, now)
	}
	seed("WARRANTY-ORDER")
	payout := func() string {
		in := map[string]any{"key": security.ID(), "accountVersion": 1}
		object(t, request(h, "POST", "/api/partner/payouts", in, partner), 201)
		object(t, request(h, "POST", "/api/partner/payouts", in, partner), 200)
		var id string
		if e := owner.DB.QueryRow(context.Background(), `SELECT id FROM partner_payouts WHERE request_key=$1`, in["key"]).Scan(&id); e != nil {
			t.Fatal(e)
		}
		return id
	}
	handle := func(id, action string, version int) {
		object(t, request(h, "POST", "/api/admin/partner-payouts/"+id, map[string]any{"action": action, "version": version, "note": "fixture", "reference": "fixture-transfer"}, admin), 200)
	}
	reset := func() {
		object(t, request(h, "POST", "/api/admin/partners/"+partnerID+"/payout-cooldown/reset", map[string]any{"key": security.ID(), "note": "fixture"}, admin), 200)
	}
	stats := func(debt, available float64, held bool) {
		st := object(t, request(h, "GET", "/api/partner/dashboard", nil, partner), 200)["stats"].(map[string]any)
		if st["debt"] != debt || st["available"] != available || st["withdrawalHeld"] != held {
			t.Fatalf("unexpected earnings: %+v", st)
		}
	}
	original := payout()
	handle(original, "approve", 0)
	handle(original, "paid", 1)
	reset()
	seed("NEXT-ORDER-A")
	var lastCase, firstCase string
	for i, amount := range []int{3333, 3333, 3334} {
		in := map[string]any{"key": security.ID(), "reason": "多邻国质保退款", "version": i * 2}
		object(t, request(h, "POST", "/api/orders/WARRANTY-ORDER/refunds", in, buyer), 201)
		object(t, request(h, "POST", "/api/orders/WARRANTY-ORDER/refunds", in, buyer), 200)
		if e := owner.DB.QueryRow(context.Background(), `SELECT id FROM refund_cases WHERE request_key=$1`, in["key"]).Scan(&lastCase); e != nil {
			t.Fatal(e)
		}
		object(t, request(h, "POST", "/api/partner/payouts", map[string]any{"key": security.ID(), "accountVersion": 1}, partner), 409)
		object(t, request(h, "POST", "/api/admin/refunds/"+lastCase, map[string]any{"action": "approve", "version": 0, "amountCents": amount, "note": "质保退款"}, admin), 200)
		complete := map[string]any{"action": "complete", "version": 1, "note": "已核对退款", "reference": "fixture-refund"}
		object(t, request(h, "POST", "/api/admin/refunds/"+lastCase, complete, admin), 200)
		object(t, request(h, "POST", "/api/admin/refunds/"+lastCase, complete, admin), 409)
		if i == 0 {
			firstCase = lastCase
		}
		expected := float64(4001)
		stats(expected, 4001-expected, false)
	}
	var count, total int64
	if e := owner.DB.QueryRow(context.Background(), `SELECT count(*),sum(amount_cents)::bigint FROM partner_debits WHERE partner_id=$1`, partnerID).Scan(&count, &total); e != nil || count != 1 || total != 4001 {
		t.Fatalf("incorrect cumulative debit %d/%d: %v", count, total, e)
	}
	debitState := func(status any) {
		detail := object(t, request(h, "GET", "/api/admin/refunds/"+firstCase, nil, admin), 200)
		debit := detail["partnerDebit"].(map[string]any)
		if detail["hasPartner"] != true || debit["amount_cents"] != float64(4001) || debit["payout_status"] != status {
			t.Fatalf("incorrect refund deduction detail: %+v", detail)
		}
		customer := object(t, request(h, "GET", "/api/orders/WARRANTY-ORDER/refunds", nil, buyer), 200)
		if _, exists := customer["partnerDebit"]; exists {
			t.Fatal("customer received private commission detail")
		}
	}
	debitState(nil)
	object(t, request(h, "POST", "/api/partner/payouts", map[string]any{"key": security.ID(), "accountVersion": 1}, partner), 409)
	stats(4001, 0, false)
	seed("NEXT-ORDER-B")
	next := payout()
	detail := object(t, request(h, "GET", "/api/admin/partner-payouts/"+next, nil, admin), 200)
	p := detail["payout"].(map[string]any)
	if p["gross_cents"] != float64(8002) || p["deduction_cents"] != float64(4001) || p["amount_cents"] != float64(4001) || len(detail["deductions"].([]any)) != 1 {
		t.Fatalf("wrong net payout: %+v", detail)
	}
	debitState("pending")
	handle(next, "rejected", 0)
	stats(4001, 4001, false)
	debitState(nil)
	history := object(t, request(h, "GET", "/api/admin/partner-payouts/"+next, nil, admin), 200)
	if len(history["deductions"].([]any)) != 1 {
		t.Fatal("rejection erased deduction history")
	}
	reset()
	next = payout()
	handle(next, "approve", 0)
	handle(next, "paid", 1)
	stats(0, 0, false)
	debitState("paid")
	reset()
	seed("NEXT-ORDER-C")
	next = payout()
	detail = object(t, request(h, "GET", "/api/admin/partner-payouts/"+next, nil, admin), 200)
	p = detail["payout"].(map[string]any)
	if p["amount_cents"] != float64(4001) || p["deduction_cents"] != float64(0) {
		t.Fatal("refund was deducted twice")
	}
}

func TestDuolingoWarrantyUnpaidCommissionAndCancelledRefund(t *testing.T) {
	for _, action := range []string{"complete", "cancel", "reject"} {
		t.Run(action, func(t *testing.T) {
			owner, _, _, h, mail := identityFixture(t)
			admin := login(t, h, mail, "admin", "")
			execSQL(t, owner, `INSERT INTO catalog_plans(id,product_id,name,price_cents,default_cost_cents) VALUES('duolingo-12m','duolingo','多邻国一年',10000,6000)`)
			partnerID := object(t, request(h, "POST", "/api/admin/partners", map[string]any{"email": "unpaid@example.test"}, admin), 200)["id"].(string)
			partner := login(t, h, mail, "partner", "unpaid@example.test")
			object(t, request(h, "POST", "/api/partner/payout-account", map[string]any{"phone": "13800138000", "name": "测试收款人", "version": 0}, partner), 200)
			buyer := &http.Cookie{Name: "__Host-sf_buyer", Value: security.Token()}
			now := time.Now().UnixMilli()
			execSQL(t, owner, `INSERT INTO orders(id,owner_hash,idempotency_key,plan,plan_name,price_cents,cost_cents,partner_id,mode,payment_status,status,created_at,updated_at,last_event_id) VALUES('WARRANTY-UNPAID',$1,'fixture','duolingo-12m','多邻国一年',10000,6000,$2,'live','paid','success',$3,$3,'fixture')`, security.Hash(buyer.Value), partnerID, now)
			object(t, request(h, "POST", "/api/partner/payouts", map[string]any{"key": security.ID(), "accountVersion": 1}, partner), 201)
			object(t, request(h, "POST", "/api/orders/WARRANTY-UNPAID/refunds", map[string]any{"key": security.ID(), "reason": "质保退款", "version": 0}, buyer), 201)
			var id, payoutStatus string
			if e := owner.DB.QueryRow(context.Background(), `SELECT id FROM refund_cases WHERE order_id='WARRANTY-UNPAID'`).Scan(&id); e != nil {
				t.Fatal(e)
			}
			if e := owner.DB.QueryRow(context.Background(), `SELECT status FROM partner_payouts WHERE partner_id=$1`, partnerID).Scan(&payoutStatus); e != nil || payoutStatus != "rejected" {
				t.Fatal("unreviewed payout was not released", e)
			}
			available := float64(4000)
			switch action {
			case "complete":
				object(t, request(h, "POST", "/api/admin/refunds/"+id, map[string]any{"action": "approve", "version": 0, "amountCents": 1000, "note": "部分质保退款也扣回整单收益"}, admin), 200)
				object(t, request(h, "POST", "/api/admin/refunds/"+id, map[string]any{"action": "complete", "version": 1, "reference": "fixture", "note": "已退款"}, admin), 200)
				available = 0
			case "cancel":
				object(t, request(h, "POST", "/api/orders/WARRANTY-UNPAID/refunds", map[string]any{"action": "cancel", "id": id, "version": 0}, buyer), 200)
			case "reject":
				object(t, request(h, "POST", "/api/admin/refunds/"+id, map[string]any{"action": "reject", "version": 0, "note": "不符合质保"}, admin), 200)
			}
			st := object(t, request(h, "GET", "/api/partner/dashboard", nil, partner), 200)["stats"].(map[string]any)
			if st["debt"] != float64(0) || st["available"] != available || st["withdrawalHeld"] != false {
				t.Fatalf("wrong restored earnings: %+v", st)
			}
			var count int
			if e := owner.DB.QueryRow(context.Background(), `SELECT count(*) FROM partner_debits WHERE partner_id=$1`, partnerID).Scan(&count); e != nil || count != 0 {
				t.Fatal("unpaid commission incorrectly created debt", e)
			}
		})
	}
}
