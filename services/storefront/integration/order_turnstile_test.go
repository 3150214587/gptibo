package integration_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"gptibo/storefront/internal/httpapi"
	"gptibo/storefront/internal/security"
	"gptibo/storefront/internal/turnstile"
)

type turnstileTransport func(*http.Request) (*http.Response, error)

func (f turnstileTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestOrderTurnstileEnforcementAndReplay(t *testing.T) {
	owner, app, a, _, _ := identityFixture(t)
	execSQL(t, owner, `INSERT INTO catalog_plans(id,product_id,name,price_cents,enabled) VALUES('plus','chatgpt','Plus',100,true)`)
	verification, err := turnstile.New([]byte(`[{"sitekey":"public-key","secret":"private-secret","hosts":["storefront.example.test"]}]`))
	if err != nil {
		t.Fatal(err)
	}
	old := http.DefaultTransport
	var lock sync.Mutex
	consumed := map[string]bool{}
	calls := 0
	http.DefaultTransport = turnstileTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://challenges.cloudflare.com/turnstile/v0/siteverify" {
			return nil, fmt.Errorf("unexpected request")
		}
		r.ParseForm()
		token := r.Form.Get("response")
		lock.Lock()
		defer lock.Unlock()
		calls++
		ok := token == "valid-token" && !consumed[token]
		consumed[token] = true
		body := fmt.Sprintf(`{"success":%t,"hostname":"storefront.example.test","action":"order_create","challenge_ts":%q}`, ok, time.Now().UTC().Format(time.RFC3339))
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = old })
	router := httpapi.Router(app, "fixture", slog.New(slog.NewTextHandler(io.Discard, nil)), httpapi.Options{Origin: testOrigin, Auth: a, OrderTesting: true, Turnstile: verification})
	buyer := &http.Cookie{Name: "__Host-sf_buyer", Value: security.Token()}
	config := object(t, request(router, "GET", "/api/orders/verification", nil, buyer), 200)
	if config["siteKey"] != "public-key" || strings.Contains(fmt.Sprint(config), "private-secret") {
		t.Fatal("public config incorrect")
	}
	in := map[string]any{"plan": "plus", "email": "buyer@qq.com", "key": security.ID(), "expectedPrice": 100}
	object(t, request(router, "POST", "/api/orders", in, buyer), 400)
	if calls != 0 {
		t.Fatal("missing token reached provider")
	}
	in["turnstileToken"] = "forged-token"
	object(t, request(router, "POST", "/api/orders", in, buyer), 400)
	var count int
	owner.DB.QueryRow(context.Background(), `SELECT count(*) FROM orders`).Scan(&count)
	if count != 0 {
		t.Fatal("unverified order persisted")
	}
	in["turnstileToken"] = "valid-token"
	created := object(t, request(router, "POST", "/api/orders", in, buyer), 201)["order"].(map[string]any)
	delete(in, "turnstileToken")
	replay := object(t, request(router, "POST", "/api/orders", in, buyer), 200)["order"].(map[string]any)
	if replay["id"] != created["id"] || calls != 2 {
		t.Fatal("idempotent replay consumed another token")
	}
	object(t, request(router, "GET", "/api/orders/"+created["id"].(string), nil, buyer), 200)
	in["key"] = security.ID()
	in["turnstileToken"] = "valid-token"
	object(t, request(router, "POST", "/api/orders", in, buyer), 400)
	owner.DB.QueryRow(context.Background(), `SELECT count(*) FROM orders`).Scan(&count)
	if count != 1 {
		t.Fatal("token reuse created another order")
	}
}
