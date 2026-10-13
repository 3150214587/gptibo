package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"gptibo/storefront/internal/fault"
	"gptibo/storefront/internal/notifications"
	"gptibo/storefront/internal/telegram"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTelegramProvisionReplayAndDurableInvitation(t *testing.T) {
	owner, app, a, _, _ := identityFixture(t)
	ctx := context.Background()
	execSQL(t, owner, `INSERT INTO catalog_plans(id,product_id,name,price_cents,default_cost_cents) VALUES('plus','chatgpt','Plus',16900,10000)`)
	client, e := telegram.NewClient("123456:fixture", "fixture_bot", strings.Repeat("s", 32), []int64{-100123})
	if e != nil {
		t.Fatal(e)
	}
	failReply, member := false, true
	replies := 0
	client.HTTP.Transport = rechargeTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "api.telegram.org" {
			t.Fatal("unexpected host")
		}
		result := `true`
		if strings.HasSuffix(r.URL.Path, "getChatMember") {
			result = `{"status":"member"}`
			if !member {
				result = `{"status":"left"}`
			}
		} else if strings.HasSuffix(r.URL.Path, "sendMessage") {
			replies++
			if failReply {
				return nil, errors.New("lost acknowledgement")
			}
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":` + result + `}`))}, nil
	})
	mail := &frozenMail{fail: true}
	s := telegram.Service{Store: app, Auth: a, Client: client, Sender: mail, Origin: testOrigin}
	call := func(id int, text string, callback bool, secret string) error {
		msg := map[string]any{"text": text, "from": map[string]any{"id": 77}, "chat": map[string]any{"id": 77, "type": "private"}}
		u := map[string]any{"update_id": id, "message": msg}
		if callback {
			delete(u, "message")
			u["callback_query"] = map[string]any{"id": "callback", "data": text, "from": map[string]any{"id": 77}, "message": msg}
		}
		raw, _ := json.Marshal(u)
		r := httptest.NewRequest("POST", "/api/telegram/webhook", strings.NewReader(string(raw)))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Telegram-Bot-Api-Secret-Token", secret)
		_, err := s.Webhook(ctx, r)
		return err
	}
	send := func(id int, text string, callback bool) {
		t.Helper()
		if err := call(id, text, callback, client.Secret); err != nil {
			t.Fatal(err)
		}
	}
	if fault.Status(call(1, "/apply", false, "wrong")) != 403 {
		t.Fatal("unauthenticated webhook accepted")
	}
	send(1, "/start apply_-999", false)
	send(2, "/apply", false)
	send(3, "wechat_77", false)
	send(4, "1234567", false)
	send(5, "agent@example.test", false)
	var expires int64
	if e = owner.DB.QueryRow(ctx, `SELECT expires_at FROM telegram_partner_sessions WHERE user_id=77`).Scan(&expires); e != nil {
		t.Fatal(e)
	}
	confirm := fmt.Sprintf("apply_confirm:%d", expires)
	member = false
	send(6, confirm, true)
	var count int
	owner.DB.QueryRow(ctx, `SELECT count(*) FROM partners`).Scan(&count)
	if count != 0 {
		t.Fatal("nonmember provisioned")
	}
	member = true
	failReply = true
	if call(7, confirm, true, client.Secret) == nil {
		t.Fatal("reply failure hidden")
	}
	failReply = false
	send(7, confirm, true)
	send(8, confirm, true)
	send(3, "wechat_77", false)
	if fault.Status(call(7, "different", true, client.Secret)) != 409 {
		t.Fatal("update identity not bound")
	}
	var id, partner, email, step, wechat string
	if e = owner.DB.QueryRow(ctx, `SELECT id,partner_id,email FROM telegram_partner_applications WHERE user_id=77`).Scan(&id, &partner, &email); e != nil {
		t.Fatal(e)
	}
	owner.DB.QueryRow(ctx, `SELECT count(*) FROM partners`).Scan(&count)
	if count != 1 {
		t.Fatal("duplicate partner")
	}
	owner.DB.QueryRow(ctx, `SELECT step,wechat FROM telegram_partner_sessions WHERE user_id=77`).Scan(&step, &wechat)
	if step != "submitted" || wechat != "" {
		t.Fatal("session not cleared")
	}
	owner.DB.QueryRow(ctx, `SELECT count(*) FROM partner_prices WHERE partner_id=$1 AND cost_cents=10000`, partner).Scan(&count)
	if count != 1 {
		t.Fatal("defaults missing")
	}
	owner.DB.QueryRow(ctx, `SELECT email FROM support_contacts WHERE owner=$1`, partner).Scan(&email)
	if email != "" {
		t.Fatal("private email published")
	}
	client.RegistrationClosed = true
	send(9, "/status", false)
	var savedReply string
	if e = owner.DB.QueryRow(ctx, `SELECT reply FROM telegram_partner_updates WHERE update_id=9`).Scan(&savedReply); e != nil || !strings.Contains(savedReply, "代理已开通") {
		t.Fatal("registration pause hid existing application status", e)
	}
	if s.Invitations(ctx) == nil {
		t.Fatal("mail uncertainty hidden")
	}
	if len(mail.keys) != 1 || mail.keys[0] != "partner-welcome/telegram-"+id {
		t.Fatal("legacy mail identity changed")
	}
	if e = s.Invitations(ctx); e != nil || len(mail.keys) != 1 {
		t.Fatal("backoff not enforced", e)
	}
	execSQL(t, owner, `UPDATE telegram_partner_applications SET next_attempt_at=0 WHERE id=$1`, id)
	mail.fail = false
	if e = s.Invitations(ctx); e != nil {
		t.Fatal(e)
	}
	if len(mail.keys) != 2 || mail.keys[0] != mail.keys[1] || mail.bodies[0] != mail.bodies[1] {
		t.Fatal("mail retry mutated")
	}
	if e = s.Invitations(ctx); e != nil || len(mail.keys) != 2 {
		t.Fatal("successful mail sent again", e)
	}
	if replies == 0 {
		t.Fatal("bot never replied")
	}
}

