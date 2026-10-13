package integration_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"gptibo/storefront/internal/delivery"
	"gptibo/storefront/internal/mailer"
	"gptibo/storefront/internal/store"
)

func TestPartnerNotificationSettingsIsolation(t *testing.T) {
	owner, _, _, h, mail := identityFixture(t)
	execSQL(t, owner, `INSERT INTO partners(id,slug,name,email,active) VALUES('a','AAAAAA','A','a@example.test',true),('b','BBBBBB','B','b@example.test',true)`)
	a := login(t, h, mail, "partner", "a@example.test")
	b := login(t, h, mail, "partner", "b@example.test")
	path := "/api/partner/notification-settings"
	initial := object(t, request(h, "GET", path, nil, a), 200)
	if initial["orderCompletedEmail"] != false || initial["recipient"] != "a@example.test" || initial["version"] != float64(0) {
		t.Fatal(initial)
	}
	enabled := object(t, request(h, "POST", path, map[string]any{"orderCompletedEmail": true, "version": 0}, a), 200)
	if enabled["orderCompletedEmail"] != true || enabled["version"] != float64(1) {
		t.Fatal(enabled)
	}
	object(t, request(h, "POST", path, map[string]any{"orderCompletedEmail": false, "version": 0}, a), 409)
	object(t, request(h, "POST", path, map[string]any{"version": 1}, a), 400)
	object(t, request(h, "GET", path, nil, nil), 401)
	other := object(t, request(h, "GET", path, nil, b), 200)
	if other["orderCompletedEmail"] != false || other["recipient"] != "b@example.test" {
		t.Fatal("settings leaked", other)
	}
	admin := login(t, h, mail, "admin", "")
	preview := object(t, request(h, "GET", "/api/admin/partners/a/notification-settings", nil, admin), 200)
	if preview["orderCompletedEmail"] != true {
		t.Fatal(preview)
	}
	object(t, request(h, "POST", "/api/admin/partners/a/notification-settings", map[string]any{"orderCompletedEmail": false, "version": 1}, admin), 405)
	object(t, request(h, "GET", "/api/admin/partners/a/notification-settings", nil, b), 401)
	var count int
	if e := owner.DB.QueryRow(context.Background(), `SELECT count(*) FROM audit_events WHERE action='partner-notification-settings' AND target='a'`).Scan(&count); e != nil || count != 1 {
		t.Fatal("missing audit", count, e)
	}
}

func notificationFixture(t *testing.T) (*store.Store, *store.Store) {
	t.Helper()
	owner, app := fixture(t, true)
	execSQL(t, owner, `INSERT INTO partners(id,slug,name,email,active) VALUES('a','AAAAAA','星河 <shop>','a@example.test',true),('b','BBBBBB','B','b@example.test',true); INSERT INTO catalog_plans(id,product_id,name,price_cents) VALUES('plus','chatgpt','Plus',13900); INSERT INTO partner_notification_settings(partner_id,order_completed_email,version) VALUES('a',true,1)`)
	return owner, app
}

func notificationOrder(t *testing.T, s *store.Store, id string) {
	t.Helper()
	now := time.Now().UnixMilli()
	execSQL(t, s, `INSERT INTO orders(id,owner_hash,idempotency_key,plan,plan_name,price_cents,partner_id,mode,payment_status,status,customer_email,created_at,updated_at,last_event_id) VALUES($1,'owner',$1,'plus','Plus',13900,'a','live','paid','processing','buyer@example.test',$2,$2,'fixture')`, id, now)
}

