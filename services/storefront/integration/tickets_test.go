package integration_test

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"gptibo/storefront/internal/security"
)

func TestPartnerTicketsLifecycleIsolationAndNotifications(t *testing.T) {
	owner, _, _, h, mail := identityFixture(t)
	admin := login(t, h, mail, "admin", "")
	a := object(t, request(h, "POST", "/api/admin/partners", map[string]any{"email": "ticket-a@example.test"}, admin), 200)["id"].(string)
	b := object(t, request(h, "POST", "/api/admin/partners", map[string]any{"email": "ticket-b@example.test"}, admin), 200)["id"].(string)
	ca, cb := login(t, h, mail, "partner", "ticket-a@example.test"), login(t, h, mail, "partner", "ticket-b@example.test")
	execSQL(t, owner, `INSERT INTO catalog_plans(id,product_id,name,price_cents) VALUES('plus','chatgpt','Plus',16000)`)
	now := time.Now().UnixMilli()
	execSQL(t, owner, `INSERT INTO orders(id,owner_hash,idempotency_key,plan,plan_name,price_cents,cost_cents,partner_id,mode,payment_status,status,created_at,updated_at,last_event_id) VALUES('ticket-order-a','a','a','plus','Plus',16000,10000,$1,'live','paid','success',$3,$3,'fixture'),('ticket-order-b','b','b','plus','Plus',16000,10000,$2,'live','paid','success',$3,$3,'fixture')`, a, b, now)
	// Reserved route names must not become ticket IDs and shadow admin detail.
	for _, invalid := range []string{"notifications", "not-a-uuid", ""} {
		object(t, request(h, "POST", "/api/partner/tickets", map[string]any{"key": invalid, "orderId": "ticket-order-a", "subject": "问题", "body": "描述"}, ca), 400)
	}
	key := security.ID()
	in := map[string]any{"key": key, "orderId": "ticket-order-a", "subject": "充值问题", "body": "已经付款但未到账"}
	object(t, request(h, "POST", "/api/partner/tickets", in, nil), 401)
	object(t, request(h, "POST", "/api/partner/tickets", in, cb), 404)
	object(t, request(h, "POST", "/api/partner/tickets", in, admin), 401)
	for range 2 {
		object(t, request(h, "POST", "/api/partner/tickets", in, ca), 200)
	}
	in["body"] = "changed"
	object(t, request(h, "POST", "/api/partner/tickets", in, ca), 409)
	object(t, request(h, "GET", "/api/partner/tickets/"+key, nil, cb), 404)
	list := object(t, request(h, "GET", "/api/partner/tickets", nil, cb), 200)
	if list["total"] != float64(0) {
		t.Fatal("foreign tickets leaked")
	}
	list = object(t, request(h, "GET", "/api/admin/tickets?q=ticket-a%40example.test&status=open", nil, admin), 200)
	if list["total"] != float64(1) {
		t.Fatal("admin filters failed", list)
	}
	check := func(version, pending float64) {
		t.Helper()
		got := object(t, request(h, "GET", "/api/admin/tickets/notifications", nil, admin), 200)
		if got["version"] != version || got["pending"] != pending {
			t.Fatal("notification mismatch", got)
		}
	}
	check(1, 1)
	object(t, request(h, "GET", "/api/admin/tickets/notifications", nil, ca), 401)
	reply := map[string]any{"key": security.ID(), "body": "正在核对", "status": "processing", "version": 0}
	object(t, request(h, "POST", "/api/partner/tickets/"+key+"/replies", reply, ca), 400)
	reply["status"] = "open"
	object(t, request(h, "POST", "/api/partner/tickets/"+key+"/replies", reply, cb), 404)
	reply["status"] = "processing"
	object(t, request(h, "POST", "/api/admin/tickets/"+key+"/replies", reply, admin), 200)
	object(t, request(h, "POST", "/api/admin/tickets/"+key+"/replies", reply, admin), 200)
	check(1, 1)
	reply["key"] = security.ID()
	object(t, request(h, "POST", "/api/admin/tickets/"+key+"/replies", reply, admin), 409)
	reply["version"] = 1
	reply["status"] = "waiting"
	reply["body"] = "请提供页面错误提示"
	object(t, request(h, "POST", "/api/admin/tickets/"+key+"/replies", reply, admin), 200)
	check(1, 0)
	reply = map[string]any{"key": security.ID(), "body": "提示充值失败", "status": "open", "version": 2}
	object(t, request(h, "POST", "/api/partner/tickets/"+key+"/replies", reply, ca), 200)
	object(t, request(h, "POST", "/api/partner/tickets/"+key+"/replies", reply, ca), 200)
	check(2, 1)
	reply = map[string]any{"key": security.ID(), "body": "已处理完成", "status": "resolved", "version": 3}
	object(t, request(h, "POST", "/api/admin/tickets/"+key+"/replies", reply, admin), 200)
	check(2, 0)
	reply = map[string]any{"key": security.ID(), "body": "仍有问题", "status": "open", "version": 4}
	object(t, request(h, "POST", "/api/partner/tickets/"+key+"/replies", reply, ca), 200)
	check(3, 1)
	detail := object(t, request(h, "GET", "/api/partner/tickets/"+key, nil, ca), 200)
	if detail["status"] != "open" || detail["version"] != float64(5) || len(detail["events"].([]any)) != 6 {
		t.Fatal("history or reopen failed", detail)
	}
	// State handling is informational and must never alter money or fulfillment.
	var status string
	var refunded int64
	if e := owner.DB.QueryRow(context.Background(), `SELECT status,refunded_cents FROM orders WHERE id='ticket-order-a'`).Scan(&status, &refunded); e != nil || status != "success" || refunded != 0 {
		t.Fatal("ticket mutated order", e)
	}
	for _, body := range []string{" ", strings.Repeat("问", 5001)} {
		in["key"] = security.ID()
		in["body"] = body
		object(t, request(h, "POST", "/api/partner/tickets", in, ca), 400)
	}
	object(t, request(h, "GET", "/api/admin/tickets?status=invalid", nil, admin), 400)
	execSQL(t, owner, `UPDATE partners SET active=false WHERE id=$1`, a)
	object(t, request(h, "GET", "/api/partner/tickets", nil, ca), 401)
	object(t, request(h, "POST", "/api/partner/tickets/"+key+"/replies", map[string]any{"key": security.ID(), "version": 5, "status": "open", "body": "disabled"}, ca), 401)
}

