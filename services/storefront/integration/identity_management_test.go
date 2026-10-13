package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gptibo/storefront/internal/auth"
	"gptibo/storefront/internal/fault"
	"gptibo/storefront/internal/httpapi"
	"gptibo/storefront/internal/security"
	"gptibo/storefront/internal/store"
)

const testOrigin = "https://storefront.example.test"
const testTOTP = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"

type deliveredCode struct{ Role, To, Code string }
type fakeMail struct {
	mu   sync.Mutex
	sent map[string]deliveredCode
	fail bool
}

func (m *fakeMail) SendCode(_ context.Context, role, to, code, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail {
		return errors.New("synthetic email failure")
	}
	m.sent[id] = deliveredCode{role, to, code}
	return nil
}
func (m *fakeMail) get(id string) deliveredCode { m.mu.Lock(); defer m.mu.Unlock(); return m.sent[id] }
func identityFixture(t *testing.T) (*store.Store, *store.Store, *auth.Service, http.Handler, *fakeMail) {
	owner, app := fixture(t, true)
	mailer := &fakeMail{sent: map[string]deliveredCode{}}
	a, e := auth.New(app, auth.Config{Secret: strings.Repeat("a1", 32), AdminUsername: "fixture-admin", AdminEmail: "admin@example.test", AdminTOTP: testTOTP, Sender: mailer})
	if e != nil {
		t.Fatal(e)
	}
	gin.SetMode(gin.TestMode)
	h := httpapi.Router(app, "fixture-worker", slog.New(slog.NewJSONHandler(io.Discard, nil)), httpapi.Options{Origin: testOrigin, Auth: a})
	return owner, app, a, h, mailer
}
func request(h http.Handler, method, path string, body any, cookie *http.Cookie) *httptest.ResponseRecorder {
	var b bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&b).Encode(body)
	}
	r := httptest.NewRequest(method, path, &b)
	r.Header.Set("Origin", testOrigin)
	r.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func object(t *testing.T, w *httptest.ResponseRecorder, status int) map[string]any {
	t.Helper()
	if w.Code != status {
		t.Fatalf("wanted HTTP %d, got %d: %s", status, w.Code, w.Body.String())
	}
	var out map[string]any
	if e := json.Unmarshal(w.Body.Bytes(), &out); e != nil {
		t.Fatal(e)
	}
	return out
}
func login(t *testing.T, h http.Handler, mail *fakeMail, role, email string) *http.Cookie {
	t.Helper()
	sendPath, verifyPath := "/api/"+role+"/auth/send", "/api/"+role+"/auth/verify"
	input := map[string]any{"email": email}
	if role == "admin" {
		sendPath = "/api/admin/login/email-code"
		verifyPath = "/api/admin/login"
		input = map[string]any{"username": "fixture-admin"}
	}
	if role == "customer" {
		sendPath = "/api/orders/recovery/send"
		verifyPath = "/api/orders/recovery/verify"
	}
	out := object(t, request(h, "POST", sendPath, input, nil), 200)
	id := out["challenge"].(string)
	code := mail.get(id).Code
	if code == "" {
		t.Fatal("fake mail missing")
	}
	verify := map[string]any{"challenge": id, "code": code}
	if role == "admin" {
		verify["username"] = "fixture-admin"
		verify["emailCode"] = code
		verify["code"] = security.TOTP(testTOTP, time.Now().Unix()/30)
	}
	if role == "partner" {
		verify = map[string]any{"email": email, "code": code}
	}
	w := request(h, "POST", verifyPath, verify, nil)
	object(t, w, 200)
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal("missing session cookie")
	}
	c := cookies[0]
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || !strings.HasPrefix(c.Name, "__Host-sf_") {
		t.Fatal("unsafe session cookie")
	}
	return c
}

