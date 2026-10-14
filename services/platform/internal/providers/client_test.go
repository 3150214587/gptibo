package providers

import (
	"context"
	"encoding/json"
	"errors"
	"gptibo/platform/internal/commerce"
	"io"
	"net/http"
	"strings"
	"testing"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(body string) *http.Response {
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}
func TestLostGPTSubmissionOnlyQueriesOriginalOrder(t *testing.T) {
	old := client
	defer func() { client = old }()
	calls := 0
	client = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != "GET" {
			t.Fatal("resubmitted unknown request")
		}
		if strings.HasSuffix(r.URL.Path, "/orders") {
			return response(`{"code":0,"data":{"list":[{"id":77,"client_request_id":"original-order"}],"total":1}}`), nil
		}
		if r.URL.Path != "/openapi/v1/gpt-direct/orders/77" {
			t.Fatalf("wrong lookup: %s", r.URL.Path)
		}
		return response(`{"code":0,"data":{"id":77,"card_id":42,"status":"completed","stage":"done"}}`), nil
	})}
	s := commerce.Snapshot{Provider: commerce.ProviderConfig{APIBase: "https://cards.example.invalid/openapi/v1", Environment: "sandbox", APIKey: "sk_synthetic"}}
	result, e := gptQuery(context.Background(), s, Job{Stage: "submitting", CardID: 42}, "original-order")
	if e != nil || result.Status != "succeeded" || calls != 2 {
		t.Fatalf("query failed %+v %v", result, e)
	}
}
func TestMismatchedCardNeverSucceeds(t *testing.T) {
	old := client
	defer func() { client = old }()
	client = &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		return response(`{"code":0,"data":{"id":"order-1","card_id":999,"status":"completed"}}`), nil
	})}
	_, e := gptQuery(context.Background(), commerce.Snapshot{}, Job{UpstreamRef: "order-1", CardID: 42}, "request")
	if e == nil {
		t.Fatal("mismatched card succeeded")
	}
}
func TestUnknownProviderErrorRemainsUnknown(t *testing.T) {
	old := client
	defer func() { client = old }()
	client = &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 400, Body: io.NopCloser(strings.NewReader(`{"code":1,"error_code":"NEW_ERROR"}`))}, nil
	})}
	_, e := cardAPI(context.Background(), commerce.ProviderConfig{APIBase: "https://cards.example.invalid/openapi/v1"}, "POST", "/gpt-direct/orders", Object{}, "original")
	var err *UpstreamError
	if !errors.As(e, &err) || err.Definitive {
		t.Fatal("unknown provider error treated as definitive")
	}
}

func TestProviderErrorCannotLeakCredentials(t *testing.T) {
	old := client
	defer func() { client = old }()
	client = &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 400, Body: io.NopCloser(strings.NewReader(`{"code":1,"error_code":"secret: token value"}`))}, nil
	})}
	_, err := cardAPI(context.Background(), commerce.ProviderConfig{APIBase: "https://cards.example.invalid/openapi/v1"}, "POST", "/gpt-direct/orders", Object{}, "original")
	var upstream *UpstreamError
	if !errors.As(err, &upstream) || upstream.Code != "upstream_result_unknown" {
		t.Fatal("raw provider error exposed")
	}
}
func TestXCheckoutIdentityAndAmountGuards(t *testing.T) {
	s := commerce.Snapshot{ProductConfig: Object{"months": 3.0, "amount_minor": 30000.0, "currency": "bdt", "stripe_merchant": "acct_fixture", "stripe_product": "prod_test"}}
	o := commerce.Order{Recipient: "tester"}
	j := Job{Session: "cs_live_test"}
	var page Object
	_ = json.Unmarshal([]byte(`{"session_id":"cs_live_test","account_settings":{"account_id":"acct_fixture"},"livemode":true,"mode":"payment","currency":"bdt","success_url":"https://x.com/tester/gift-premium/success","cancel_url":"https://x.com/tester/gift-premium","total_summary":{"total":30000,"subtotal":30000,"due":30000},"line_item_group":{"currency":"bdt","total":30000,"subtotal":30000,"due":30000,"line_items":[{"name":"Premium Gift - 3 months","quantity":1,"total":30000,"subtotal":30000,"price":{"currency":"bdt","type":"one_time","unit_amount":30000,"product":{"id":"prod_test","name":"Premium Gift - 3 months","livemode":true}}}]},"status":"open","payment_status":"unpaid","payment_intent":null,"init_checksum":"test-checksum"}`), &page)
	if e := guardPage(page, o, s, &j); e != nil {
		t.Fatal(e)
	}
	obj(page["total_summary"])["total"] = 30001.0
	if e := guardPage(page, o, s, &j); e == nil {
		t.Fatal("wrong amount accepted")
	}
}

