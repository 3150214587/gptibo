package httpapi

import (
	"context"
	"fmt"
	"gptibo/platform/internal/security"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func loginFrom(h http.Handler, email, ip, forwarded string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/api/login", strings.NewReader(fmt.Sprintf(`{"email":%q,"password":"wrong-password"}`, email)))
	req.RemoteAddr = ip + ":1234"
	req.Header.Set("Origin", "https://supply.example.invalid")
	req.Header.Set("X-Forwarded-For", forwarded)
	req.Header.Set("X-Real-IP", forwarded)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestLoginLimitsAcrossAddressesAndAccounts(t *testing.T) {
	t.Run("account across IPs", func(t *testing.T) {
		_, h := fixture(t)
		for i := 1; i <= 11; i++ {
			got := loginFrom(h, "one@example.com", fmt.Sprintf("198.51.100.%d", i), "")
			want := 401
			if i == 11 {
				want = 429
			}
			if got.Code != want {
				t.Fatalf("attempt %d: %d, want %d", i, got.Code, want)
			}
		}
	})
	t.Run("IP across emails and spoofed forwarding", func(t *testing.T) {
		_, h := fixture(t)
		for i := 1; i <= 61; i++ {
			got := loginFrom(h, fmt.Sprintf("missing-%d@example.com", i), "198.51.100.1", fmt.Sprintf("203.0.113.%d", i))
			want := 401
			if i == 61 {
				want = 429
			}
			if got.Code != want {
				t.Fatalf("attempt %d: %d, want %d", i, got.Code, want)
			}
		}
	})
	t.Run("trusted nginx distinguishes clients", func(t *testing.T) {
		s, _ := fixture(t)
		s.TrustedProxies = []string{"127.0.0.1", "::1"}
		h := s.Router()
		for i := 1; i <= 65; i++ {
			got := loginFrom(h, fmt.Sprintf("missing-%d@example.com", i), "127.0.0.1", fmt.Sprintf("198.51.100.%d", i))
			if got.Code != 401 {
				t.Fatalf("independent client %d was limited: %d", i, got.Code)
			}
		}
	})
}

func TestSignedExtremeTimestampCannotOverflowWindow(t *testing.T) {
	s, h := fixture(t)
	secret := "test-secret"
	_, err := s.Store.DB.Exec(context.Background(), "INSERT INTO api_keys(id,user_id,label,secret_cipher) VALUES('overflow-key','one','test',$1)", s.Store.Vault.Seal("api-key:overflow-key", []byte(secret)))
	if err != nil {
		t.Fatal(err)
	}
	for i, seconds := range []int64{time.Now().Unix() + (-1 << 63), -1 << 63, 1<<63 - 1} {
		ts := strconv.FormatInt(seconds, 10)
		nonce := fmt.Sprintf("overflow-nonce-%016d", i)
		req := httptest.NewRequest("GET", "/v1/balance", nil)
		req.Header.Set("X-Key-Id", "overflow-key")
		req.Header.Set("X-Timestamp", ts)
		req.Header.Set("X-Nonce", nonce)
		req.Header.Set("X-Signature", security.Signature(secret, "GET", "/v1/balance", url.Values{}, ts, nonce, "overflow-key", "", nil))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 401 || !strings.Contains(rec.Body.String(), "invalid_timestamp") {
			t.Fatal("extreme timestamp accepted", rec.Code, rec.Body.String())
		}
	}
}

func TestBrowserSecurityHeaders(t *testing.T) {
	_, h := fixture(t)
	r := call(h, "GET", "/api/session", "", "")
	if r.Header().Get("X-Frame-Options") != "DENY" || !strings.Contains(r.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") || r.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("browser protections absent", r.Header())
	}
}
