package recharge

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"gptibo/storefront/internal/fault"
	"gptibo/storefront/internal/security"
)

type Client struct {
	Base, Partner, Key, Secret string
	HTTP                       *http.Client
}

func NewClient(base, partner, key, secret string) (*Client, error) {
	if base == "" {
		base = "https://recharge.example.invalid/v1"
	}
	u, e := url.Parse(base)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.TrimSuffix(u.Path, "/") != "/v1" || partner == "" || key == "" || secret == "" {
		return nil, errors.New("invalid Recharge configuration")
	}
	return &Client{Base: "https://" + u.Host + "/v1", Partner: partner, Key: key, Secret: secret, HTTP: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *Client) Account() string { return strings.TrimSuffix(c.Base, "/v1") + ":" + c.Partner }
func HMAC(secret, value string) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(value))
	return hex.EncodeToString(h.Sum(nil))
}
func Canonical(q url.Values) string {
	pairs := [][2]string{}
	encode := func(s string) string { return strings.ReplaceAll(url.QueryEscape(s), "+", "%20") }
	for k, vs := range q {
		for _, v := range vs {
			pairs = append(pairs, [2]string{encode(k), encode(v)})
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i][0] == pairs[j][0] {
			return pairs[i][1] < pairs[j][1]
		}
		return pairs[i][0] < pairs[j][0]
	})
	parts := make([]string, len(pairs))
	for i, p := range pairs {
		parts[i] = p[0] + "=" + p[1]
	}
	return strings.Join(parts, "&")
}
func Signature(secret, method string, u *url.URL, stamp, nonce, key, idempotency, raw string) string {
	return HMAC(secret, strings.Join([]string{method, u.EscapedPath(), Canonical(u.Query()), stamp, nonce, key, idempotency, security.Hash(raw)}, "\n"))
}

type APIError struct {
	Code       string
	Retryable  bool
	HTTPStatus int
	RequestID  string
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	if message := requestMessages[e.Code]; message != "" {
		return message
	}
	return "充值服务暂未完成请求，请查询原订单后重试。"
}

func (e *APIError) Unwrap() error { return fault.New(503, e.Error()) }

var requestMessages = map[string]string{
	"invalid_request":                         "提交内容未通过校验，请核对资料。",
	"invalid_credential":                      "登录凭据已失效，请重新登录后提交。",
	"session_invalid":                         "登录凭据已失效，请重新登录后提交。",
	"invalid_signature":                       "充值服务认证异常，请联系客服。",
	"timestamp_out_of_range":                  "充值服务时间校验异常，请联系客服。",
	"nonce_replayed":                          "请求校验重复，请刷新原订单核对结果。",
	"ip_not_allowed":                          "充值服务访问受限，请联系客服。",
	"product_not_authorized":                  "该套餐暂未开放，请联系客服。",
	"order_not_found":                         "平台暂未查到原订单，请联系客服核对，勿重新付款。",
	"idempotency_conflict":                    "原提交编号与资料不一致，请保留原订单并联系客服。",
	"idempotency_in_progress":                 "原请求仍在处理中，请稍后查询原订单，勿重复提交。",
	"invalid_state_transition":                "订单状态已变化，请刷新原订单核对。",
	"provider_refund_reconciliation_required": "退款正在核对渠道记录，请查询原退款，勿重复申请。",
	"price_out_of_range":                      "套餐价格已变化，请刷新套餐或联系客服。",
	"rate_limited":                            "充值服务请求较频繁，请稍后核对原提交。",
	"temporarily_unavailable":                 "充值服务暂不可用，请稍后查询原订单。",
	"transport_unknown":                       "提交连接中断或等待超时，结果尚未确认，请核对原提交。",
}

func safeRequestID(value string) string {
	if regexp.MustCompile(`^req_[A-Za-z0-9_-]{1,96}$`).MatchString(value) {
		return value
	}
	return ""
}

