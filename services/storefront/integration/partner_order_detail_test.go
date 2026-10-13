package integration_test

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestPartnerOrderSearchDetailIsolation(t *testing.T) {
	owner, _, _, h, mail := identityFixture(t)
	execSQL(t, owner, `INSERT INTO partners(id,slug,name,email,active) VALUES('a','AAAAAA','A','a@example.test',true),('b','BBBBBB','B','b@example.test',true)`)
	a := login(t, h, mail, "partner", "a@example.test")
	b := login(t, h, mail, "partner", "b@example.test")
	admin := login(t, h, mail, "admin", "")
	execSQL(t, owner, `INSERT INTO catalog_plans(id,product_id,name,price_cents,enabled) VALUES('plus','chatgpt','Plus',10000,true)`)
	now := time.Now().UnixMilli()
	execSQL(t, owner, `INSERT INTO orders(id,owner_hash,idempotency_key,plan,plan_name,customer_email,price_cents,cost_cents,partner_id,mode,payment_status,status,created_at,updated_at,last_event_id) SELECT 'ORDER-'||i,'owner','key-'||i,'plus','Plus','buyer@example.test',10000,6000,'a','live','paid','failed',$1::bigint+i,$1::bigint+i,'event-'||i FROM generate_series(1,23) i`, now)
	execSQL(t, owner, `INSERT INTO orders(id,owner_hash,idempotency_key,plan,plan_name,customer_email,price_cents,cost_cents,partner_id,mode,payment_status,status,created_at,updated_at,last_event_id) VALUES('OTHER','private-owner','other-key','plus','Plus','buyer@example.test',10000,6000,'b','live','paid','failed',$1,$1,'other-event')`, now)
	// Deliberately insert out of sequence; tied timestamps must retain event order.
	execSQL(t, owner, `INSERT INTO order_events(id,order_id,actor,action,to_status,note,order_version,created_at) VALUES
	 ('history-failed','ORDER-1','system','recharge-status','failed','充值前校验未通过，请核对账号。',4,$1),
	 ('history-created','ORDER-1','customer','create','pending','创建订单，等待付款',0,$1),
	 ('history-running','ORDER-1','system','recharge-status','processing','充值任务已受理，等待完成',3,$1),
	 ('history-submit','ORDER-1','customer','recharge-submit','session','客户已授权提交充值资料，等待充值服务确认受理',2,$1),
	 ('history-paid','ORDER-1','payment','payment-confirmed','session','支付渠道已核验收款，等待提交充值资料',1,$1),
	 ('history-private','ORDER-1','admin:private-admin@example.test','note','failed','PRIVATE_INTERNAL_NOTE',5,$1),
	 ('history-resend','ORDER-1','admin:private-admin@example.test','recharge-resend','session','管理员已补发',6,$1),
	 ('history-other','OTHER','system','recharge-status','failed','OTHER_PARTNER_HISTORY',0,$1)`, now)
	for _, q := range []string{"buyer@example.test", " BUYER@EXAMPLE.TEST ", "plus"} {
		result := object(t, request(h, "GET", "/api/partner/orders?q="+url.QueryEscape(q), nil, a), 200)
		if result["total"] != float64(23) || len(result["rows"].([]any)) != 20 {
			t.Fatal("search/pagination incorrect", result)
		}
	}
	for _, filter := range []string{"q=OTHER", "q=%25", "q=buyer&status=success"} {
		r := object(t, request(h, "GET", "/api/partner/orders?"+filter, nil, a), 200)
		if r["total"] != float64(0) {
			t.Fatal("filter isolation failed", r)
		}
	}
	page := object(t, request(h, "GET", "/api/partner/orders?q=buyer&page=2", nil, a), 200)
	if len(page["rows"].([]any)) != 3 {
		t.Fatal("wrong second page")
	}
	object(t, request(h, "GET", "/api/partner/orders?status=invalid", nil, a), 400)
	object(t, request(h, "GET", "/api/partner/orders?q="+strings.Repeat("x", 321), nil, a), 400)
	object(t, request(h, "GET", "/api/partner/orders/ORDER-1", nil, nil), 401)
	object(t, request(h, "GET", "/api/partner/orders/ORDER-1", nil, b), 404)
	object(t, request(h, "GET", "/api/partner/orders/OTHER", nil, a), 404)
	object(t, request(h, "GET", "/api/admin/partners/a/orders/OTHER", nil, admin), 404)
	response := object(t, request(h, "GET", "/api/partner/orders/ORDER-1", nil, a), 200)
	detail := response["order"].(map[string]any)
	if detail["status"] != "failed" || detail["profit_cents"] == nil {
		t.Fatal("missing order state", detail)
	}
	events := response["events"].([]any)
	expected := []string{"history-created", "history-paid", "history-submit", "history-running", "history-failed", "history-resend"}
	if len(events) != len(expected) {
		t.Fatal("missing processing history", events)
	}
	for i, id := range expected {
		event := events[i].(map[string]any)
		if event["id"] != id || event["created_at"] != float64(now) || event["note"] == "" || len(event) != 6 {
			t.Fatal("incorrect event ordering or public fields", event)
		}
	}
	if events[1].(map[string]any)["actor"] != "system" || events[5].(map[string]any)["actor"] != "admin" || events[4].(map[string]any)["to_status"] != "failed" {
		t.Fatal("incorrect historical actor or status", events)
	}
	raw, _ := json.Marshal(response)
	for _, secret := range []string{"owner_hash", "idempotency_key", "request_hash", "credential", "private-owner", "other-key", "PRIVATE_INTERNAL_NOTE", "OTHER_PARTNER_HISTORY", "private-admin@example.test"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("private field exposed", secret)
		}
	}
	preview := object(t, request(h, "GET", "/api/admin/partners/a/orders/ORDER-1", nil, admin), 200)
	previewRaw, _ := json.Marshal(preview)
	if string(previewRaw) != string(raw) {
		t.Fatal("admin partner preview differs from partner history")
	}
	empty := object(t, request(h, "GET", "/api/partner/orders/ORDER-2", nil, a), 200)
	if history, ok := empty["events"].([]any); !ok || len(history) != 0 {
		t.Fatal("missing empty history array", empty["events"])
	}
	var n int
	if err := owner.DB.QueryRow(context.Background(), `SELECT count(*) FROM orders`).Scan(&n); err != nil || n != 24 {
		t.Fatal("read changed orders", err)
	}
}