func TestAdminThreeFactorLoginAndReplay(t *testing.T) {
	owner, _, a, h, mail := identityFixture(t)
	out := object(t, request(h, "POST", "/api/admin/login/email-code", map[string]any{"username": "fixture-admin", "email": "attacker@example.test"}, nil), 200)
	id := out["challenge"].(string)
	sent := mail.get(id)
	if sent.To != "admin@example.test" {
		t.Fatal("administrator destination overridden")
	}
	in := auth.VerifyInput{Username: "fixture-admin", Challenge: id, EmailCode: sent.Code, Code: security.TOTP(testTOTP, time.Now().Unix()/30)}
	wrong := in
	wrong.Code = "abcdef"
	if _, e := a.Verify(context.Background(), "admin", wrong, "test"); fault.Status(e) != 401 {
		t.Fatal("missing TOTP accepted")
	}
	wrong = in
	wrong.Username = "someone-else"
	if _, e := a.Verify(context.Background(), "admin", wrong, "test"); fault.Status(e) != 401 {
		t.Fatal("wrong administrator accepted")
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, e := a.Verify(context.Background(), "admin", in, "test"); results <- e }()
	}
	wg.Wait()
	close(results)
	success := 0
	for e := range results {
		if e == nil {
			success++
		} else if fault.Status(e) != 401 {
			t.Fatal(e)
		}
	}
	if success != 1 {
		t.Fatal("concurrent challenge consumed multiple times")
	}
	execSQL(t, owner, "UPDATE auth_limits SET reset_at=now()-interval '1 second'")
	next, e := a.Send(context.Background(), "admin", auth.SendInput{Username: "fixture-admin"}, "test")
	if e != nil {
		t.Fatal(e)
	}
	in.Challenge = next
	in.EmailCode = mail.get(next).Code
	if _, e = a.Verify(context.Background(), "admin", in, "test"); fault.Status(e) != 401 {
		t.Fatal("TOTP time step replay accepted")
	}
	var count int
	if e = owner.DB.QueryRow(context.Background(), "SELECT count(*) FROM auth_sessions WHERE role='admin'").Scan(&count); e != nil || count != 1 {
		t.Fatal("invalid session count")
	}
}

func TestAuthenticationFailuresLimitsAndRotation(t *testing.T) {
	owner, app, a, h, mail := identityFixture(t)
	unknown := object(t, request(h, "POST", "/api/partner/auth/send", map[string]any{"email": "missing@example.test"}, nil), 200)
	if mail.get(unknown["challenge"].(string)).Code != "" {
		t.Fatal("unknown partner received mail")
	}
	object(t, request(h, "POST", "/api/partner/auth/send", map[string]any{"email": "missing@example.test"}, nil), 429)
	execSQL(t, owner, "INSERT INTO partners(id,slug,name,email,active) VALUES('a','ABC123','A','a@example.test',true)")
	cookie := login(t, h, mail, "partner", "a@example.test")
	object(t, request(h, "GET", "/api/partner/plans", nil, cookie), 200)
	object(t, request(h, "GET", "/api/admin/plans", nil, cookie), 401)
	rotated, e := auth.New(app, auth.Config{Secret: strings.Repeat("b2", 32), Sender: mail})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = rotated.Require(context.Background(), app.DB, "partner", cookie.Value); fault.Status(e) != 401 {
		t.Fatal("old session survived key rotation")
	}
	execSQL(t, owner, "UPDATE partners SET active=false,auth_version=auth_version+1 WHERE id='a'")
	object(t, request(h, "GET", "/api/partner/plans", nil, cookie), 401)
	execSQL(t, owner, "UPDATE partners SET active=true,auth_version=auth_version+1 WHERE id='a'")
	object(t, request(h, "GET", "/api/partner/plans", nil, cookie), 401)
	id, e := a.Send(context.Background(), "customer", auth.SendInput{Email: "lockout@example.test"}, "lockout")
	if e != nil {
		t.Fatal(e)
	}
	code := mail.get(id).Code
	wrong := "000000"
	if wrong == code {
		wrong = "111111"
	}
	for i := 0; i < 5; i++ {
		if _, e = a.Verify(context.Background(), "customer", auth.VerifyInput{Challenge: id, Code: wrong}, "lockout"); fault.Status(e) != 401 {
			t.Fatal("bad code accepted")
		}
	}
	if _, e = a.Verify(context.Background(), "customer", auth.VerifyInput{Challenge: id, Code: code}, "lockout"); fault.Status(e) != 401 {
		t.Fatal("attempt cap bypassed")
	}
	mail.mu.Lock()
	mail.fail = true
	mail.mu.Unlock()
	if _, e = a.Send(context.Background(), "customer", auth.SendInput{Email: "failed@example.test"}, "failure"); fault.Status(e) != 503 {
		t.Fatal("failed mail treated as success")
	}
	var count int
	if e = owner.DB.QueryRow(context.Background(), "SELECT count(*) FROM auth_challenges WHERE subject='failed@example.test'").Scan(&count); e != nil || count != 0 {
		t.Fatal("failed mail retained usable challenge")
	}
}

