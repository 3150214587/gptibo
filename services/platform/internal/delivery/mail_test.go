package delivery

import (
	"context"
	"encoding/json"
	"gptibo/platform/internal/security"
	"gptibo/platform/internal/testutil"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestResendAcceptanceAndSafeErrors(t *testing.T) {
	t.Setenv("RESEND_API_KEY", "synthetic-key")
	t.Setenv("MAIL_FROM", "GPTibo <noreply@example.invalid>")
	for _, tc := range []struct {
		status     int
		body, code string
	}{
		{200, `{"id":"email-test"}`, ""},
		{200, `{}`, "mail_unconfirmed"},
		{200, `invalid`, "mail_unconfirmed"},
		{401, `{"message":"private diagnostic"}`, "mail_unauthorized"},
		{403, `{}`, "mail_sender_rejected"},
		{429, `{}`, "mail_rate_limited"},
		{422, `{}`, "mail_invalid_request"},
		{503, `{}`, "mail_service_unavailable"},
	} {
		client := &http.Client{Transport: alertTransport(func(r *http.Request) (*http.Response, error) {
			if r.URL.String() != "https://api.resend.com/emails" || r.Header.Get("Idempotency-Key") != "mail-stable" || r.Header.Get("Authorization") != "Bearer synthetic-key" {
				t.Fatal("wrong destination or unstable request identity")
			}
			var payload map[string]any
			if json.NewDecoder(r.Body).Decode(&payload) != nil || !strings.Contains(payload["html"].(string), "https://supply.example.invalid/join#test") {
				t.Fatal("invitation missing")
			}
			return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body)), Header: make(http.Header)}, nil
		})}
		err := sendMailWithClient(context.Background(), "mail-stable", notification{Email: "test@example.com", URL: "https://supply.example.invalid/join#test"}, client)
		if tc.code == "" && err != nil || tc.code != "" && (err == nil || err.Error() != tc.code) {
			t.Fatalf("status %d: got %v, expected %s", tc.status, err, tc.code)
		}
	}
}

func TestUnconfiguredMailKeepsInvitationRetryable(t *testing.T) {
	t.Setenv("RESEND_API_KEY", "")
	t.Setenv("MAIL_FROM", "")
	s := testutil.Database(t)
	ctx := context.Background()
	token := security.Token()
	body, _ := json.Marshal(notification{Email: "test@example.com", URL: "https://supply.example.invalid/join#" + token})
	_, err := s.DB.Exec(ctx, `INSERT INTO users(id,email,name,role,status) VALUES('mail-user','test@example.com','test','channel','invited')`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.DB.Exec(ctx, `INSERT INTO invitations(token_hash,user_id,expires_at) VALUES($1,'mail-user',now()+interval '48 hours')`, security.Hash(token))
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.DB.Exec(ctx, `INSERT INTO outbox(id,user_id,kind,payload_cipher,attempts) VALUES('mail-test','mail-user','invitation',$1,9)`, s.Vault.Seal("outbox:mail-test", body))
	if err != nil {
		t.Fatal(err)
	}
	w := &Worker{Store: s}
	if worked, err := w.outbox(ctx); !worked || err != nil {
		t.Fatal("notification not processed", err)
	}
	var status, code string
	var attempts int
	if err := s.DB.QueryRow(ctx, "SELECT status,last_error,attempts FROM outbox WHERE id='mail-test'").Scan(&status, &code, &attempts); err != nil || status != "pending" || code != "email_not_configured" || attempts != 9 {
		t.Fatal("missing configuration exhausted invitation", status, code, attempts, err)
	}
}

func TestNotificationsProceedWithoutOrderTick(t *testing.T) {
	s := testutil.Database(t)
	_, err := s.DB.Exec(context.Background(), `INSERT INTO users(id,email,name,role,status) VALUES('mail-user','test@example.com','test','channel','invited')`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.DB.Exec(context.Background(), `INSERT INTO outbox(id,user_id,kind,payload_cipher) VALUES('expired','mail-user','invitation',$1)`, s.Vault.Seal("outbox:expired", []byte(`{"email":"test@example.com","url":"https://supply.example.invalid/join#expired"}`)))
	if err != nil {
		t.Fatal(err)
	}
	w := &Worker{Store: s}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); w.notifications(ctx) }()
	defer func() { cancel(); <-done }()
	deadline := time.After(3 * time.Second)
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		var status string
		if err := s.DB.QueryRow(ctx, "SELECT status FROM outbox WHERE id='expired'").Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status == "dead" {
			return
		}
		select {
		case <-deadline:
			t.Fatal("notification depended on order tick")
		case <-ticker.C:
		}
	}
}