func TestPartnerTicketConcurrentCreationAndReply(t *testing.T) {
	owner, _, _, h, mail := identityFixture(t)
	admin := login(t, h, mail, "admin", "")
	id := object(t, request(h, "POST", "/api/admin/partners", map[string]any{"email": "ticket-race@example.test"}, admin), 200)["id"].(string)
	partner := login(t, h, mail, "partner", "ticket-race@example.test")
	execSQL(t, owner, `INSERT INTO catalog_plans(id,product_id,name,price_cents) VALUES('plus','chatgpt','Plus',16000)`)
	execSQL(t, owner, `INSERT INTO orders(id,owner_hash,idempotency_key,plan,plan_name,price_cents,cost_cents,partner_id,mode,payment_status,status,created_at,updated_at,last_event_id) VALUES('ticket-race','a','a','plus','Plus',16000,10000,$1,'live','paid','success',1,1,'fixture')`, id)
	key := security.ID()
	in := map[string]any{"key": key, "orderId": "ticket-race", "subject": "问题", "body": "需要处理"}
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for range 2 {
		wg.Go(func() { codes <- request(h, "POST", "/api/partner/tickets", in, partner).Code })
	}
	wg.Wait()
	close(codes)
	successes := 0
	for code := range codes {
		if code == 200 {
			successes++
		} else if code != 409 {
			t.Fatal("unexpected create response", code)
		}
	}
	if successes < 1 {
		t.Fatal("no create committed")
	}
	object(t, request(h, "POST", "/api/partner/tickets", in, partner), 200)
	replies := []struct {
		role   string
		cookie *http.Cookie
		status string
	}{{"admin", admin, "resolved"}, {"partner", partner, "open"}}
	codes = make(chan int, 2)
	for _, actor := range replies {
		wg.Go(func() {
			codes <- request(h, "POST", "/api/"+actor.role+"/tickets/"+key+"/replies", map[string]any{"key": security.ID(), "body": "并发回复", "status": actor.status, "version": 0}, actor.cookie).Code
		})
	}
	wg.Wait()
	close(codes)
	successes = 0
	conflicts := 0
	for code := range codes {
		if code == 200 {
			successes++
		} else if code == 409 {
			conflicts++
		} else {
			t.Fatal("unexpected reply response", code)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatal("lost update protection failed")
	}
	detail := object(t, request(h, "GET", "/api/admin/tickets/"+key, nil, admin), 200)
	if detail["version"] != float64(1) || len(detail["events"].([]any)) != 2 {
		t.Fatal("duplicate history", detail)
	}
}
