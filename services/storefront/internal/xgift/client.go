package xgift

import (
	"context"
	"encoding/json"
	"errors"
	"gptibo/storefront/internal/security"
	"gptibo/storefront/internal/recharge"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var plans = map[string]int64{"x-premium-3m": 1700, "x-premium-6m": 3400}
var codes = map[string]string{"x-premium-3m": "x-3m", "x-premium-6m": "x-6m"}

func IsPlan(plan string) bool { return plans[plan] > 0 }
func Username(value string) string {
	v := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(value), "@"))
	if !regexp.MustCompile(`^[a-z0-9_]{1,15}$`).MatchString(v) {
		return ""
	}
	return v
}

type Client struct {
	Base, OriginalBase, Backend, Partner, Key, Secret string
	HTTP                                              *http.Client
}

func NewClient(base, backend, partner, key, secret string) (*Client, error) {
	if backend != "legacy" && backend != "platform" {
		return nil, errors.New("invalid XGift backend")
	}
	if base == "" {
		base = "https://x-api.shop.example.invalid/v1"
		if backend == "platform" {
			base = "https://api.shop.example.invalid/v1"
		}
	}
	u, e := url.Parse(base)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.TrimSuffix(u.Path, "/") != "/v1" || key == "" || secret == "" || (backend == "legacy" && partner == "") {
		return nil, errors.New("invalid XGift configuration")
	}
	return &Client{Base: "https://" + u.Host + "/v1", OriginalBase: base, Backend: backend, Partner: partner, Key: key, Secret: secret, HTTP: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *Client) Fingerprint() string {
	values := []string{c.OriginalBase, c.Partner, c.Key, c.Secret}
	if c.Backend == "platform" {
		values = []string{"platform", c.OriginalBase, c.Key, c.Secret}
	}
	return security.Hash(strings.Join(values, "|"))
}

type APIError struct {
	Code   string
	Status int
}

func (e *APIError) Error() string { return "X 赠送服务结果待核对，请继续原订单。" }
func (c *Client) Request(ctx context.Context, path string, body any, key string, out any) error {
	if !regexp.MustCompile(`^/[a-z][A-Za-z0-9_/?=&%.-]*$`).MatchString(path) || strings.Contains(path, "..") {
		return errors.New("invalid supplier path")
	}
	method, raw := "GET", ""
	if body != nil {
		method = "POST"
		raw = recharge.Raw(body)
		if !regexp.MustCompile(`^[A-Za-z0-9_.:-]{8,128}$`).MatchString(key) {
			return errors.New("invalid supplier request key")
		}
	}
	u, _ := url.Parse(c.Base + path)
	stamp, nonce := strconv.FormatInt(time.Now().Unix(), 10), security.ID()
	req, e := http.NewRequestWithContext(ctx, method, u.String(), strings.NewReader(raw))
	if e != nil {
		return e
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Backend == "legacy" {
		req.Header.Set("X-Partner-Id", c.Partner)
	}
	req.Header.Set("X-Key-Id", c.Key)
	req.Header.Set("X-Timestamp", stamp)
	req.Header.Set("X-Nonce", nonce)
	req.Header.Set("X-Signature", recharge.Signature(c.Secret, method, u, stamp, nonce, c.Key, key, raw))
	if body != nil {
		req.Header.Set("Idempotency-Key", key)
	}
	res, e := c.HTTP.Do(req)
	if e != nil {
		return errors.New("supplier outcome unknown")
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 && res.StatusCode < 400 {
		return errors.New("supplier redirect rejected")
	}
	b, e := io.ReadAll(io.LimitReader(res.Body, 256001))
	if e != nil || len(b) > 256000 || !utf8.Valid(b) {
		return errors.New("invalid supplier response")
	}
	var envelope struct {
		Data  json.RawMessage `json:"data"`
		Error *struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(b, &envelope) != nil {
		return errors.New("invalid supplier response")
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		if envelope.Error != nil && regexp.MustCompile(`^[a-z0-9_]{1,64}$`).MatchString(envelope.Error.Code) {
			return &APIError{envelope.Error.Code, res.StatusCode}
		}
		return errors.New("supplier outcome unknown")
	}
	if envelope.Error != nil || len(envelope.Data) == 0 || string(envelope.Data) == "null" || json.Unmarshal(envelope.Data, out) != nil {
		return errors.New("invalid supplier response")
	}
	return nil
}

type Product struct {
	Code    string `json:"code"`
	Name    string `json:"name"`
	Points  int64  `json:"points"`
	Enabled int    `json:"enabled"`
}

func (c *Client) Products(ctx context.Context) ([]Product, error) {
	var out []Product
	if c.Backend == "legacy" {
		e := c.Request(ctx, "/products", nil, "", &out)
		return out, e
	}
	var rows []struct {
		Code      string `json:"code"`
		Name      string `json:"name"`
		Business  string `json:"business"`
		Supply    *int64 `json:"supply_minor"`
		Enabled   bool   `json:"enabled"`
		Available bool   `json:"available"`
	}
	if e := c.Request(ctx, "/products", nil, "", &rows); e != nil {
		return nil, e
	}
	for code, upstream := range codes {
		count := 0
		var p Product
		for _, r := range rows {
			if r.Code == upstream && r.Business == "x" {
				count++
				if r.Supply == nil || *r.Supply < 0 {
					return nil, errors.New("invalid supplier price")
				}
				p = Product{Code: code, Name: r.Name, Points: *r.Supply}
				if r.Enabled && r.Available {
					p.Enabled = 1
				}
			}
		}
		if count != 1 {
			return nil, errors.New("ambiguous supplier product")
		}
		out = append(out, p)
	}
	return out, nil
}

type Balance struct {
	Available int64 `json:"available"`
	Frozen    int64 `json:"frozen"`
}

func (c *Client) Balance(ctx context.Context) (Balance, error) {
	var b Balance
	if c.Backend == "legacy" {
		e := c.Request(ctx, "/balance", nil, "", &b)
		if e == nil && (b.Available < 0 || b.Frozen < 0) {
			e = errors.New("invalid supplier balance")
		}
		return b, e
	}
	var v struct {
		Currency  string `json:"currency"`
		Available *int64 `json:"available_minor"`
		Frozen    *int64 `json:"frozen_minor"`
	}
	e := c.Request(ctx, "/balance", nil, "", &v)
	if e != nil {
		return b, e
	}
	if v.Currency != "CNY" || v.Available == nil || v.Frozen == nil || *v.Available < 0 || *v.Frozen < 0 {
		return b, errors.New("invalid supplier balance")
	}
	return Balance{*v.Available, *v.Frozen}, nil
}

type Eligibility struct {
	Username  string `json:"username"`
	Recipient string `json:"recipient_id"`
	Eligible  *bool  `json:"eligible"`
	Reason    string `json:"reason"`
}

func (c *Client) Eligibility(ctx context.Context, username, plan string) (Eligibility, error) {
	var out Eligibility
	if c.Backend == "legacy" {
		e := c.Request(ctx, "/eligibility", map[string]any{"username": username}, "check:"+security.ID(), &out)
		return out, e
	}
	var v struct {
		Recipient string `json:"recipient"`
		ID        string `json:"recipient_id"`
		Eligible  *bool  `json:"eligible"`
	}
	e := c.Request(ctx, "/preflight", map[string]any{"product_code": codes[plan], "recipient": username}, "check:"+security.ID(), &v)
	return Eligibility{Username: v.Recipient, Recipient: v.ID, Eligible: v.Eligible}, e
}

type Remote struct {
	ID        string  `json:"id"`
	Merchant  string  `json:"merchant_order_no"`
	Product   string  `json:"product_code"`
	Recipient string  `json:"recipient"`
	Points    int64   `json:"points"`
	Currency  string  `json:"currency"`
	Amount    int64   `json:"amount_minor"`
	Status    string  `json:"status"`
	Receipt   *string `json:"receipt"`
	Failure   *string `json:"failure_code"`
	Created   int64   `json:"created_at"`
	Updated   int64   `json:"updated_at"`
	Stage     string  `json:"stage"`
}

func (c *Client) order(ctx context.Context, path string, body any, key string) (Remote, error) {
	var out Remote
	if c.Backend == "legacy" {
		e := c.Request(ctx, path, body, key, &out)
		return out, e
	}
	var v struct {
		ID        string  `json:"id"`
		Merchant  string  `json:"merchant_order_no"`
		Product   string  `json:"product_code"`
		Business  string  `json:"business"`
		Supply    int64   `json:"supply_minor"`
		Recipient string  `json:"recipient"`
		Status    string  `json:"status"`
		Receipt   *string `json:"receipt"`
		Failure   *string `json:"failure_code"`
		Created   string  `json:"created_at"`
		Updated   string  `json:"updated_at"`
		Stage     string  `json:"stage"`
	}
	if e := c.Request(ctx, path, body, key, &v); e != nil {
		return out, e
	}
	product := ""
	for code, upstream := range codes {
		if v.Product == upstream {
			product = code
		}
	}
	created, e1 := time.Parse(time.RFC3339Nano, v.Created)
	updated, e2 := time.Parse(time.RFC3339Nano, v.Updated)
	if product == "" || v.Business != "x" || v.Supply <= 0 || v.Supply > 9007199254740991 || !regexp.MustCompile(`^ord_[a-f0-9]{32}$`).MatchString(v.ID) || Username(v.Recipient) != v.Recipient || !slices.Contains([]string{"queued", "running", "unknown", "succeeded", "failed"}, v.Status) || e1 != nil || e2 != nil {
		return out, errors.New("invalid supplier order")
	}
	if v.Receipt != nil && *v.Receipt == "" {
		v.Receipt = nil
	}
	if v.Failure != nil && *v.Failure == "" {
		v.Failure = nil
	}
	return Remote{v.ID, v.Merchant, product, v.Recipient, v.Supply, "CNY", v.Supply, v.Status, v.Receipt, v.Failure, created.UnixMilli(), updated.UnixMilli(), v.Stage}, nil
}
func (c *Client) Query(ctx context.Context, merchant string) (Remote, error) {
	path := "/orders?merchant_order_no=" + url.QueryEscape(merchant)
	if c.Backend == "platform" {
		path = "/orders/by-merchant/" + url.PathEscape(merchant)
		path = strings.ReplaceAll(path, ":", "%3A")
	}
	return c.order(ctx, path, nil, "")
}
func (c *Client) Create(ctx context.Context, merchant, product, recipient, recipientID string, points int64) (Remote, error) {
	if c.Backend == "platform" {
		return c.order(ctx, "/orders", struct {
			Merchant  string `json:"merchant_order_no"`
			Product   string `json:"product_code"`
			Recipient string `json:"recipient"`
			ID        string `json:"confirmed_recipient_id"`
			Points    int64  `json:"expected_supply_minor"`
		}{merchant, codes[product], recipient, recipientID, points}, "gptibo:"+merchant)
	}
	return c.order(ctx, "/orders", struct {
		Merchant  string `json:"merchant_order_no"`
		Product   string `json:"product_code"`
		Recipient string `json:"recipient"`
		ID        string `json:"recipient_id"`
		Points    int64  `json:"expected_points"`
	}{merchant, product, recipient, recipientID, points}, "gptibo:"+merchant)
}