func TestInvalidAmountsNeverProveZeroCharge(t *testing.T) {
	for _, v := range []any{nil, "", "invalid", "NaN", "Infinity", true, -1.0, 1.001} {
		if _, ok := usdMinor(v); ok {
			t.Fatalf("invalid USD amount accepted: %v", v)
		}
	}
	old := client
	defer func() { client = old }()
	for _, amount := range []string{"null", `"invalid"`, `"NaN"`, `false`, `-1`, `1`, `0.01`, `0`, `"0"`} {
		t.Run(amount, func(t *testing.T) {
			client = &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
				return response(`{"code":0,"data":{"id":"original","status":"failed","final_amount_minor":` + amount + `}}`), nil
			})}
			result, err := gptQuery(context.Background(), commerce.Snapshot{}, Job{UpstreamRef: "original"}, "request")
			expected := "unknown"
			if amount == "0" || amount == `"0"` {
				expected = "failed"
			}
			if err != nil || result.Status != expected {
				t.Fatal(result, err)
			}
		})
	}
}

func TestAccountPreflightDoesNotReuseGlobalIdempotencyKey(t *testing.T) {
	old := client
	defer func() { client = old }()
	calls := 0
	client = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/plans") {
			return response(`{"code":0,"data":{"registry":[{"product":"gpt","key":"plan","purchasable":true}],"plans":{"plan":{"enabled":true}}}}`), nil
		}
		calls++
		if r.Header.Get("Idempotency-Key") != "" {
			t.Fatal("different accounts share a preflight idempotency key")
		}
		return response(`{"code":0,"data":{"email":"customer@test.invalid","preflight_token":"fresh-token"}}`), nil
	})}
	s := commerce.Snapshot{Plan: "plan", Provider: commerce.ProviderConfig{APIBase: "https://cards.example.invalid/openapi/v1", PaymentCountry: "PH", PaymentCurrency: "PHP"}}
	for _, token := range []string{"first-account", "second-account"} {
		if _, err := gptPreflight(context.Background(), s, commerce.Credential{Mode: "access_token", AccessToken: token}); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 2 {
		t.Fatal("preflight not refreshed")
	}
}
func TestXLiveQuoteMustMatchExactConfiguredProductPriceAndType(t *testing.T) {
	cfg := Object{"stripe_merchant": "acct_fixture", "stripe_product": "prod_test", "currency": "bdt", "amount_minor": 30000.0}
	data := Object{"web_subscription_product_details_by_rest_id": Object{"rest_id": "prod_test", "prices": []any{Object{"currency_code": "BDT", "amount_local_micro": 300000000.0, "price_type": "OneTime"}}}}
	if err := guardXQuote(data, cfg); err != nil {
		t.Fatal(err)
	}
	p := obj(obj(data["web_subscription_product_details_by_rest_id"])["prices"].([]any)[0])
	p["amount_local_micro"] = 300010000.0
	if guardXQuote(data, cfg) == nil {
		t.Fatal("changed upstream price allowed")
	}
	p["amount_local_micro"] = 300000000.0
	p["price_type"] = "Recurring"
	if guardXQuote(data, cfg) == nil {
		t.Fatal("recurring product allowed for gift")
	}
}