var safePath = regexp.MustCompile(`^/[a-z0-9][A-Za-z0-9_/?=&%.-]*$`)
var remoteID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func bounded(r io.Reader, n int) ([]byte, error) {
	b, e := io.ReadAll(io.LimitReader(r, int64(n+1)))
	if e != nil || len(b) > n || !utf8.Valid(b) {
		return nil, errors.New("invalid bounded response")
	}
	return b, nil
}
func (c *Client) Request(ctx context.Context, path string, body *string, key string, out any) error {
	if !safePath.MatchString(path) || strings.Contains(path, "..") {
		return errors.New("invalid Recharge path")
	}
	method, raw := "GET", ""
	if body != nil {
		method = "POST"
		raw = *body
		if len(key) < 8 || len(key) > 128 {
			return errors.New("invalid idempotency key")
		}
	}
	u, _ := url.Parse(c.Base + path)
	stamp, nonce := strconv.FormatInt(time.Now().Unix(), 10), security.ID()
	req, e := http.NewRequestWithContext(ctx, method, u.String(), strings.NewReader(raw))
	if e != nil {
		return e
	}
	req.Header.Set("X-Partner-Id", c.Partner)
	req.Header.Set("X-Key-Id", c.Key)
	req.Header.Set("X-Timestamp", stamp)
	req.Header.Set("X-Nonce", nonce)
	req.Header.Set("X-Signature", Signature(c.Secret, method, u, stamp, nonce, c.Key, key, raw))
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", key)
	}
	response, e := c.HTTP.Do(req)
	if e != nil {
		return &APIError{Code: "transport_unknown", Retryable: true}
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		return &APIError{Code: "redirect_rejected", Retryable: true, HTTPStatus: response.StatusCode}
	}
	b, e := bounded(response.Body, 256000)
	if e != nil {
		return &APIError{Code: "invalid_response", Retryable: true, HTTPStatus: response.StatusCode, RequestID: safeRequestID(response.Header.Get("X-Request-Id")), RetryAfter: retryAfter(response.Header.Get("Retry-After"))}
	}
	var envelope struct {
		Data  json.RawMessage `json:"data"`
		Error *struct {
			Code      string `json:"code"`
			Retryable bool   `json:"retryable"`
			RequestID string `json:"request_id"`
		} `json:"error"`
	}
	if json.Unmarshal(b, &envelope) != nil {
		return &APIError{Code: "invalid_response", Retryable: true, HTTPStatus: response.StatusCode, RequestID: safeRequestID(response.Header.Get("X-Request-Id")), RetryAfter: retryAfter(response.Header.Get("Retry-After"))}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		code, retry := "request_rejected", slices.Contains([]int{429, 502, 503, 504}, response.StatusCode)
		requestID := safeRequestID(response.Header.Get("X-Request-Id"))
		if envelope.Error != nil {
			if requestMessages[envelope.Error.Code] != "" && envelope.Error.Code != "transport_unknown" {
				code = envelope.Error.Code
			}
			if requestID == "" {
				requestID = safeRequestID(envelope.Error.RequestID)
			}
			retry = retry || envelope.Error.Retryable
		}
		return &APIError{Code: code, Retryable: retry, HTTPStatus: response.StatusCode, RequestID: requestID, RetryAfter: retryAfter(response.Header.Get("Retry-After"))}
	}
	if envelope.Error != nil || len(envelope.Data) == 0 || string(envelope.Data) == "null" || json.Unmarshal(envelope.Data, out) != nil {
		return &APIError{Code: "invalid_response", Retryable: true, HTTPStatus: response.StatusCode, RequestID: safeRequestID(response.Header.Get("X-Request-Id")), RetryAfter: retryAfter(response.Header.Get("Retry-After"))}
	}
	return nil
}

type Product struct {
	Code      string   `json:"product_code"`
	Supply    string   `json:"supply_price"`
	MaxSale   string   `json:"max_sale_price"`
	Currency  string   `json:"currency"`
	Quantity  int      `json:"max_quantity"`
	Available bool     `json:"available"`
	Modes     []string `json:"delivery_modes"`
}
type RemoteOrder struct {
	ID          string  `json:"order_id"`
	Merchant    string  `json:"merchant_order_no"`
	Product     string  `json:"product_code"`
	Quantity    int     `json:"quantity"`
	Currency    string  `json:"currency"`
	Collection  string  `json:"collection_mode"`
	Scope       string  `json:"payment_scope"`
	Delivery    string  `json:"delivery_mode"`
	Channel     string  `json:"payment_channel"`
	Sale        string  `json:"sale_amount"`
	Supply      string  `json:"supply_amount"`
	Margin      string  `json:"merchant_margin"`
	Refunded    string  `json:"refunded_amount"`
	Payment     string  `json:"payment_status"`
	Fulfillment string  `json:"fulfillment_status"`
	URL         *string `json:"qr_payload"`
	Expires     string  `json:"expires_at"`
}
type Redemption struct {
	Message  *string `json:"message"`
	ID       string  `json:"redemption_id"`
	Order    string  `json:"order_id"`
	Status   string  `json:"status"`
	Failure  *string `json:"failure_code"`
	Category *string `json:"error_category"`
	Result   *string `json:"result_code"`
	Fallback *bool   `json:"fallback_recharge_available"`
	Email    *string `json:"account_email_masked"`
	Finished *string `json:"finished_at"`
	Stage    *string `json:"progress_stage"`
	Version  *int64  `json:"progress_version"`
	Updated  *string `json:"progress_updated_at"`
	Attempt  *int64  `json:"attempt_no"`
	Retry    bool    `json:"retry_allowed"`
	Next     string  `json:"next_action"`
	Recovery string  `json:"recovery_action"`
}
type RemoteRefund struct {
	ID       string `json:"refund_id"`
	Order    string `json:"order_id"`
	Merchant string `json:"merchant_refund_no"`
	Amount   string `json:"amount"`
	Status   string `json:"status"`
}

