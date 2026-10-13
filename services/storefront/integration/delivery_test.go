package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"gptibo/storefront/internal/delivery"
	"gptibo/storefront/internal/httpapi"
	"gptibo/storefront/internal/mailer"
)

type frozenMail struct {
	bodies, keys []string
	fail         bool
}

func (m *frozenMail) Freeze(to string, value mailer.Message) (string, error) {
	b, e := json.Marshal(mailer.Frozen{From: "fixture@example.test", To: []string{to}, Message: value})
	return string(b), e
}
func (m *frozenMail) SendFrozen(_ context.Context, body, key string) (string, error) {
	m.bodies = append(m.bodies, body)
	m.keys = append(m.keys, key)
	if m.fail {
		return "", errors.New("ambiguous timeout")
	}
	return "mail-fixture", nil
}

func TestDeliveryFreezesRetryAndBlocksReissue(t *testing.T) {
	owner, app, a, base, loginMail := identityFixture(t)
	admin := login(t, base, loginMail, "admin", "")
	fake := &frozenMail{fail: true}
	h := httpapi.Router(app, "fixture", slog.New(slog.NewTextHandler(io.Discard, nil)), httpapi.Options{Origin: testOrigin, Auth: a, MailSender: fake})
	now := time.Now().UnixMilli()
	execSQL(t, owner, `INSERT INTO catalog_plans(id,product_id,name,price_cents) VALUES('duolingo-12m','duolingo','Duolingo',1000)`)
	execSQL(t, owner, `INSERT INTO orders(id,owner_hash,idempotency_key,plan,plan_name,price_cents,mode,payment_status,status,customer_email,created_at,updated_at,last_event_id) VALUES('YMX-EMAIL','owner','key','duolingo-12m','Duolingo',1000,'live','paid','session','buyer@example.test',$1,$1,'fixture')`, now)
	path := "/api/admin/orders/YMX-EMAIL/delivery"
	object(t, request(h, "POST", path, map[string]any{"action": "save", "version": -1, "orderVersion": 0, "value": "CDK-TEST", "instructions": "第一行\n第二行 __RECHARGE_DELIVERY_VALUE__"}, admin), 200)
	object(t, request(h, "POST", path, map[string]any{"action": "send", "version": 0, "orderVersion": 1}, admin), 503)
	if len(fake.bodies) != 1 || !strings.Contains(fake.bodies[0], "buyer@example.test") {
		t.Fatal("mail not frozen")
	}
	execSQL(t, owner, `UPDATE orders SET customer_email='changed@example.test' WHERE id='YMX-EMAIL'`)
	object(t, request(h, "POST", path, map[string]any{"action": "save", "version": 2, "orderVersion": 2, "value": "REPLACEMENT", "instructions": "cannot change"}, admin), 409)
	execSQL(t, owner, `UPDATE orders SET refund_hold=1 WHERE id='YMX-EMAIL'`)
	object(t, request(h, "POST", path, map[string]any{"action": "send", "version": 2, "orderVersion": 2}, admin), 409)
	execSQL(t, owner, `UPDATE orders SET refund_hold=0 WHERE id='YMX-EMAIL'`)
	fake.fail = false
	object(t, request(h, "POST", path, map[string]any{"action": "send", "version": 2, "orderVersion": 2}, admin), 200)
	if len(fake.bodies) != 2 || fake.bodies[0] != fake.bodies[1] || fake.keys[0] != fake.keys[1] {
		t.Fatal("retry changed body, recipient or idempotency")
	}
	object(t, request(h, "POST", path, map[string]any{"action": "send", "version": 2, "orderVersion": 2}, admin), 200)
	if len(fake.bodies) != 2 {
		t.Fatal("sent mail resent")
	}
	var status string
	if e := owner.DB.QueryRow(context.Background(), `SELECT status FROM orders WHERE id='YMX-EMAIL'`).Scan(&status); e != nil || status != "success" {
		t.Fatal("delivery not completed atomically", e)
	}
}

func TestRemindersFreezeAndCancelAfterRedemption(t *testing.T) {
	owner, app := fixture(t, true)
	fake := &frozenMail{fail: true}
	service := delivery.Service{Store: app, Sender: fake, Origin: testOrigin}
	now := time.Now().UnixMilli() - 1000000
	execSQL(t, owner, `INSERT INTO catalog_plans(id,product_id,name,price_cents) VALUES('plus','chatgpt','Plus',1000)`)
	for _, id := range []string{"RETRY", "STARTED", "EXPIRED"} {
		execSQL(t, owner, `INSERT INTO orders(id,owner_hash,idempotency_key,plan,plan_name,price_cents,mode,payment_status,status,customer_email,created_at,updated_at,last_event_id) VALUES($1,'owner',$1,'plus','Plus',1000,'live','unpaid','pending','buyer@example.test',$2,$2,'fixture')`, id, now)
		execSQL(t, owner, `UPDATE orders SET payment_status='paid',status='session',updated_at=$1 WHERE id=$2`, now, id)
	}
	execSQL(t, owner, `UPDATE orders SET status='processing',started_at=$1 WHERE id='STARTED'`, now)
	execSQL(t, owner, `UPDATE recharge_reminders SET first_attempt_at=$1 WHERE order_id='EXPIRED'`, now-24*3600000)
	out, e := service.Reminders(context.Background(), 20)
	if e != nil || out["failed"] != 1 || len(fake.bodies) != 1 {
		t.Fatal("invalid reminder eligibility", out, e)
	}
	execSQL(t, owner, `UPDATE recharge_reminders SET next_attempt_at=0 WHERE order_id='RETRY'; UPDATE orders SET customer_email='new@example.test' WHERE id='RETRY'`)
	fake.fail = false
	out, e = service.Reminders(context.Background(), 20)
	if e != nil || out["sent"] != 1 || len(fake.bodies) != 2 || fake.bodies[0] != fake.bodies[1] || fake.keys[0] != fake.keys[1] {
		t.Fatal("reminder changed retry", out, e)
	}
	out, e = service.Reminders(context.Background(), 20)
	if e != nil || out["sent"] != 0 || len(fake.bodies) != 2 {
		t.Fatal("duplicate reminder", e)
	}
}
