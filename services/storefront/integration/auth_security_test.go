package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gptibo/storefront/internal/access"
	"gptibo/storefront/internal/httpapi"
	"gptibo/storefront/internal/mailer"
	"gptibo/storefront/internal/security"
	"gptibo/storefront/internal/securityaudit"
	"gptibo/storefront/internal/turnstile"
)

func TestAuthTurnstileActionsAndReplay(t *testing.T) {
	owner, app, a, _, mail := identityFixture(t)
	execSQL(t, owner, `INSERT INTO partners(id,slug,name,email,active) VALUES('captcha-partner','CAP123','Partner','partner@example.test',true)`)
	verification, err := turnstile.New([]byte(`[{"sitekey":"public","secret":"private","hosts":["storefront.example.test"]}]`))
	if err != nil {
		t.Fatal(err)
	}
	old := http.DefaultTransport
	used := map[string]bool{}
	http.DefaultTransport = turnstileTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://challenges.cloudflare.com/turnstile/v0/siteverify" {
			return nil, fmt.Errorf("unexpected provider")
		}
		r.ParseForm()
		token := r.Form.Get("response")
		parts := strings.Split(token, ":")
		action := parts[0]
		host := "storefront.example.test"
		if strings.Contains(token, "wrong-host") {
			host = "other.example.test"
		}
		ok := !used[token] && !strings.Contains(token, "forged")
		used[token] = true
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(fmt.Sprintf(`{"success":%t,"hostname":%q,"action":%q,"challenge_ts":%q}`, ok, host, action, time.Now().UTC().Format(time.RFC3339)))), Header: http.Header{}}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = old })
	h := httpapi.Router(app, "fixture", slog.New(slog.NewTextHandler(io.Discard, nil)), httpapi.Options{Origin: testOrigin, Auth: a, Turnstile: verification, AuthTurnstile: true})
	for _, role := range []string{"customer", "partner", "admin"} {
		t.Run(role, func(t *testing.T) {
			send, verify := "/api/"+role+"/auth/send", "/api/"+role+"/auth/verify"
			in := map[string]any{"email": role + "@example.test"}
			if role == "customer" {
				send = "/api/orders/recovery/send"
				verify = "/api/orders/recovery/verify"
			}
			if role == "admin" {
				send = "/api/admin/login/email-code"
				verify = "/api/admin/login"
				in = map[string]any{"username": "fixture-admin"}
			}
			object(t, request(h, "POST", send, in, nil), 400)
			for _, bad := range []string{"order_create:" + role, role + "_verify:" + role, role + "_send:wrong-host", role + "_send:forged"} {
				in["turnstileToken"] = bad
				object(t, request(h, "POST", send, in, nil), 400)
			}
			var count int
			if err = owner.DB.QueryRow(context.Background(), `SELECT count(*) FROM auth_challenges WHERE role=$1`, role).Scan(&count); err != nil || count != 0 {
				t.Fatal("unverified send changed challenges", err)
			}
			in["turnstileToken"] = role + "_send:valid"
			id := object(t, request(h, "POST", send, in, nil), 200)["challenge"].(string)
			object(t, request(h, "POST", send, in, nil), 400)
			code := mail.get(id).Code
			login := map[string]any{"email": role + "@example.test", "challenge": id, "code": code}
			if role == "admin" {
				login = map[string]any{"username": "fixture-admin", "challenge": id, "emailCode": code, "code": security.TOTP(testTOTP, time.Now().Unix()/30)}
			}
			object(t, request(h, "POST", verify, login, nil), 400)
			login["turnstileToken"] = role + "_send:wrong-action"
			object(t, request(h, "POST", verify, login, nil), 400)
			login["turnstileToken"] = role + "_verify:valid"
			w := request(h, "POST", verify, login, nil)
			object(t, w, 200)
			if len(w.Result().Cookies()) != 1 {
				t.Fatal("verified login did not create session")
			}
			object(t, request(h, "POST", verify, login, nil), 400)
		})
	}
}