var moneyPattern = regexp.MustCompile(`^(0|[1-9][0-9]{0,7})\.[0-9]{2}$`)

func Money(v string) (int64, error) {
	if !moneyPattern.MatchString(v) {
		return 0, errors.New("invalid supplier money")
	}
	n, e := strconv.ParseInt(strings.ReplaceAll(v, ".", ""), 10, 64)
	return n, e
}
func Margin(v string) (int64, error) {
	if strings.HasPrefix(v, "-") {
		n, e := Money(v[1:])
		return -n, e
	}
	return Money(v)
}
func Amount(n int64) string { return fmt.Sprintf("%d.%02d", n/100, n%100) }
func (c *Client) PaymentLink(value *string, id string) (*url.URL, error) {
	if value == nil {
		return nil, nil
	}
	u, e := url.Parse(*value)
	if e != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" || "https://"+u.Host != strings.TrimSuffix(c.Base, "/v1") {
		return nil, errors.New("invalid payment link")
	}
	if strings.HasPrefix(u.Path, "/payments") {
		q, e := url.ParseQuery(u.RawQuery)
		if e != nil || u.Path != "/payments/"+id || len(q) != 1 || len(q["token"]) != 1 || !regexp.MustCompile(`^[A-Za-z0-9_-]{16,256}$`).MatchString(q.Get("token")) {
			return nil, errors.New("invalid cashier link")
		}
	}
	return u, nil
}
func (c *Client) QR(ctx context.Context, r RemoteOrder, deadline int64) (*string, error) {
	u, e := c.PaymentLink(r.URL, r.ID)
	if e != nil {
		return nil, e
	}
	if u == nil || u.Path != "/payments/"+r.ID || deadline <= time.Now().UnixMilli() {
		return nil, nil
	}
	type cashier struct {
		Status   string  `json:"status"`
		Amount   string  `json:"amount"`
		Expires  string  `json:"expires_at"`
		Expired  *bool   `json:"expired"`
		Start    *bool   `json:"can_start"`
		QR       *string `json:"qr_code"`
		Delivery string  `json:"delivery_mode"`
	}
	read := func(action string) (cashier, error) {
		var out cashier
		v := *u
		v.Path += "/" + action
		method := "GET"
		if action == "start" {
			method = "POST"
		}
		req, e := http.NewRequestWithContext(ctx, method, v.String(), nil)
		if e != nil {
			return out, e
		}
		req.Header.Set("Accept", "application/json")
		res, e := c.HTTP.Do(req)
		if e != nil {
			return out, &APIError{Code: "payment_transport_unknown", Retryable: true}
		}
		defer res.Body.Close()
		b, e := bounded(res.Body, 16000)
		if e != nil || res.StatusCode != 200 || json.Unmarshal(b, &out) != nil {
			return out, &APIError{Code: "invalid_payment_response", Retryable: true, HTTPStatus: res.StatusCode}
		}
		exp, e1 := time.Parse(time.RFC3339Nano, out.Expires)
		original, e2 := time.Parse(time.RFC3339Nano, r.Expires)
		a, e3 := Money(out.Amount)
		want, e4 := Money(r.Sale)
		if e1 != nil || e2 != nil || e3 != nil || e4 != nil || a != want || !exp.Equal(original) || out.Delivery != r.Delivery || out.Start == nil || out.Expired == nil || !paymentStatus(out.Status) {
			return out, errors.New("cashier quote mismatch")
		}
		return out, nil
	}
	data, e := read("status")
	if e != nil {
		return nil, e
	}
	if data.Status != "pending" || *data.Expired || deadline <= time.Now().UnixMilli() {
		return nil, nil
	}
	if data.QR == nil && *data.Start {
		data, e = read("start")
		if e != nil {
			return nil, e
		}
	}
	if data.Status != "pending" || *data.Expired || deadline <= time.Now().UnixMilli() || data.QR == nil {
		return nil, nil
	}
	v, e := url.Parse(*data.QR)
	if e != nil || v.Scheme != "https" || v.Host != "qr.alipay.com" || v.User != nil || v.RawQuery != "" || v.Fragment != "" || !regexp.MustCompile(`^/[A-Za-z0-9]{8,128}$`).MatchString(v.Path) {
		return nil, errors.New("invalid Alipay QR")
	}
	return data.QR, nil
}
func paymentStatus(s string) bool {
	return slices.Contains([]string{"pending", "paid", "closed", "expired", "partially_refunded", "refunded"}, s)
}
