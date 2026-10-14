package providers

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"gptibo/platform/internal/commerce"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func useXTestServer(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	ts := httptest.NewTLSServer(handler)
	roots := x509.NewCertPool()
	roots.AddCert(ts.Certificate())
	old := http.DefaultTransport
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "example.com"}, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, ts.Listener.Addr().String())
	}}
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = old; transport.CloseIdleConnections(); ts.Close() })
}

func TestXPreflightChecksLiveQuoteBeforeAcceptingPayment(t *testing.T) {
	e, _, s := orderFixture(t)
	ctx := context.Background()
	s.Provider = commerce.ProviderConfig{APIBase: "https://cards.example.invalid/openapi/v1", Environment: "production", XBearer: "Bearer synthetic"}
	data, _ := json.Marshal(s.Provider)
	_, err := e.Store.DB.Exec(ctx, "UPDATE products SET enabled=true, config='{\"months\":3,\"amount_minor\":30000,\"currency\":\"bdt\",\"stripe_product\":\"prod_fixture3\",\"stripe_merchant\":\"acct_fixture\"}' WHERE code='x-3m'")
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.Store.DB.Exec(ctx, "INSERT INTO x_accounts(id,name,payload_cipher,daily_limit) VALUES('account','Test',$1,10)", e.Store.Vault.Seal("x-account:account", []byte(`{"auth_token":"synthetic","ct0":"synthetic"}`)))
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.Store.DB.Exec(ctx, "INSERT INTO settings(id,payload_cipher) VALUES('provider',$1)", e.Store.Vault.Seal("setting:provider", data))
	if err != nil {
		t.Fatal(err)
	}
	changed := true
	eligibilityCalls := 0
	useXTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/i/api/graphql/Se1Bp6zcNnuXYXRecV2qLA/useSubscriptionProductDetailsByRestIdQuery" {
			amount := 300000000
			if changed {
				amount--
			}
			_ = json.NewEncoder(w).Encode(Object{"data": Object{"web_subscription_product_details_by_rest_id": Object{"rest_id": "prod_fixture3", "prices": []any{Object{"amount_local_micro": amount, "price_type": "OneTime", "currency_code": "BDT"}}}}})
		} else {
			eligibilityCalls++
			_, _ = w.Write([]byte(`{"data":{"user":{"result":{"rest_id":"12345","core":{"screen_name":"receiver"},"premium_gifting_eligible":true}}}}`))
		}
	})
	_, err = e.Preflight(ctx, "x-3m", "receiver", commerce.Credential{})
	var known *commerce.Error
	if !errors.As(err, &known) || known.Code != "preflight_failed" || eligibilityCalls != 0 {
		t.Fatal("changed live price did not stop preflight")
	}
	changed = false
	result, err := e.Preflight(ctx, "x-3m", "receiver", commerce.Credential{})
	if err != nil || result.(Object)["recipient_id"] != "12345" || eligibilityCalls != 1 {
		t.Fatalf("valid identity/quote preflight failed: %v", err)
	}
}

func TestXConfirmedIdentityStopsBeforeCreatingCheckout(t *testing.T) {
	e, o, s := orderFixture(t)
	s.Business = "x"
	s.Provider.Environment = "production"
	s.Provider.StripeKey = "pk_live_synthetic"
	s.Provider.XBearer = "Bearer synthetic"
	s.ConfirmedRecipientID = "12345"
	s.ProductConfig = Object{"months": float64(3), "amount_minor": float64(30000), "currency": "bdt", "stripe_merchant": "acct_fixture"}
	o.Recipient = "receiver"
	old := http.DefaultTransport
	defer func() { http.DefaultTransport = old }()
	calls := 0
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "GET" || r.URL.Path != "/i/api/graphql/kn8hCE6bHstQV2MtfYDTKg/PremiumGiftingQuery" {
			t.Error("unexpected side effect during identity check")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"user":{"result":{"rest_id":"999","core":{"screen_name":"receiver"},"premium_gifting_eligible":true}}}}`))
	}))
	defer ts.Close()
	roots := x509.NewCertPool()
	roots.AddCert(ts.Certificate())
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "example.com"}, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, ts.Listener.Addr().String())
	}}
	defer transport.CloseIdleConnections()
	http.DefaultTransport = transport
	j := Job{Stage: "preflight", AccountID: "account", Account: Object{"auth_token": "synthetic", "ct0": "synthetic"}}
	_, err := e.x(context.Background(), o, s, &j, func() error { t.Fatal("identity mismatch checkpointed checkout"); return nil })
	var upstream *UpstreamError
	if !errors.As(err, &upstream) || upstream.Code != "recipient_changed" || !upstream.Definitive || calls != 1 {
		t.Fatalf("identity was not guarded: %v (%d calls)", err, calls)
	}
}

func TestProductionNestedCardLimitsRequireAnActualCap(t *testing.T) {
	for _, tc := range []struct {
		row  Object
		want bool
	}{
		{Object{"spend_limits_supported": true, "limit": Object{"single_limit": float64(20)}}, true},
		{Object{"spend_limits_supported": true, "limit": Object{"single_limit": float64(21)}}, false},
		{Object{"spend_limits_supported": true, "limit": Object{"single_limit": float64(0)}}, false},
		{Object{"spend_limits_supported": true, "limit": Object{}}, false},
		{Object{"limit": Object{"single_limit": float64(20)}}, false},
		{Object{"transaction_limit": float64(0), "transaction_limit_type": "limited", "spend_limits_supported": true, "limit": Object{"single_limit": float64(20)}}, false},
	} {
		if got := cardLimitMatches(tc.row, 2000); got != tc.want {
			t.Fatalf("card spending cap accepted=%v want=%v", got, tc.want)
		}
	}
}
