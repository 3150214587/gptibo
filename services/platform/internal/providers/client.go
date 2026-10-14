package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"gptibo/platform/internal/commerce"
	"gptibo/platform/internal/security"
	"io"
	"math"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Object = map[string]any

func obj(v any) Object {
	if o, ok := v.(map[string]any); ok {
		return o
	}
	return Object{}
}
func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	if n, ok := v.(float64); ok {
		if math.IsNaN(n) || math.IsInf(n, 0) || math.Trunc(n) != n || math.Abs(n) > 9007199254740991 {
			return ""
		}
		return strconv.FormatInt(int64(n), 10)
	}
	return ""
}
func number(v any) float64 {
	if n, ok := v.(float64); ok {
		return n
	}
	n, _ := strconv.ParseFloat(str(v), 64)
	return n
}
func usdMinor(v any) (int64, bool) {
	n, valid := exactNumber(v)
	if !valid {
		return 0, false
	}
	if math.IsNaN(n) || math.IsInf(n, 0) || n < 0 || n > 100000000 {
		return 0, false
	}
	scaled := n * 100
	rounded := math.Round(scaled)
	return int64(rounded), math.Abs(scaled-rounded) < 0.000001
}

// Provider balances retain fractional cents (fees and conversions). They are
// available funds, not a charge amount: discard fractional cents conservatively.
// Keep usdMinor strict for requested amounts, prices and configured limits.
func usdBalanceMinor(v any) (int64, bool) {
	n, valid := exactNumber(v)
	if !valid || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 || n > 100000000 {
		return 0, false
	}
	return int64(math.Floor(n * 100)), true
}
func exactNumber(v any) (float64, bool) {
	var n float64
	switch value := v.(type) {
	case float64:
		n = value
	case string:
		if value == "" || strings.TrimSpace(value) != value {
			return 0, false
		}
		var err error
		n, err = strconv.ParseFloat(value, 64)
		if err != nil {
			return 0, false
		}
	default:
		return 0, false
	}
	return n, !math.IsNaN(n) && !math.IsInf(n, 0)
}
func matchesID(v any, expected int64) bool {
	if expected <= 0 {
		return false
	}
	if value, ok := v.(string); ok {
		if value == "" || strings.Trim(value, "0123456789") != "" {
			return false
		}
		id, err := strconv.ParseInt(value, 10, 64)
		return err == nil && id == expected
	}
	n, ok := exactNumber(v)
	return ok && n > 0 && n <= 9007199254740991 && math.Trunc(n) == n && n == float64(expected)
}
func accountEmail(v any) string {
	raw := strings.ToLower(strings.TrimSpace(str(v)))
	a, err := mail.ParseAddress(raw)
	if err != nil || a.Address != raw || len(raw) > 254 {
		return ""
	}
	return raw
}
func truth(v any) bool { b, _ := v.(bool); return b }

