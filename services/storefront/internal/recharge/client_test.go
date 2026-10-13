package recharge

import (
	"context"
	"errors"
	"gptibo/storefront/internal/fault"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type transport func(*http.Request) (*http.Response, error)

func TestSubmissionErrorDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name, body, header, code, requestID, message string
		status                                       int
		retry                                        bool
	}{
		{"rate limit", `{"error":{"code":"rate_limited","message":"PRIVATE_SESSION","request_id":"req_body","retryable":false}}`, "req_header", "rate_limited", "req_header", "请求较频繁", 429, true},
		{"credential rejected", `{"error":{"code":"session_invalid","message":"PRIVATE_SESSION","request_id":"req_body","retryable":false}}`, "", "session_invalid", "req_body", "登录凭据已失效", 400, false},
		{"unknown code", `{"error":{"code":"PRIVATE_SESSION","message":"PRIVATE_SESSION","request_id":"unsafe request id"}}`, "bad header", "request_rejected", "", "暂未完成请求", 409, false},
		{"invalid response", `PRIVATE_SESSION`, "req_header", "invalid_response", "req_header", "暂未完成请求", 502, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := NewClient("https://supplier.example.test/v1", "tenant", "key", "secret")
			calls := 0
			c.HTTP.Transport = transport(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: tc.status, Header: http.Header{"X-Request-Id": {tc.header}}, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})
			raw := `{"credential":"PRIVATE_SESSION"}`
			var result map[string]any
			err := c.Request(context.Background(), "/redemptions", &raw, "recharge:original", &result)
			var api *APIError
			if !errors.As(err, &api) || api.Code != tc.code || api.RequestID != tc.requestID || api.HTTPStatus != tc.status || api.Retryable != tc.retry {
				t.Fatalf("unexpected diagnostics: %#v", api)
			}
			if calls != 1 {
				t.Fatal("write request was automatically replayed")
			}
			if fault.Status(err) != 503 || !strings.Contains(fault.Message(err), tc.message) || strings.Contains(fault.Message(err), "PRIVATE") || strings.Contains(fault.Message(err), "req_") {
				t.Fatal("unsafe or missing public error", fault.Message(err))
			}
		})
	}
}

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestCanonicalSigningAndNoRedirect(t *testing.T) {
	q := url.Values{"a": {"z", "a b"}, "a!": {"x"}, "a%": {"x"}, "é": {"*"}}
	if got := Canonical(q); got != "%C3%A9=%2A&a=a%20b&a=z&a%21=x&a%25=x" {
		t.Fatal("canonical query incompatible", got)
	}
	c, e := NewClient("https://supplier.example.test/v1", "tenant", "key", "secret")
	if e != nil {
		t.Fatal(e)
	}
	c.HTTP.Transport = transport(func(r *http.Request) (*http.Response, error) {
		raw, _ := io.ReadAll(r.Body)
		if r.Header.Get("X-Signature") != Signature("secret", r.Method, r.URL, r.Header.Get("X-Timestamp"), r.Header.Get("X-Nonce"), "key", r.Header.Get("Idempotency-Key"), string(raw)) {
			t.Fatal("signature mismatch")
		}
		return &http.Response{StatusCode: 302, Body: io.NopCloser(strings.NewReader(`{"data":{}}`)), Header: http.Header{"Location": []string{"https://elsewhere.example.test"}}}, nil
	})
	raw := `{"fixture":true}`
	var result map[string]any
	if e = c.Request(context.Background(), "/orders", &raw, "order:fixture", &result); e == nil {
		t.Fatal("redirect accepted")
	}
	for _, value := range []string{"https://other.example.test/payments/id?token=abcdefghijklmnop", "https://supplier.example.test/payments/id?token=abcdefghijklmnop&token=x", "https://supplier.example.test/payments/wrong?token=abcdefghijklmnop"} {
		if _, e = c.PaymentLink(&value, "id"); e == nil {
			t.Fatal("cashier mismatch accepted")
		}
	}
	for _, value := range []string{"1", "1.001", "-1.00", "01.00", "1e2", "NaN"} {
		if _, e = Money(value); e == nil {
			t.Fatal("invalid money accepted")
		}
	}
}
