package mailer

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestResendRequestAndResponseValidation(t *testing.T) {
	r, e := NewResend("fixture-key", "Dev <dev@example.test>")
	if e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		status int
		body   string
		ok     bool
	}{{200, `{"id":"mail-fixture"}`, true}, {200, `{}`, false}, {429, `{"message":"limited"}`, false}, {200, strings.Repeat("x", 16385), false}} {
		r.client.Transport = roundTrip(func(req *http.Request) (*http.Response, error) {
			if req.URL.String() != "https://api.resend.com/emails" || req.Method != "POST" || req.Header.Get("Idempotency-Key") != "storefront-auth/test-id" {
				t.Fatal("invalid mail request")
			}
			return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body)), Header: http.Header{}}, nil
		})
		e = r.SendCode(context.Background(), "admin", "admin@example.test", "123456", "test-id")
		if (e == nil) != tc.ok {
			t.Fatal("wrong mail response interpretation")
		}
	}
}

func TestTemplatesEscapeValuesAndUseDevelopmentLinks(t *testing.T) {
	m, e := Template("admin-login", "http://127.0.0.1:8093", map[string]string{"VERIFICATION_CODE": `<script>&"`})
	if e != nil || strings.Contains(m.HTML, `<script>`) || !strings.Contains(m.Text, `<script>&"`) || strings.Contains(m.HTML, "https://shop.example.invalid") {
		t.Fatal("unsafe or incorrect template expansion", e)
	}
	if _, e = Template("admin-login", "http://127.0.0.1:8093", nil); e == nil {
		t.Fatal("missing placeholder accepted")
	}
	m, e = Template("partner-welcome", "http://127.0.0.1:8093", nil)
	if e != nil || !strings.Contains(m.HTML, "http://127.0.0.1:8093/join") {
		t.Fatal("invitation points to wrong environment", e)
	}
}

func TestTemplateSubstitutionDoesNotReinterpretUserMarkers(t *testing.T) {
	m, e := Template("delivery-cdk", "http://127.0.0.1:8093", map[string]string{"DELIVERY_ORDER": "fixture", "DELIVERY_VALUE": "<CDK>", "DELIVERY_INSTRUCTIONS": "line 1\n__RECHARGE_DELIVERY_VALUE__"})
	if e != nil || strings.Contains(m.HTML, "<CDK>") || !strings.Contains(m.HTML, "line 1<br />__RECHARGE_DELIVERY_VALUE__") {
		t.Fatal("user value treated as template", e)
	}
}