var cardTransport = &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, errors.New("invalid provider address")
	}
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil || len(ips) == 0 {
		return nil, errors.New("provider DNS unavailable")
	}
	for _, ip := range ips {
		if !security.PublicIP(ip) {
			return nil, errors.New("non-public provider address")
		}
	}
	return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
}, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 25 * time.Second}
var client = &http.Client{Transport: cardTransport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

type UpstreamError struct {
	Code       string
	Definitive bool
}

func (e *UpstreamError) Error() string { return e.Code }
func readJSON(ctx context.Context, c *http.Client, method, target string, headers map[string]string, body []byte) (Object, int, error) {
	req, e := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if e != nil {
		return nil, 0, e
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, e := c.Do(req)
	if e != nil {
		return nil, 0, e
	}
	defer resp.Body.Close()
	b, e := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024+1))
	if e != nil || len(b) > 2*1024*1024 {
		return nil, resp.StatusCode, errors.New("invalid response size")
	}
	var o Object
	if e = json.Unmarshal(b, &o); e != nil {
		return nil, resp.StatusCode, e
	}
	return o, resp.StatusCode, nil
}
func apiBase(p commerce.ProviderConfig) string {
	if p.APIBase != "" {
		return p.APIBase
	}
	if p.Environment == "sandbox" {
		return "https://sandbox.cards.example.invalid/openapi/v1"
	}
	return "https://cards.example.invalid/openapi/v1"
}
func cardAPI(ctx context.Context, p commerce.ProviderConfig, method, path string, body Object, reference string) (Object, error) {
	if err := commerce.ValidateCardBase(apiBase(p)); err != nil {
		return nil, &UpstreamError{"provider_configuration_invalid", false}
	}
	headers := map[string]string{"X-API-Key": p.APIKey, "Accept": "application/json"}
	if p.AppID != "" {
		headers["X-App-Id"] = p.AppID
	}
	if reference != "" {
		headers["Idempotency-Key"] = reference
	}
	var b []byte
	if body != nil {
		b, _ = json.Marshal(body)
		headers["Content-Type"] = "application/json"
		if reference != "" {
			headers["Idempotency-Key"] = reference
		}
	}
	o, status, e := readJSON(ctx, client, method, apiBase(p)+path, headers, b)
	if e != nil {
		return nil, &UpstreamError{"upstream_unavailable", false}
	}
	code, hasCode := o["code"].(float64)
	if status >= 200 && status < 300 && hasCode && code == 0 {
		o["_http_status"] = status
		return o, nil
	}
	errorCode := str(o["error_code"])
	definitive := false
	switch errorCode {
	case "GPT_SESSION_INVALID", "SESSION_REQUIRED", "MAILBOX_LOGIN_FAILED", "PRECHECK_REJECTED", "GPT_PLAN_ALREADY_ACTIVE", "GPT_CREDIT_REQUIRES_SUBSCRIPTION", "DIRECT_PRODUCT_DISABLED", "GPT_CREDIT_DISABLED", "ACCOUNT_UNAVAILABLE", "INSUFFICIENT_BALANCE", "insufficient_balance", "RECHARGE_REQUIRED", "GPT_DIRECT_ORDER_REJECTED":
		definitive = true
	case "GPT_DIRECT_UNAVAILABLE", "GPT_DIRECT_PAUSED", "GPT_PRICE_UNCONFIRMED", "channel_unavailable":
	default:
		errorCode = "upstream_result_unknown"
	}
	if errorCode == "" {
		errorCode = "upstream_result_unknown"
	}
	return nil, &UpstreamError{errorCode, definitive}
}
func plans(ctx context.Context, s commerce.Snapshot) (Object, error) {
	data, registry, e := readGPTPlans(ctx, s.Provider)
	if e != nil {
		return nil, e
	}
	for _, row := range registry {
		if row.Code == s.Plan && row.Enabled && row.Purchasable {
			return data, nil
		}
	}
	return nil, &UpstreamError{"product_unavailable", true}
}
func gptPreflight(ctx context.Context, s commerce.Snapshot, c commerce.Credential) (Object, error) {
	if _, e := plans(ctx, s); e != nil {
		return nil, e
	}
	if s.Provider.PaymentCountry == "" || s.Provider.PaymentCurrency == "" {
		return nil, &UpstreamError{"payment_region_missing", true}
	}
	o, e := cardAPI(ctx, s.Provider, "POST", "/gpt-direct/preflight", Object{"product": "gpt", "credential": c, "payment_country": s.Provider.PaymentCountry, "payment_currency": s.Provider.PaymentCurrency}, "")
	if e != nil {
		return nil, e
	}
	data := obj(o["data"])
	if str(data["preflight_token"]) == "" || str(data["quote_error"]) != "" {
		return nil, &UpstreamError{"quote_unavailable", true}
	}
	if accountEmail(data["email"]) == "" {
		return nil, &UpstreamError{"account_unconfirmed", true}
	}
	return data, nil
}
func gptQuery(ctx context.Context, s commerce.Snapshot, j Job, id string) (Result, error) {
	ref := j.UpstreamRef
	if ref == "" {
		for page := 1; page <= 10; page++ {
			o, e := cardAPI(ctx, s.Provider, "GET", fmt.Sprintf("/gpt-direct/orders?page=%d&page_size=100", page), nil, "")
			if e != nil {
				return Result{}, e
			}
			data := obj(o["data"])
			rows, _ := data["list"].([]any)
			for _, v := range rows {
				row := obj(v)
				if str(row["client_request_id"]) == id {
					ref = str(row["id"])
					if ref == "" {
						ref = str(row["order_id"])
					}
					break
				}
			}
			if ref != "" || len(rows) < 100 {
				break
			}
		}
	}
	if ref == "" {
		return Result{}, &UpstreamError{"original_request_unconfirmed", false}
	}
	o, e := cardAPI(ctx, s.Provider, "GET", "/gpt-direct/orders/"+url.PathEscape(ref), nil, "")
	if e != nil {
		return Result{}, e
	}
	data := obj(o["data"])
	if _, ok := data["order"]; ok {
		data = obj(data["order"])
	}
	returned := str(data["id"])
	if returned == "" {
		returned = str(data["order_id"])
	}
	if returned != ref {
		return Result{}, &UpstreamError{"order_evidence_mismatch", false}
	}
	state := str(data["status"])
	stage := str(data["stage"])
	stage = safeStage(stage)
	if data["client_request_id"] != nil && str(data["client_request_id"]) != id {
		return Result{}, &UpstreamError{"order_evidence_mismatch", false}
	}
	if data["plan"] != nil && s.Plan != "" && str(data["plan"]) != s.Plan {
		return Result{}, &UpstreamError{"plan_evidence_mismatch", false}
	}
	if data["card_id"] != nil && !matchesID(data["card_id"], j.CardID) {
		return Result{}, &UpstreamError{"card_evidence_mismatch", false}
	}
	email := data["account_email"]
	if email == nil {
		email = data["email"]
	}
	if email != nil && j.AccountEmail != "" && accountEmail(email) != j.AccountEmail {
		return Result{}, &UpstreamError{"account_evidence_mismatch", false}
	}
	switch state {
	case "succeeded", "completed":
		if !matchesID(data["card_id"], j.CardID) {
			return Result{}, &UpstreamError{"card_evidence_mismatch", false}
		}
		return Result{Status: "succeeded", Stage: stage, Receipt: ref}, nil
	case "failed", "declined", "cancelled":
		amount, valid := exactNumber(data["final_amount_minor"])
		if !valid || amount != 0 {
			return Result{Status: "unknown", Stage: stage, Failure: "upstream_charge_requires_review"}, nil
		}
		return Result{Status: "failed", Stage: stage, Failure: "recharge_failed"}, nil
	default:
		return Result{Status: "running", Stage: stage}, nil
	}
}
