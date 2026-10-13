package integration_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"gptibo/storefront/internal/httpapi"
	"gptibo/storefront/internal/payments"
	"gptibo/storefront/internal/security"
)

func TestRefundApprovalAndPaidPartnerDebit(t *testing.T) {
	owner, _, _, h, mail := identityFixture(t)
	admin := login(t, h, mail, "admin", "")
	execSQL(t, owner, `INSERT INTO catalog_plans(id,product_id,name,price_cents) VALUES('plus','chatgpt','Plus',10000)`)
	partner := object(t, request(h, "POST", "/api/admin/partners", map[string]any{"email": "refund-partner@example.test"}, admin), 200)["id"].(string)
	now := time.Now().UnixMilli()
	buyer := &http.Cookie{Name: "__Host-sf_buyer", Value: security.Token()}
	execSQL(t, owner, `INSERT INTO partner_payouts(id,partner_id,amount_cents,status,recipient,created_at,request_key) VALUES('PAID-PAYOUT',$1,4000,'paid','fixture',$2,'fixture')`, partner, now)
	execSQL(t, owner, `INSERT INTO orders(id,owner_hash,idempotency_key,plan,plan_name,price_cents,cost_cents,partner_id,payout_id,mode,payment_status,status,created_at,updated_at,last_event_id) VALUES('YMX-REFUND',$1,'fixture','plus','Plus',10000,6000,$2,'PAID-PAYOUT','live','paid','success',$3,$3,'fixture')`, security.Hash(buyer.Value), partner, now)
	in := map[string]any{"key": security.ID(), "reason": "退款申请", "version": 0}
	object(t, request(h, "POST", "/api/orders/YMX-REFUND/refunds", in, buyer), 201)
	object(t, request(h, "POST", "/api/orders/YMX-REFUND/refunds", in, buyer), 200)
	object(t, request(h, "GET", "/api/orders/YMX-REFUND/refunds", nil, &http.Cookie{Name: buyer.Name, Value: security.Token()}), 404)
	cases := object(t, request(h, "GET", "/api/orders/YMX-REFUND/refunds", nil, buyer), 200)
	id := cases["cases"].([]any)[0].(map[string]any)["id"].(string)
	object(t, request(h, "GET", "/api/admin/refunds/"+id, nil, admin), 200)
	object(t, request(h, "POST", "/api/admin/refunds/"+id, map[string]any{"action": "complete", "version": 0, "note": "unapproved", "reference": "fixture"}, admin), 409)
	object(t, request(h, "POST", "/api/admin/refunds/"+id, map[string]any{"action": "approve", "version": 0, "note": "批准部分退款", "amountCents": 2500}, admin), 200)
	object(t, request(h, "POST", "/api/orders/YMX-REFUND/refunds", map[string]any{"action": "cancel", "id": id, "version": 1}, buyer), 409)
	complete := map[string]any{"action": "complete", "version": 1, "note": "原渠道已人工退回", "reference": "MANUAL-FIXTURE"}
	object(t, request(h, "POST", "/api/admin/refunds/"+id, complete, admin), 200)
	object(t, request(h, "POST", "/api/admin/refunds/"+id, complete, admin), 409)
	var refunded, hold, debit, count int64
	e := owner.DB.QueryRow(context.Background(), `SELECT refunded_cents,refund_hold FROM orders WHERE id='YMX-REFUND'`).Scan(&refunded, &hold)
	if e != nil {
		t.Fatal(e)
	}
	e = owner.DB.QueryRow(context.Background(), `SELECT count(*),COALESCE(sum(amount_cents),0)::bigint FROM partner_debits WHERE refund_id=$1`, id).Scan(&count, &debit)
	if e != nil || refunded != 2500 || hold != 0 || count != 1 || debit != 1000 {
		t.Fatal("refund or proportional debit inconsistent", e)
	}
	object(t, request(h, "GET", "/api/admin/refunds?status=refunded", nil, admin), 200)
}
func TestRefundUnknownProviderDoesNotReleaseHold(t *testing.T) {
	owner, app, a, base, mail := identityFixture(t)
	admin := login(t, base, mail, "admin", "")
	fake := &paymentFake{account: "original"}
	payment := payments.Service{Store: app, Provider: fake}
	h := httpapi.Router(app, "fixture", slog.New(slog.NewTextHandler(io.Discard, nil)), httpapi.Options{Origin: testOrigin, Auth: a, Payments: &payment})
	now := time.Now().UnixMilli()
	buyer := &http.Cookie{Name: "__Host-sf_buyer", Value: security.Token()}
	execSQL(t, owner, `INSERT INTO catalog_plans(id,product_id,name,price_cents) VALUES('plus','chatgpt','Plus',1000)`)
	execSQL(t, owner, `INSERT INTO orders(id,owner_hash,idempotency_key,plan,plan_name,price_cents,mode,payment_status,status,created_at,updated_at,last_event_id) VALUES('YMX-RETURN',$1,'fixture','plus','Plus',1000,'live','paid','session',$2,$2,'fixture')`, security.Hash(buyer.Value), now)
	execSQL(t, owner, `INSERT INTO payments(order_id,provider,account,amount_cents,state,trade_no,expires_at,created_at,updated_at) VALUES('YMX-RETURN','alipay','original',1000,'paid','TRADE-RETURN',$1,$1,$1)`, now)
	object(t, request(h, "POST", "/api/orders/YMX-RETURN/refunds", map[string]any{"key": security.ID(), "reason": "尚未交付", "version": 0}, buyer), 201)
	cases := object(t, request(h, "GET", "/api/orders/YMX-RETURN/refunds", nil, buyer), 200)
	id := cases["cases"].([]any)[0].(map[string]any)["id"].(string)
	object(t, request(h, "POST", "/api/admin/refunds/"+id, map[string]any{"action": "approve", "version": 0, "amountCents": 500, "note": "partial"}, admin), 400)
	object(t, request(h, "POST", "/api/admin/refunds/"+id, map[string]any{"action": "approve", "version": 0, "amountCents": 1000, "note": "full"}, admin), 200)
	in := map[string]any{"action": "execute-refund", "version": 1, "note": "原路退款"}
	object(t, request(h, "POST", "/api/admin/refunds/"+id, in, admin), 503)
	var hold, refunded int64
	_ = owner.DB.QueryRow(context.Background(), `SELECT refund_hold,refunded_cents FROM orders WHERE id='YMX-RETURN'`).Scan(&hold, &refunded)
	if hold != 1 || refunded != 0 {
		t.Fatal("unknown refund released funds")
	}
	fake.refund = payments.Refunded{Confirmed: true, TradeNo: "TRADE-RETURN"}
	object(t, request(h, "POST", "/api/admin/refunds/"+id, in, admin), 200)
	if fake.refunds != 1 {
		t.Fatal("recovery repeated confirmed refund")
	}
}
