package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTelegramRegistrationPauseStopsNewAndPendingApplications(t *testing.T) {
	f := botFixture(t)
	f.service.Client.RegistrationClosed = true
	ctx := context.Background()
	for i, text := range []string{"/apply", "/start apply_-10042", "/restart", "/help", "hello@example.test"} {
		m := groupMessage(int64(i+1), text, 202)
		m["chat"] = map[string]any{"id": 202, "type": "private"}
		if err := f.send(int64(i+1), "message", m); err != nil {
			t.Fatal(err)
		}
		last := f.calls[len(f.calls)-1]
		if last.method != "sendMessage" || !strings.Contains(last.data["text"].(string), "暂停新代理注册") || last.data["reply_markup"] != nil {
			t.Fatal("new applicant did not receive pause message", last)
		}
	}
	expires := time.Now().Add(time.Hour).UnixMilli()
	_, err := f.service.Store.DB.Exec(ctx, `UPDATE telegram_partner_sessions SET step='confirm',group_id=-10042,wechat='wechat_202',qq='1234567',email='new@example.test',expires_at=$1 WHERE user_id=202`, expires)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"update_id": 10, "callback_query": map[string]any{"id": "confirm", "data": fmt.Sprintf("apply_confirm:%d", expires), "from": map[string]any{"id": 202}, "message": map[string]any{"chat": map[string]any{"id": 202, "type": "private"}}}})
	r := httptest.NewRequest("POST", "/api/telegram/webhook", strings.NewReader(string(raw)))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Telegram-Bot-Api-Secret-Token", f.service.Client.Secret)
	if _, err = f.service.Webhook(ctx, r); err != nil {
		t.Fatal(err)
	}
	var partners, applications int
	var step, wechat, email string
	if err = f.service.Store.DB.QueryRow(ctx, `SELECT (SELECT count(*) FROM partners),(SELECT count(*) FROM telegram_partner_applications),step,wechat,email FROM telegram_partner_sessions WHERE user_id=202`).Scan(&partners, &applications, &step, &wechat, &email); err != nil || partners != 0 || applications != 0 || step != "idle" || wechat != "" || email != "" {
		t.Fatal("pending confirmation was not stopped and cleared", partners, applications, step, err)
	}
	if f.count("getChatMember") != 0 {
		t.Fatal("closed application queried membership")
	}
	if err = f.send(11, "message", groupMessage(11, "/apply", 202)); err != nil {
		t.Fatal(err)
	}
	last := f.calls[len(f.calls)-1]
	if !strings.Contains(last.data["text"].(string), "暂停新代理注册") || last.data["reply_markup"] != nil {
		t.Fatal("group still offers application button")
	}
	if err = f.send(12, "message", groupMessage(12, "/blockword nft", 101)); err != nil {
		t.Fatal(err)
	}
	if err = f.send(13, "message", groupMessage(13, "NFT", 202)); err != nil || f.count("deleteMessages") != 1 {
		t.Fatal("registration pause disabled word filter", err)
	}
	if err = f.send(14, "message", groupMessage(14, "/mute", 101)); err != nil || !f.active(t) {
		t.Fatal("registration pause disabled moderation", err)
	}
	f.service.Client.RegistrationClosed = false
	m := groupMessage(15, "/apply", 202)
	m["chat"] = map[string]any{"id": 202, "type": "private"}
	if err = f.send(15, "message", m); err != nil {
		t.Fatal(err)
	}
	if err = f.service.Store.DB.QueryRow(ctx, `SELECT step FROM telegram_partner_sessions WHERE user_id=202`).Scan(&step); err != nil || step != "wechat" {
		t.Fatal("registration did not resume", step, err)
	}
}