func TestCustomerSessionLogoutExpiryAndRoleSeparation(t *testing.T) {
	owner, _, _, h, mail := identityFixture(t)
	c := login(t, h, mail, "customer", "buyer@example.test")
	session := object(t, request(h, "GET", "/api/orders/recovery/session", nil, c), 200)
	if session["email"] != "buyer@example.test" {
		t.Fatal("wrong recovered email")
	}
	object(t, request(h, "GET", "/api/admin/plans", nil, c), 401)
	object(t, request(h, "GET", "/api/partner/plans", nil, c), 401)
	forged := *c
	forged.Name = "__Host-sf_admin"
	object(t, request(h, "GET", "/api/admin/plans", nil, &forged), 401)
	object(t, request(h, "POST", "/api/orders/recovery/logout", map[string]any{}, c), 200)
	session = object(t, request(h, "GET", "/api/orders/recovery/session", nil, c), 200)
	if session["email"] != nil {
		t.Fatal("logout failed")
	}
	c = login(t, h, mail, "customer", "expired@example.test")
	execSQL(t, owner, "UPDATE auth_sessions SET expires_at=now()-interval '1 second'")
	if object(t, request(h, "GET", "/api/orders/recovery/session", nil, c), 200)["email"] != nil {
		t.Fatal("expired session accepted")
	}
}

