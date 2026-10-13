package access

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"github.com/golang-jwt/jwt/v5"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAssertionValidationAndKeyCache(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kid": "key1", "kty": "RSA", "alg": "RS256", "use": "sig", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": "AQAB"}}})
	}))
	defer server.Close()
	s, err := New("https://team.cloudflareaccess.com", strings.Repeat("a", 64), "admin@example.test")
	if err != nil {
		t.Fatal(err)
	}
	s.issuer = server.URL
	base := func() *claims {
		return &claims{RegisteredClaims: jwt.RegisteredClaims{Issuer: server.URL, Audience: jwt.ClaimStrings{s.audience}, Subject: "user", ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)), IssuedAt: jwt.NewNumericDate(time.Now().Add(-time.Minute))}, Email: s.email, Type: "app"}
	}
	sign := func(c *claims, kid string) string {
		token := jwt.NewWithClaims(jwt.SigningMethodRS256, c)
		token.Header["kid"] = kid
		raw, err := token.SignedString(key)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	for i := 0; i < 2; i++ {
		if err = s.Verify(context.Background(), sign(base(), "key1")); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatal("keys not cached")
	}
	for _, mutate := range []func(*claims){func(c *claims) { c.Audience = []string{"wrong"} }, func(c *claims) { c.Issuer = "https://attacker.test" }, func(c *claims) { c.Email = "other@example.test" }, func(c *claims) { c.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-time.Minute)) }, func(c *claims) { c.ExpiresAt = nil }, func(c *claims) { c.IssuedAt = jwt.NewNumericDate(time.Now().Add(time.Hour)) }, func(c *claims) { c.Type = "service" }, func(c *claims) { c.Subject = "" }} {
		c := base()
		mutate(c)
		if s.Verify(context.Background(), sign(c, "key1")) == nil {
			t.Fatal("invalid claims accepted")
		}
	}
	for _, raw := range []string{"", "forged", sign(base(), "unknown")} {
		if s.Verify(context.Background(), raw) == nil {
			t.Fatal("forged assertion accepted")
		}
	}
	if calls != 1 {
		t.Fatal("unknown kid caused a fetch flood")
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, base())
	token.Header["kid"] = "key1"
	raw, _ := token.SignedString([]byte("wrong"))
	if s.Verify(context.Background(), raw) == nil {
		t.Fatal("algorithm confusion")
	}
	s.fetched = time.Now().Add(-2 * time.Hour)
	s.attempt = time.Now().Add(-2 * time.Minute)
	if err = s.Verify(context.Background(), sign(base(), "key1")); err != nil || calls != 2 {
		t.Fatal("expired keys did not refresh", err)
	}
}
func TestAccessRejectsUnsafeConfiguration(t *testing.T) {
	for _, issuer := range []string{"http://team.cloudflareaccess.com", "https://127.0.0.1", "https://team.cloudflareaccess.com/", "https://team.cloudflareaccess.com.evil.test"} {
		if _, err := New(issuer, strings.Repeat("a", 64), "admin@example.test"); err == nil {
			t.Fatal("unsafe issuer", issuer)
		}
	}
}