func TestSecurityAuditBlocksAndRedacts(t *testing.T) {
	owner, app, a, base, mail := identityFixture(t)
	admin := login(t, base, mail, "admin", "")
	audit := &securityaudit.Service{Store: app, Auth: a}
	h := httpapi.Router(app, "fixture", slog.New(slog.NewTextHandler(io.Discard, nil)), httpapi.Options{Origin: testOrigin, Auth: a, SecurityAudit: audit})
	cookie := &http.Cookie{Name: "__Host-sf_buyer", Value: security.Token()}
	for i := 0; i < 21; i++ {
		object(t, request(h, "GET", "/api/orders/00000000-0000-4000-8000-000000000001?token=secret-query", nil, cookie), 404)
	}
	object(t, request(h, "GET", "/api/orders", nil, cookie), 429)
	// A forged proxy header must not switch the source IP and bypass the ban.
	r := httptest.NewRequest("GET", "/api/orders", nil)
	r.Header.Set("X-Forwarded-For", "203.0.113.99")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	object(t, w, 429)
	// Callbacks retain independent admission even while the caller is restricted.
	if w = request(h, "POST", "/api/payments/notify/test", map[string]any{}, nil); w.Code == 429 {
		t.Fatal("blocked payment callback")
	}
	events, err := audit.Recent(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range events {
		if e.Kind == "block" {
			found = true
		}
		if e.IP != "192.0.2.1" || strings.Contains(fmt.Sprint(e), cookie.Value) || strings.Contains(fmt.Sprint(e), "secret-query") {
			t.Fatal("audit leaked secret or trusted spoofed IP")
		}
	}
	if !found {
		t.Fatal("missing ban audit")
	}
	// Ban expires without deleting history, then admin-only audit endpoint is readable.
	execSQL(t, owner, `UPDATE auth_limits SET reset_at=now()-interval '1 second'`)
	object(t, request(h, "GET", "/api/admin/security", nil, nil), 401)
	object(t, request(h, "GET", "/api/admin/security", nil, admin), 200)
}

func TestSecurityAlertAggregationAndRetry(t *testing.T) {
	owner, app, a, _, _ := identityFixture(t)
	start := time.Now().UTC().Truncate(15 * time.Minute).Add(-15 * time.Minute)
	// Exercise the real mail adapter so argument order and the wire contract
	// cannot be accidentally redefined by a permissive Sender mock.
	old := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = old })
	keys, sends, failSend := map[string]string{}, 0, true
	http.DefaultTransport = turnstileTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost || r.URL.String() != "https://api.resend.com/emails" {
			return nil, fmt.Errorf("unexpected mail request")
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		var frozen mailer.Frozen
		if err = json.Unmarshal(body, &frozen); err != nil || frozen.From != "alerts@example.test" || len(frozen.To) != 1 || frozen.To[0] != "admin@example.test" || !strings.Contains(frozen.Text, "触发 3 次") {
			return nil, fmt.Errorf("invalid frozen security alert")
		}
		key := r.Header.Get("Idempotency-Key")
		if key != "security-alert:"+start.Format("20060102T1504") {
			return nil, fmt.Errorf("wrong alert idempotency key")
		}
		if prior, ok := keys[key]; ok && prior != string(body) {
			return nil, fmt.Errorf("idempotency payload changed")
		}
		keys[key] = string(body)
		sends++
		status, response := http.StatusOK, `{"id":"fixture-message"}`
		if failSend {
			status, response = http.StatusServiceUnavailable, `{}`
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(response)), Header: http.Header{}}, nil
	})
	sender, err := mailer.NewResend("fixture-key", "alerts@example.test")
	if err != nil {
		t.Fatal(err)
	}
	audit := &securityaudit.Service{Store: app, Auth: a, Sender: sender, Recipient: "admin@example.test"}
	for i := 0; i < 3; i++ {
		_, err := owner.DB.Exec(context.Background(), `INSERT INTO audit_events(id,actor_role,actor_id,action,target,detail,created_at) VALUES($1,'guest','','security-block','','{"ip":"192.0.2.1"}',$2)`, security.ID(), start.Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
	}
	if audit.Alerts(context.Background()) == nil {
		t.Fatal("expected send failure")
	}
	failSend = false
	if err := audit.Alerts(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := audit.Alerts(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || sends != 2 {
		t.Fatal("alert was not aggregated/retried/deduplicated", sends)
	}
}

func TestAdminAccessCannotBeBypassedAtOrigin(t *testing.T) {
	_, app, a, _, _ := identityFixture(t)
	gate, err := access.New("https://team.cloudflareaccess.com", strings.Repeat("a", 64), "admin@example.test")
	if err != nil {
		t.Fatal(err)
	}
	h := httpapi.Router(app, "fixture", slog.New(slog.NewTextHandler(io.Discard, nil)), httpapi.Options{Origin: testOrigin, Auth: a, AdminAccess: gate})
	for _, path := range []string{"/api/admin/session", "/api/admin/orders", "/api/admin/security", "/api/admin/login"} {
		r := httptest.NewRequest("GET", "https://alternate.example.test"+path, nil)
		r.Header.Set("Cf-Access-Jwt-Assertion", "forged")
		r.Header.Set("Cf-Access-Authenticated-User-Email", "admin@example.test")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		object(t, w, 403)
	}
	object(t, request(h, "GET", "/api/partner/auth/session", nil, nil), 200)
	object(t, request(h, "GET", "/api/orders/recovery/session", nil, nil), 200)
}