func TestManagementEndToEndAndConcurrentEdits(t *testing.T) {
	owner, _, _, h, mail := identityFixture(t)
	admin := login(t, h, mail, "admin", "")
	plan := map[string]any{"id": "plus", "productId": "chatgpt", "name": "Plus", "priceCents": 16900, "defaultCostCents": 12000, "enabled": true, "partnerEnabled": true, "note": "test catalogue"}
	object(t, request(h, "POST", "/api/admin/plans", plan, admin), 200)
	a := object(t, request(h, "POST", "/api/admin/partners", map[string]any{"email": "a@example.test"}, admin), 200)
	b := object(t, request(h, "POST", "/api/admin/partners", map[string]any{"email": "b@example.test"}, admin), 200)
	if a["invitationSent"] != false {
		t.Fatal("unimplemented invitation falsely reported sent")
	}
	ca := login(t, h, mail, "partner", "a@example.test")
	cb := login(t, h, mail, "partner", "b@example.test")
	object(t, request(h, "POST", "/api/partner/prices/plus", map[string]any{"priceCents": 11999, "version": 0}, ca), 400)
	object(t, request(h, "POST", "/api/partner/prices/plus", map[string]any{"priceCents": 18000, "version": 0}, ca), 200)
	object(t, request(h, "POST", "/api/partner/prices/plus", map[string]any{"priceCents": 19000, "version": 0}, ca), 409)
	object(t, request(h, "POST", "/api/partner/prices/plus", map[string]any{"priceCents": 19000, "version": 1, "partnerId": b["id"]}, ca), 400)
	object(t, request(h, "POST", "/api/admin/partners/"+b["id"].(string)+"/prices/plus", map[string]any{"costCents": 13000, "version": 0}, ca), 401)
	object(t, request(h, "POST", "/api/partner/support-contacts", map[string]any{"wechat": "only-a", "qq": "12345", "email": "help@example.test", "version": 0}, ca), 200)
	object(t, request(h, "POST", "/api/partner/support-contacts", map[string]any{"wechat": "only-a-2", "qq": "12345", "email": "help@example.test", "version": 1}, ca), 200)
	object(t, request(h, "POST", "/api/partner/announcement", map[string]any{"enabled": true, "placement": "home", "title": "A notice", "content": "Only A", "version": 0}, ca), 200)
	ac := object(t, request(h, "GET", "/api/catalog?partner="+a["slug"].(string), nil, nil), 200)
	bc := object(t, request(h, "GET", "/api/catalog?partner="+b["slug"].(string), nil, nil), 200)
	if ac["plans"].([]any)[0].(map[string]any)["price_cents"] != float64(18000) || bc["plans"].([]any)[0].(map[string]any)["price_cents"] != float64(16900) {
		t.Fatal("partner pricing isolation failed")
	}
	if ac["supportContacts"].(map[string]any)["wechat"] != "only-a-2" || bc["announcement"] != nil || ac["checkoutEnabled"] != false {
		t.Fatal("settings isolation or checkout gate failed")
	}
	object(t, request(h, "GET", "/api/partner/plans", nil, cb), 200)
	var wg sync.WaitGroup
	statuses := make(chan int, 2)
	for _, price := range []int{17500, 18500} {
		wg.Add(1)
		go func(value int) {
			defer wg.Done()
			statuses <- request(h, "POST", "/api/admin/plans/plus", map[string]any{"priceCents": value, "enabled": true, "version": 0, "note": "concurrent edit"}, admin).Code
		}(price)
	}
	wg.Wait()
	close(statuses)
	counts := map[int]int{}
	for status := range statuses {
		counts[status]++
	}
	if counts[200] != 1 || counts[409] != 1 {
		t.Fatalf("concurrent edit outcomes: %v", counts)
	}
	var n int
	if e := owner.DB.QueryRow(context.Background(), "SELECT count(*) FROM audit_events WHERE action='plan-save'").Scan(&n); e != nil || n != 2 {
		t.Fatal("audit not atomic with successful plan changes")
	}
	object(t, request(h, "POST", "/api/admin/partners/"+a["id"].(string), map[string]any{"name": "A", "email": "a@example.test", "active": false, "version": 0}, admin), 200)
	object(t, request(h, "POST", "/api/partner/prices/plus", map[string]any{"priceCents": 20000, "version": 1}, ca), 401)
	object(t, request(h, "GET", "/api/catalog?partner="+a["slug"].(string), nil, nil), 404)
}

func TestCSRFBodyLimitsAndSpoofedProxyHeaders(t *testing.T) {
	_, _, _, h, mail := identityFixture(t)
	admin := login(t, h, mail, "admin", "")
	for _, origin := range []string{"", "https://evil.example.test"} {
		r := httptest.NewRequest("POST", "/api/admin/logout", strings.NewReader(`{}`))
		r.AddCookie(admin)
		r.Header.Set("Origin", origin)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatal("cross-origin write accepted")
		}
	}
	for _, body := range []string{`{} {}`, `{"username":"` + strings.Repeat("x", 33000) + `"}`} {
		r := httptest.NewRequest("POST", "/api/admin/login/email-code", strings.NewReader(body))
		r.Header.Set("Origin", testOrigin)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatal("malformed/oversized body accepted")
		}
	}
	for i := 0; i < 10; i++ {
		r := httptest.NewRequest("POST", "/api/partner/auth/send", strings.NewReader(`{"email":"unknown`+string(rune('a'+i))+`@example.test"}`))
		r.Header.Set("Origin", testOrigin)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Forwarded-For", "203.0.113."+string(rune('0'+i)))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("request %d status %d", i, w.Code)
		}
	}
	object(t, request(h, "POST", "/api/partner/auth/send", map[string]any{"email": "one-more@example.test"}, nil), 429)
}