func TestPartnerNotificationQueueAndFrozenRetry(t *testing.T) {
	owner, app := notificationFixture(t)
	for _, id := range []string{"SEND", "OFF", "TEST", "DEMO", "UNPAID", "REFUND", "ROLLBACK"} {
		notificationOrder(t, app, id)
	}
	execSQL(t, owner, `UPDATE orders SET partner_id='b' WHERE id='OFF'; UPDATE orders SET is_test=1 WHERE id='TEST'; UPDATE orders SET mode='demo',payment_status='simulated' WHERE id='DEMO'; UPDATE orders SET payment_status='unpaid' WHERE id='UNPAID'; UPDATE orders SET refunded_cents=13900 WHERE id='REFUND'`)
	execSQL(t, app, `UPDATE orders SET status='success' WHERE id<>'ROLLBACK'`)
	tx, e := app.DB.Begin(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(context.Background(), `UPDATE orders SET status='success' WHERE id='ROLLBACK'`); e != nil {
		t.Fatal(e)
	}
	_ = tx.Rollback(context.Background())
	// Turning on another shop does not enqueue its already-completed order.
	execSQL(t, owner, `INSERT INTO partner_notification_settings(partner_id,order_completed_email,version) VALUES('b',true,1)`)
	var count int
	if e = owner.DB.QueryRow(context.Background(), `SELECT count(*) FROM partner_order_notifications`).Scan(&count); e != nil || count != 1 {
		t.Fatal("wrong eligibility or rollback", count, e)
	}
	fake := &frozenMail{fail: true}
	service := delivery.Service{Store: app, Sender: fake, Origin: testOrigin}
	out, e := service.PartnerOrderNotifications(context.Background(), 20)
	if e != nil || out["failed"] != 1 || len(fake.bodies) != 1 {
		t.Fatal(out, e)
	}
	var message mailer.Frozen
	if e = json.Unmarshal([]byte(fake.bodies[0]), &message); e != nil {
		t.Fatal(e)
	}
	if len(message.To) != 1 || message.To[0] != "a@example.test" {
		t.Fatal("wrong recipient")
	}
	for _, part := range []string{"ChatGPT", "Plus", "¥139.00", "buyer@example.test", "SEND", "北京时间", "星河 <shop>"} {
		if !strings.Contains(message.Text, part) {
			t.Fatalf("missing %s in %s", part, message.Text)
		}
	}
	if strings.Contains(message.HTML, "<shop>") || !strings.Contains(message.HTML, "&lt;shop&gt;") || strings.Contains(message.HTML, "__RECHARGE_") {
		t.Fatal("unsafe or unresolved template")
	}
	if !strings.Contains(message.HTML, testOrigin+"/join?tab=orders") {
		t.Fatal("wrong workspace link")
	}
	execSQL(t, owner, `UPDATE partner_order_notifications SET next_attempt_at=0; UPDATE orders SET plan_name='Changed',customer_email='changed@example.test' WHERE id='SEND'; UPDATE partners SET name='Changed' WHERE id='a'`)
	fake.fail = false
	out, e = service.PartnerOrderNotifications(context.Background(), 20)
	if e != nil || out["sent"] != 1 || len(fake.bodies) != 2 || fake.bodies[0] != fake.bodies[1] || fake.keys[0] != fake.keys[1] {
		t.Fatal("retry changed", out, e)
	}
	execSQL(t, app, `UPDATE orders SET status='failed' WHERE id='SEND'; UPDATE orders SET status='success' WHERE id='SEND'`)
	out, e = service.PartnerOrderNotifications(context.Background(), 20)
	if e != nil || out["sent"] != 0 || len(fake.bodies) != 2 {
		t.Fatal("duplicate notification", out, e)
	}
}

func TestPartnerNotificationCancellation(t *testing.T) {
	cases := map[string]string{
		"disabled":         `UPDATE partner_notification_settings SET order_completed_email=false,version=2`,
		"reenabled":        `UPDATE partner_notification_settings SET order_completed_email=false,version=2; UPDATE partner_notification_settings SET order_completed_email=true,version=3`,
		"refund":           `UPDATE orders SET refunded_cents=13900`,
		"hold":             `UPDATE orders SET refund_hold=1`,
		"inactive":         `UPDATE partners SET active=false`,
		"recipient-change": `UPDATE partners SET email='new@example.test' WHERE id='a'`,
		"failed":           `UPDATE orders SET status='failed'`,
		"expired":          `UPDATE partner_order_notifications SET first_attempt_at=1`,
	}
	for name, sql := range cases {
		t.Run(name, func(t *testing.T) {
			owner, app := notificationFixture(t)
			notificationOrder(t, app, "CANCEL")
			execSQL(t, app, `UPDATE orders SET status='success' WHERE id='CANCEL'`)
			execSQL(t, owner, sql)
			fake := &frozenMail{}
			out, e := (delivery.Service{Store: app, Sender: fake, Origin: testOrigin}).PartnerOrderNotifications(context.Background(), 20)
			if e != nil || out["sent"] != 0 || len(fake.bodies) != 0 {
				t.Fatal("ineligible email sent", out, e)
			}
			var cancelled bool
			if e = owner.DB.QueryRow(context.Background(), `SELECT cancelled_at IS NOT NULL FROM partner_order_notifications WHERE order_id='CANCEL'`).Scan(&cancelled); e != nil || !cancelled {
				t.Fatal("not cancelled", e)
			}
		})
	}
}

type concurrentNotificationMail struct {
	frozenMail
	mu sync.Mutex
}

func (m *concurrentNotificationMail) SendFrozen(ctx context.Context, body, key string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.frozenMail.SendFrozen(ctx, body, key)
}

func TestPartnerNotificationConcurrentWorkers(t *testing.T) {
	_, app := notificationFixture(t)
	notificationOrder(t, app, "ONCE")
	execSQL(t, app, `UPDATE orders SET status='success' WHERE id='ONCE'`)
	fake := &concurrentNotificationMail{}
	service := delivery.Service{Store: app, Sender: fake, Origin: testOrigin}
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, e := service.PartnerOrderNotifications(context.Background(), 20); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	if len(fake.bodies) != 1 {
		t.Fatal("concurrent duplicate", len(fake.bodies))
	}
}
