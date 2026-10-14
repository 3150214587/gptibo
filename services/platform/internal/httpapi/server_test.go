package httpapi

import (
	"context"
	"encoding/json"
	"gptibo/platform/internal/commerce"
	"gptibo/platform/internal/security"
	"gptibo/platform/internal/testutil"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T) (*Server, http.Handler) {
	t.Helper()
	s := testutil.Database(t)
	_, e := s.DB.Exec(context.Background(), "INSERT INTO users(id,email,name,role,status) VALUES('admin','admin@example.com','管理员','admin','active'),('one','one@example.com','渠道一','channel','active'),('two','two@example.com','渠道二','channel','active'); INSERT INTO wallets(user_id) VALUES('one'),('two')")
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.DB.Exec(context.Background(), "INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,'admin',now()+interval '1 hour'),($2,'one',now()+interval '1 hour')", security.Hash("admin-session"), security.Hash("one-session"))
	if e != nil {
		t.Fatal(e)
	}
	server := &Server{Store: s, Commerce: &commerce.Service{Store: s}, Origin: "https://supply.example.invalid", Secure: true, WebDir: t.TempDir()}
	return server, server.Router()
}
func call(handler http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://supply.example.invalid")
	if token != "" {
		req.AddCookie(&http.Cookie{Name: "platform_session", Value: token})
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}
func TestTenantIsolationRoleAndOrigin(t *testing.T) {
	s, h := fixture(t)
	ctx := context.Background()
	config := s.Store.Vault.Seal("order:other", []byte(`{}`))
	_, e := s.Store.DB.Exec(ctx, "INSERT INTO orders(id,user_id,merchant_order_no,idempotency_key,request_hash,product_code,business,supply_minor,config_cipher) VALUES('other','two','other-order','other-idem','hash','gpt-plus','gpt',1000,$1)", config)
	if e != nil {
		t.Fatal(e)
	}
	if got := call(h, "GET", "/api/orders/other", "one-session", ""); got.Code != 404 {
		t.Fatalf("cross-channel order exposed: %d", got.Code)
	}
	if got := call(h, "GET", "/api/admin/channels", "one-session", ""); got.Code != 403 {
		t.Fatal("channel reached admin")
	}
	req := httptest.NewRequest("POST", "/api/keys", strings.NewReader(`{"label":"key"}`))
	req.AddCookie(&http.Cookie{Name: "platform_session", Value: "one-session"})
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatal("cross-origin write allowed")
	}
	_, _ = s.Store.DB.Exec(ctx, "UPDATE users SET status='banned' WHERE id='one'")
	if got := call(h, "GET", "/api/ledger", "one-session", ""); got.Code != 401 {
		t.Fatal("banned session accepted")
	}
}
func TestSignedAuthenticationReplayAndBan(t *testing.T) {
	s, h := fixture(t)
	secret := "test-secret"
	_, e := s.Store.DB.Exec(context.Background(), "INSERT INTO api_keys(id,user_id,label,secret_cipher) VALUES('test-key','one','test',$1)", s.Store.Vault.Seal("api-key:test-key", []byte(secret)))
	if e != nil {
		t.Fatal(e)
	}
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := "test-nonce-0123456789"
	signed := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/v1/balance", nil)
		req.Header.Set("X-Key-Id", "test-key")
		req.Header.Set("X-Timestamp", ts)
		req.Header.Set("X-Nonce", nonce)
		req.Header.Set("X-Signature", security.Signature(secret, "GET", "/v1/balance", url.Values{}, ts, nonce, "test-key", "", nil))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if r := signed(); r.Code != 200 {
		t.Fatalf("signature failed %d: %s", r.Code, r.Body.String())
	}
	if r := signed(); r.Code != 409 {
		t.Fatal("replayed nonce accepted")
	}
	_, _ = s.Store.DB.Exec(context.Background(), "UPDATE users SET status='banned' WHERE id='one'")
	if r := signed(); r.Code != 401 {
		t.Fatal("banned key accepted")
	}
}
func TestInvitationSingleUseAndDeletionGuard(t *testing.T) {
	s, h := fixture(t)
	r := call(h, "POST", "/api/admin/channels", "admin-session", `{"name":"新渠道","email":"new@example.com"}`)
	if r.Code != 200 {
		t.Fatalf("invite failed %s", r.Body.String())
	}
	var response struct {
		Data struct {
			ID string `json:"id"`
		}
	}
	_ = json.Unmarshal(r.Body.Bytes(), &response)
	var id, cipher string
	if e := s.Store.DB.QueryRow(context.Background(), "SELECT id,payload_cipher FROM outbox WHERE user_id=$1", response.Data.ID).Scan(&id, &cipher); e != nil {
		t.Fatal(e)
	}
	data, e := s.Store.Vault.Open("outbox:"+id, cipher)
	if e != nil {
		t.Fatal(e)
	}
	var payload struct {
		URL string `json:"url"`
	}
	_ = json.Unmarshal(data, &payload)
	token := strings.Split(payload.URL, "#")[1]
	body := `{"token":"` + token + `","password":"strong-test-password"}`
	if r = call(h, "POST", "/api/invitations/accept", "", body); r.Code != 200 {
		t.Fatalf("accept failed %s", r.Body.String())
	}
	if r = call(h, "POST", "/api/invitations/accept", "", body); r.Code != 400 {
		t.Fatal("invitation reused")
	}
	_, _ = s.Store.DB.Exec(context.Background(), "UPDATE wallets SET available_minor=1 WHERE user_id=$1", response.Data.ID)
	if r = call(h, "POST", "/api/admin/channels/"+response.Data.ID+"/status", "admin-session", `{"status":"deleted"}`); r.Code != 409 {
		t.Fatal("deleted funded channel")
	}
}