func TestTransactionRollbackAndAuthenticationRetention(t *testing.T) {
	owner, app, _, h, mail := identityFixture(t)
	admin := login(t, h, mail, "admin", "")
	object(t, request(h, "POST", "/api/admin/plans", map[string]any{"id": "plus", "productId": "chatgpt", "name": "Plus", "priceCents": 16900, "defaultCostCents": 12000, "enabled": true, "note": "fixture"}, admin), 200)
	// A failing audit write must roll back the associated business update.
	execSQL(t, owner, `CREATE FUNCTION reject_test_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic audit failure'; END $$;
 CREATE TRIGGER reject_test_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_test_audit();`)
	object(t, request(h, "POST", "/api/admin/plans/plus", map[string]any{"priceCents": 20000, "enabled": true, "version": 0, "note": "must roll back"}, admin), 503)
	var price, version int64
	if e := owner.DB.QueryRow(context.Background(), "SELECT price_cents,version FROM catalog_plans WHERE id='plus'").Scan(&price, &version); e != nil || price != 16900 || version != 0 {
		t.Fatal("business update survived failed audit")
	}
	execSQL(t, owner, `DROP TRIGGER reject_test_audit ON audit_events;
 INSERT INTO auth_sessions(token_hash,role,subject,credential_version,expires_at) VALUES('expired','customer','old@example.test','fixture',now()-interval '1 second');
 INSERT INTO auth_limits(key,attempts,reset_at) VALUES('expired',1,now()-interval '1 second');
 INSERT INTO auth_challenges(id,role,subject,credential_version,code_hash,expires_at) VALUES('00000000-0000-4000-8000-000000000001','customer','old@example.test','fixture','fixture',now()-interval '2 hours');`)
	if e := app.PruneAuth(context.Background()); e != nil {
		t.Fatal(e)
	}
	var count int
	if e := owner.DB.QueryRow(context.Background(), "SELECT count(*) FROM auth_sessions WHERE token_hash='expired'").Scan(&count); e != nil || count != 0 {
		t.Fatal("expired session retained")
	}
	object(t, request(h, "GET", "/api/admin/plans", nil, admin), 200)
	if e := owner.DB.QueryRow(context.Background(), "SELECT count(*) FROM audit_events").Scan(&count); e != nil || count != 1 {
		t.Fatal("retention affected audit history")
	}
}

func TestExpiredPendingAndSupersededChallenges(t *testing.T) {
	owner, _, a, _, mail := identityFixture(t)
	ctx := context.Background()
	id, e := a.Send(ctx, "customer", auth.SendInput{Email: "expiry@example.test"}, "test")
	if e != nil {
		t.Fatal(e)
	}
	execSQL(t, owner, "UPDATE auth_challenges SET expires_at=now()-interval '1 second' WHERE id=$1", id)
	if _, e = a.Verify(ctx, "customer", auth.VerifyInput{Challenge: id, Code: mail.get(id).Code}, "test"); fault.Status(e) != 401 {
		t.Fatal("expired challenge accepted")
	}
	id, e = a.Send(ctx, "customer", auth.SendInput{Email: "pending@example.test"}, "test")
	if e != nil {
		t.Fatal(e)
	}
	execSQL(t, owner, "UPDATE auth_challenges SET ready=false WHERE id=$1", id)
	if _, e = a.Verify(ctx, "customer", auth.VerifyInput{Challenge: id, Code: mail.get(id).Code}, "test"); fault.Status(e) != 401 {
		t.Fatal("unconfirmed email accepted")
	}
	execSQL(t, owner, "UPDATE auth_limits SET reset_at=now()-interval '1 second'")
	next, e := a.Send(ctx, "customer", auth.SendInput{Email: "pending@example.test"}, "test")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = a.Verify(ctx, "customer", auth.VerifyInput{Challenge: id, Code: mail.get(id).Code}, "test"); fault.Status(e) != 401 {
		t.Fatal("superseded challenge accepted")
	}
	if _, e = a.Verify(ctx, "customer", auth.VerifyInput{Challenge: next, Code: mail.get(next).Code}, "test"); e != nil {
		t.Fatal("fresh challenge failed", e)
	}
}
