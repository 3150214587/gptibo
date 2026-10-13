package integration_test

import (
	"context"
	"net/http"
	"testing"

	"gptibo/storefront/internal/security"
)

func TestRefundNotificationsNewRequestsRetriesAndStatusChanges(t *testing.T) {
	owner, _, _, h, mail := identityFixture(t)
	admin := login(t, h, mail, "admin", "")
	object(t, request(h, "POST", "/api/admin/partners", map[string]any{"email": "refund-alert@example.test"}, admin), 200)
	partner := login(t, h, mail, "partner", "refund-alert@example.test")
	buyer := &http.Cookie{Name: "__Host-sf_buyer", Value: security.Token()}
	path := "/api/admin/refunds/notifications"
	for _, cookie := range []*http.Cookie{nil, buyer, partner} {
		object(t, request(h, "GET", path, nil, cookie), 401)
	}
	check := func(version, pending float64) {
		t.Helper()
		got := object(t, request(h, "GET", path, nil, admin), 200)
		if got["version"] != version || got["pending"] != pending {
			t.Fatal("notification mismatch", got)
		}
	}
	check(0, 0)
	execSQL(t, owner, `INSERT INTO catalog_plans(id,product_id,name,price_cents) VALUES('plus','chatgpt','Plus',1000)`)
	execSQL(t, owner, `INSERT INTO orders(id,owner_hash,idempotency_key,plan,plan_name,price_cents,mode,payment_status,status,created_at,updated_at,last_event_id) VALUES('refund-notify',$1,'refund-notify','plus','Plus',1000,'live','paid','success',1,1,'fixture')`, security.Hash(buyer.Value))
	input := map[string]any{"key": security.ID(), "reason": "申请退款", "version": 0}
	object(t, request(h, "POST", "/api/orders/refund-notify/refunds", input, buyer), 201)
	check(1, 1)
	object(t, request(h, "POST", "/api/orders/refund-notify/refunds", input, buyer), 200)
	check(1, 1)
	var id string
	if err := owner.DB.QueryRow(context.Background(), `SELECT id FROM refund_cases WHERE order_id='refund-notify'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	object(t, request(h, "GET", "/api/admin/refunds/"+id, nil, admin), 200)
	object(t, request(h, "POST", "/api/orders/refund-notify/refunds", map[string]any{"action": "cancel", "id": id, "version": 0}, buyer), 200)
	check(1, 0)
	input["key"], input["version"] = security.ID(), 2
	object(t, request(h, "POST", "/api/orders/refund-notify/refunds", input, buyer), 201)
	// The pending count is back to one, but a second application must notify.
	check(2, 1)
	if err := owner.DB.QueryRow(context.Background(), `SELECT id FROM refund_cases WHERE order_id='refund-notify' AND status='pending'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	object(t, request(h, "POST", "/api/admin/refunds/"+id, map[string]any{"action": "approve", "version": 0, "amountCents": 1000, "note": "确认处理"}, admin), 200)
	check(2, 1)
	// Terminal states affect the badge only, never generate a new notification.
	execSQL(t, owner, `UPDATE refund_cases SET status='refunded' WHERE id=$1`, id)
	check(2, 0)
}