func TestPayoutNotificationRetriesOriginalIdentity(t *testing.T) {
	owner, app := fixture(t, true)
	ctx := context.Background()
	execSQL(t, owner, `INSERT INTO partners(id,slug,name,email) VALUES('p','ABC123','Agent','agent@example.test'); INSERT INTO partner_payouts(id,partner_id,amount_cents,recipient,created_at,request_key,gross_cents,deduction_cents) VALUES('pay','p',1000,'receiver',1,'req',1200,200); INSERT INTO payout_notifications(payout_id,created_at) VALUES('pay',1)`)
	f, e := notifications.NewFeishu("fixture", "fixture", "ou_fixture", testOrigin)
	if e != nil {
		t.Fatal(e)
	}
	fail := true
	var keys []string
	f.HTTP.Transport = rechargeTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "open.feishu.cn" {
			t.Fatal("unexpected destination")
		}
		body := `{"code":0,"tenant_access_token":"fixture-token"}`
		if strings.Contains(r.URL.Path, "/im/") {
			var payload map[string]string
			json.NewDecoder(r.Body).Decode(&payload)
			keys = append(keys, payload["uuid"])
			if payload["receive_id"] != "ou_fixture" || !strings.Contains(payload["content"], "12.00") {
				t.Fatal("notification mismatch")
			}
			if fail {
				return nil, errors.New("unknown send")
			}
			body = `{"code":0}`
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	s := notifications.Service{Store: app, Sender: f}
	if s.Reconcile(ctx) == nil {
		t.Fatal("send failure hidden")
	}
	var next int64
	owner.DB.QueryRow(ctx, `SELECT next_attempt_at FROM payout_notifications WHERE payout_id='pay'`).Scan(&next)
	if next <= time.Now().UnixMilli() {
		t.Fatal("no backoff")
	}
	if e = s.Reconcile(ctx); e != nil || len(keys) != 1 {
		t.Fatal("retried before backoff", e)
	}
	execSQL(t, owner, `UPDATE payout_notifications SET next_attempt_at=0 WHERE payout_id='pay'`)
	fail = false
	if e = s.Reconcile(ctx); e != nil {
		t.Fatal(e)
	}
	if e = s.Reconcile(ctx); e != nil || len(keys) != 2 || keys[0] != keys[1] || keys[0] != "ymx-payout-pay" {
		t.Fatal("deduplication lost", e)
	}
}
