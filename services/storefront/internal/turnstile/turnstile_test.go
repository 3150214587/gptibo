package turnstile

import (
	"context"
	"encoding/json"
	"gptibo/storefront/internal/fault"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestVerify(t *testing.T) {
	for _, tc := range []struct {
		name, token, host, action string
		success                   bool
		age                       time.Duration
		status, want              int
	}{
		{"valid", "valid-token", "shop.test", Action, true, time.Second, 200, 0},
		{"missing", "", "shop.test", Action, true, time.Second, 200, 400},
		{"oversized", strings.Repeat("x", 2049), "shop.test", Action, true, time.Second, 200, 400},
		{"forged-or-replayed", "bad", "shop.test", Action, false, time.Second, 200, 400},
		{"other-host", "token", "other.test", Action, true, time.Second, 200, 400},
		{"other-action", "token", "shop.test", "login", true, time.Second, 200, 400},
		{"expired", "token", "shop.test", Action, true, 6 * time.Minute, 200, 400},
		{"future", "token", "shop.test", Action, true, -2 * time.Minute, 200, 400},
		{"provider-error", "token", "shop.test", Action, true, time.Second, 503, 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "POST" || r.ParseForm() != nil || r.Form.Get("secret") != "secret" || r.Form.Get("response") != tc.token {
					t.Error("invalid verification request")
				}
				w.WriteHeader(tc.status)
				json.NewEncoder(w).Encode(map[string]any{"success": tc.success, "hostname": tc.host, "action": tc.action, "challenge_ts": time.Now().Add(-tc.age)})
			}))
			defer srv.Close()
			s, err := New([]byte(`[{"sitekey":"key","secret":"secret","hosts":["shop.test"]}]`))
			if err != nil {
				t.Fatal(err)
			}
			s.endpoint = srv.URL
			err = s.Verify(context.Background(), "shop.test", tc.token)
			if tc.want == 0 {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || fault.Status(err) != tc.want {
				t.Fatalf("expected %d, got %v", tc.want, err)
			}
			if (tc.token == "" || len(tc.token) > 2048) && calls != 0 {
				t.Fatal("invalid token reached provider")
			}
			if s.Verify(context.Background(), "unconfigured.test", "token") == nil {
				t.Fatal("unknown host allowed")
			}
		})
	}
}

func TestUnavailableFailsClosed(t *testing.T) {
	for _, body := range []string{`not-json`, `{}`, `{"success":true,"hostname":"shop.test","action":"order_create"}`} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
		s, _ := New([]byte(`[{"sitekey":"key","secret":"secret","hosts":["shop.test"]}]`))
		s.endpoint = srv.URL
		if s.Verify(context.Background(), "shop.test", "token") == nil {
			t.Fatal("malformed result allowed")
		}
		srv.Close()
		if s.Verify(context.Background(), "shop.test", "token") == nil {
			t.Fatal("outage allowed")
		}
	}
}

func TestConfig(t *testing.T) {
	for _, raw := range []string{`[]`, `{}`, `[{"sitekey":"key","hosts":["shop.test"]}]`, `[{"sitekey":"key","secret":"secret","hosts":["*.test"]}]`, `[{"sitekey":"key","secret":"secret","hosts":["shop.test","shop.test"]}]`} {
		if _, err := New([]byte(raw)); err == nil {
			t.Fatal("invalid config accepted")
		}
	}
}
