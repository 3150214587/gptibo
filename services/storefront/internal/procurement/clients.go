package procurement

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"math/big"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var ErrRejected = errors.New("supplier explicitly rejected request")
var ErrReceiptRejected = errors.New("supplier explicitly rejected receipt before purchase")
var ErrOutOfStock = errors.New("supplier goods out of stock")
var identifier = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func client(seconds int) *http.Client {
	return &http.Client{Timeout: time.Duration(seconds) * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
func raw(v any) string { b, _ := json.Marshal(v); return string(b) }
func readResponse(res *http.Response) ([]byte, error) {
	b, e := io.ReadAll(io.LimitReader(res.Body, 256001))
	if e != nil || len(b) > 256000 || !utf8.Valid(b) {
		return nil, errors.New("invalid bounded supplier response")
	}
	return b, nil
}
func cents(v *json.Number) (int64, error) {
	if v == nil {
		return 0, errors.New("missing supplier amount")
	}
	f, e := v.Float64()
	if e != nil || math.IsNaN(f) || math.IsInf(f, 0) || f < 0 || f > 100000000 || math.Abs(f*100-math.Round(f*100)) > 0.000001 {
		return 0, errors.New("invalid supplier amount")
	}
	return int64(math.Round(f * 100)), nil
}

// DeliverySupplier quotes and receipts use up to six decimal places. Compare exact
// micro-dollar integers before rounding any display or local accounting value.
func usdMicros(v *json.Number) (int64, error) {
	if v == nil || len(v.String()) > 40 || !regexp.MustCompile(`^[0-9]+(?:\.[0-9]{1,6})?$`).MatchString(v.String()) {
		return 0, errors.New("invalid supplier USD amount")
	}
	r, ok := new(big.Rat).SetString(v.String())
	if !ok || r.Sign() < 0 {
		return 0, errors.New("invalid supplier USD amount")
	}
	r.Mul(r, big.NewRat(1000000, 1))
	if !r.IsInt() || !r.Num().IsInt64() || r.Num().Int64() > 100000000000000 {
		return 0, errors.New("invalid supplier USD amount")
	}
	return r.Num().Int64(), nil
}

type AccountSupplier struct {
	Base, App, Secret string
	HTTP              *http.Client
}

func NewAccountSupplier(base, app, secret string) (*AccountSupplier, error) {
	u, e := url.Parse(base)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.Fragment != "" || !regexp.MustCompile(`^[0-9]{1,20}$`).MatchString(app) || secret == "" {
		return nil, errors.New("invalid AccountSupplier configuration")
	}
	return &AccountSupplier{"https://" + u.Host, app, secret, client(10)}, nil
}
func LegacySignature(app, secret, stamp, payload string) string {
	sum := md5.Sum([]byte(app + secret + "3.0" + stamp + payload))
	return hex.EncodeToString(sum[:])
}
func (k *AccountSupplier) Call(ctx context.Context, path string, data, out any) error {
	switch path {
	case "/goods/getList", "/goods/getDetail", "/order/create", "/order/getDetail":
	default:
		return errors.New("invalid AccountSupplier path")
	}
	body := raw(data)
	stamp := strconv.FormatInt(time.Now().Unix(), 10)
	req, e := http.NewRequestWithContext(ctx, "POST", k.Base+"/api/v3"+path, strings.NewReader(body))
	if e != nil {
		return e
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "YMX-Gmail-Integration/1.0 (+https://shop.example.invalid)")
	req.Header.Set("X-App-Id", k.App)
	req.Header.Set("X-Version", "3.0")
	req.Header.Set("X-Timestamp", stamp)
	req.Header.Set("X-Signature", LegacySignature(k.App, k.Secret, stamp, body))
	res, e := k.HTTP.Do(req)
	if e != nil {
		return errors.New("supplier response unknown")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return errors.New("supplier unavailable")
	}
	b, e := readResponse(res)
	if e != nil {
		return e
	}
	var envelope struct {
		Code *int            `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(b, &envelope) != nil || envelope.Code == nil {
		return errors.New("invalid supplier response")
	}
	if *envelope.Code != 1000 {
		return ErrRejected
	}
	if len(envelope.Data) == 0 || string(envelope.Data) == "null" || json.Unmarshal(envelope.Data, out) != nil {
		return errors.New("invalid supplier response")
	}
	return nil
}

type Goods struct {
	ID        int64        `json:"goodsId"`
	Name      string       `json:"name"`
	Price     *json.Number `json:"salesPrice"`
	Type      int          `json:"goodsType"`
	Status    int          `json:"status"`
	SKU       int          `json:"skuType"`
	Stock     int64        `json:"stockCount"`
	Minimum   *int64       `json:"minQuantity"`
	Maximum   *int64       `json:"maxQuantity"`
	Multiple  *int64       `json:"multiple"`
	Templates []any        `json:"rechargeTemplates"`
}
type Card struct {
	ID       string  `json:"cardId"`
	Number   string  `json:"cardNo"`
	Password *string `json:"cardPwd"`
	Status   *int    `json:"status"`
}
type GmailOrder struct {
	ID       string       `json:"orderNumber"`
	Merchant string       `json:"outerNumber"`
	Status   int          `json:"status"`
	Count    int          `json:"count"`
	Money    *json.Number `json:"money"`
	Refund   *json.Number `json:"retMoney"`
	Cards    []Card       `json:"cards"`
}

func (k *AccountSupplier) Goods(ctx context.Context, id int64) (Goods, error) {
	var g Goods
	e := k.Call(ctx, "/goods/getDetail", map[string]any{"goodsId": id}, &g)
	if e != nil {
		return g, e
	}
	if g.ID != id || g.Name == "" || len(g.Name) > 6000 {
		return g, errors.New("supplier goods mismatch")
	}
	return g, nil
}
func validateGoods(g Goods, ceiling int64) error {
	money, e := cents(g.Price)
	if e != nil || money > ceiling || money > 10000000 || !regexp.MustCompile(`(?i)gmail|谷歌`).MatchString(g.Name) || g.Type != 1 || g.Status != 1 || g.SKU != 0 || g.Stock < 1 || len(g.Templates) > 0 || (g.Minimum != nil && *g.Minimum > 1) || (g.Maximum != nil && *g.Maximum < 1) || (g.Multiple != nil && *g.Multiple > 1) {
		return errors.New("Gmail goods unavailable or quote changed")
	}
	return nil
}

type DeliverySupplier struct {
	Key  string
	HTTP *http.Client
}

func NewDeliverySupplier(key string) (*DeliverySupplier, error) {
	if key == "" {
		return nil, errors.New("missing DeliverySupplier key")
	}
	return &DeliverySupplier{key, client(15)}, nil
}
func (n *DeliverySupplier) Call(ctx context.Context, path, receipt string, payload *string, out any) error {
	if path != "/products" && path != "/me" && path != "/order" && !(strings.HasPrefix(path, "/order/") && identifier.MatchString(strings.TrimPrefix(path, "/order/"))) {
		return errors.New("invalid DeliverySupplier path")
	}
	method, body := "GET", ""
	if payload != nil {
		method, body = "POST", *payload
		if path != "/order" || receipt == "" {
			return errors.New("invalid procurement request")
		}
	}
	req, e := http.NewRequestWithContext(ctx, method, "https://delivery.example.invalid/api/v1"+path, strings.NewReader(body))
	if e != nil {
		return e
	}
	req.Header.Set("X-API-Key", n.Key)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", receipt)
	}
	res, e := n.HTTP.Do(req)
	if e != nil {
		return errors.New("supplier response unknown")
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 && res.StatusCode < 400 {
		return errors.New("supplier redirect refused")
	}
	b, e := readResponse(res)
	if e != nil {
		return e
	}
	var values map[string]json.RawMessage
	if json.Unmarshal(b, &values) != nil || values == nil {
		return errors.New("invalid supplier response")
	}
	if path == "/order" && payload != nil && res.StatusCode == 400 && string(values["error_code"]) == `"INVALID_IDEMPOTENCY_KEY"` && values["order_id"] == nil && values["order"] == nil {
		return ErrReceiptRejected
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		var id, status string
		if json.Unmarshal(values["order_id"], &id) != nil || json.Unmarshal(values["order_status"], &status) != nil || !identifier.MatchString(id) || status == "" {
			return errors.New("supplier response unknown")
		}
	}
	if json.Unmarshal(b, out) != nil {
		return errors.New("invalid supplier response")
	}
	return nil
}

type NYProduct struct {
	ID           string       `json:"service_id"`
	Name         string       `json:"name"`
	Price        *json.Number `json:"price"`
	Stock        int64        `json:"stock"`
	Availability string       `json:"availability_state"`
}
type NYOrder struct {
	ID        string       `json:"order_id"`
	Status    string       `json:"order_status"`
	Service   string       `json:"service_id"`
	Requested *int         `json:"requested_quantity"`
	Delivered *int         `json:"delivered_quantity"`
	Charged   *json.Number `json:"charged_amount"`
	Refunded  *json.Number `json:"refunded_amount"`
	Net       *json.Number `json:"net_charged_amount"`
	Partial   *bool        `json:"partial_delivery"`
	Values    []string     `json:"delivered_products"`
	Retry     float64      `json:"retry_after_seconds"`
}

var mappings = map[string]struct {
	ID      string
	Ceiling int64
}{"gemini-pro-18m": {"DEMO_PRODUCT_A", 60}, "duolingo-12m": {"DEMO_PRODUCT_B", 38}}

func (n *DeliverySupplier) Products(ctx context.Context) ([]NYProduct, error) {
	var out struct {
		Services []NYProduct `json:"services"`
	}
	e := n.Call(ctx, "/products", "", nil, &out)
	if e == nil && out.Services == nil {
		e = errors.New("invalid product list")
	}
	return out.Services, e
}
func validateNY(products []NYProduct, id string, ceiling int64) error {
	for _, p := range products {
		if p.ID != id {
			continue
		}
		if p.Stock == 0 || p.Availability == "sold_out" {
			return ErrOutOfStock
		}
		money, e := usdMicros(p.Price)
		if e == nil && p.Name != "" && p.Stock >= 1 && (p.Availability == "" || p.Availability == "available") && money > 0 && money <= ceiling*10000 {
			return nil
		}
	}
	return errors.New("supplier goods unavailable or quote changed")
}
