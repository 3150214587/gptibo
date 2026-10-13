package payments

import (
	"bytes"
	"context"
	"crypto/md5" // GATEWAYB's documented wire protocol requires MD5.
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// https://gateway-b.example.invalid/member/doc.html . Credentials never enter errors/logs.
type GATEWAYBConfig struct{ PID, Key, Type, ChannelID string }
type GATEWAYB struct {
	config GATEWAYBConfig
	client *http.Client
}

func NewGATEWAYB(c GATEWAYBConfig) (*GATEWAYB, error) {
	if !regexp.MustCompile(`^[1-9][0-9]{0,19}$`).MatchString(c.PID) || len(c.Key) < 16 || len(c.Key) > 256 || strings.TrimSpace(c.Key) != c.Key || (c.Type != "alipay" && c.Type != "wxpay") || (c.ChannelID != "" && !regexp.MustCompile(`^[0-9]+(,[0-9]+)*$`).MatchString(c.ChannelID)) {
		return nil, errors.New("invalid GATEWAYB merchant configuration")
	}
	return &GATEWAYB{c, &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (z *GATEWAYB) ID() string      { return "gatewayb-" + z.config.Type }
func (z *GATEWAYB) Account() string { return z.config.PID + ":https://gateway-b.example.invalid" }

// Internal retail/invoice IDs exceed the gateway's 32-character limit. The
// signed param echoes the original ID; both identifiers must agree on callback.
func gatewaybOrderID(id string) string {
	h := sha256.Sum256([]byte(id))
	return hex.EncodeToString(h[:16])
}
func (z *GATEWAYB) sign(v map[string]string) string {
	h := md5.Sum([]byte(canonical(v, true) + z.config.Key))
	return hex.EncodeToString(h[:])
}
func (z *GATEWAYB) call(ctx context.Context, path string, v map[string]string, get bool) (map[string]string, error) {
	var body bytes.Buffer
	form := url.Values{}
	for k, s := range v {
		form.Set(k, s)
	}
	target, method, contentType := "https://gateway-b.example.invalid"+path, http.MethodGet, ""
	if get {
		target += "?" + form.Encode()
	} else {
		method = http.MethodPost
		w := multipart.NewWriter(&body)
		for k, s := range v {
			if err := w.WriteField(k, s); err != nil {
				return nil, errors.New("GATEWAYB request encoding failed")
			}
		}
		if err := w.Close(); err != nil {
			return nil, errors.New("GATEWAYB request encoding failed")
		}
		contentType = w.FormDataContentType()
	}
	req, err := http.NewRequestWithContext(ctx, method, target, &body)
	if err != nil {
		return nil, errors.New("invalid GATEWAYB request")
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	res, err := z.client.Do(req)
	if err != nil {
		return nil, errors.New("GATEWAYB request failed; result unconfirmed")
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 65537))
	if err != nil || len(raw) > 65536 || res.StatusCode != 200 || uniqueJSON(raw) != nil {
		return nil, errors.New("invalid GATEWAYB response")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return nil, errors.New("invalid GATEWAYB response")
	}
	out := map[string]string{}
	for k, v := range fields {
		var s string
		if json.Unmarshal(v, &s) == nil {
			out[k] = s
		} else if string(v) == "null" {
			out[k] = ""
		} else if regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?$`).Match(v) {
			out[k] = string(v)
		} else {
			return nil, errors.New("invalid GATEWAYB response field")
		}
	}
	if out["code"] != "1" {
		return nil, errors.New("GATEWAYB request rejected; check merchant channel configuration")
	}
	return out, nil
}
func (z *GATEWAYB) Create(ctx context.Context, o Order, notify string) (string, error) {
	amount, err := AmountText(o.Amount)
	if err != nil {
		return "", err
	}
	ip, _ := ctx.Value(clientIPKey{}).(string)
	if net.ParseIP(ip) == nil || o.ID == "" {
		return "", errors.New("GATEWAYB requires a valid client IP and order ID")
	}
	name := []rune(o.Subject)
	if len(name) > 100 {
		name = name[:100]
	}
	v := map[string]string{"pid": z.config.PID, "cid": z.config.ChannelID, "type": z.config.Type, "out_trade_no": gatewaybOrderID(o.ID), "notify_url": notify, "name": string(name), "money": amount, "clientip": ip, "param": o.ID, "sign_type": "MD5"}
	v["sign"] = z.sign(v)
	r, err := z.call(ctx, "/mapi.php", v, false)
	if err != nil {
		return "", err
	}
	verified, err := z.Query(ctx, o)
	if err != nil || !tradePattern.MatchString(r["trade_no"]) || (verified.TradeNo != "" && verified.TradeNo != r["trade_no"]) {
		return "", errors.New("GATEWAYB cashier trade not verified")
	}
	qr := r["qrcode"]
	if qr == "" {
		qr = r["payurl"]
	}
	u, err := url.Parse(qr)
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "https" && !(z.config.Type == "wxpay" && u.Scheme == "weixin" && u.Host == "wxpay")) {
		return "", errors.New("invalid GATEWAYB cashier URL")
	}
	return u.String(), nil
}
func (z *GATEWAYB) result(r map[string]string, id string) (Result, error) {
	amount, err := AmountCents(r["money"])
	// GATEWAYB can omit trade_no until an unpaid order is paid. The authenticated
	// query still binds the merchant, order, method and amount; paid results and
	// notifications must always carry a valid transaction ID.
	pendingWithoutTrade := r["status"] == "0" && r["trade_no"] == "" && r["trade_status"] == ""
	if err != nil || id == "" || len(id) > 128 || r["out_trade_no"] != gatewaybOrderID(id) || r["pid"] != z.config.PID || r["type"] != z.config.Type || (!pendingWithoutTrade && !tradePattern.MatchString(r["trade_no"])) {
		return Result{}, errors.New("GATEWAYB payment identity mismatch")
	}
	return Result{OrderID: id, Amount: amount, TradeNo: r["trade_no"]}, nil
}
func (z *GATEWAYB) Query(ctx context.Context, o Order) (Result, error) {
	r, err := z.call(ctx, "/api.php", map[string]string{"act": "order", "pid": z.config.PID, "key": z.config.Key, "out_trade_no": gatewaybOrderID(o.ID)}, true)
	if err != nil {
		return Result{}, err
	}
	result, err := z.result(r, o.ID)
	if err != nil || result.Amount != o.Amount {
		return Result{}, errors.New("GATEWAYB query identity mismatch")
	}
	switch r["status"] {
	case "0":
		result.Status = "pending"
	case "1":
		result.Status = "paid"
	default:
		return Result{}, errors.New("GATEWAYB transaction requires reconciliation")
	}
	return result, nil
}

// No close or refund query/idempotency API is documented. Never invent a closed
// result, and require Service's durable single-attempt guard before refunding.
func (z *GATEWAYB) Close(context.Context, Order) (bool, error) { return false, nil }
func (z *GATEWAYB) SingleAttemptRefund() bool                  { return true }
func (z *GATEWAYB) QueryRefund(context.Context, Order, Refund) (Refunded, error) {
	return Refunded{}, errors.New("GATEWAYB refund requires manual reconciliation")
}
func (z *GATEWAYB) Refund(ctx context.Context, o Order, f Refund) (Refunded, error) {
	amount, err := AmountText(f.Amount)
	if err != nil || f.Amount > o.Amount || f.ID == "" || o.TradeNo == "" {
		return Refunded{}, errors.New("invalid GATEWAYB refund")
	}
	original, err := z.Query(ctx, o)
	if err != nil || original.Status != "paid" || original.TradeNo != o.TradeNo {
		return Refunded{}, errors.New("GATEWAYB original payment not verified")
	}
	_, err = z.call(ctx, "/api.php?act=refund", map[string]string{"pid": z.config.PID, "key": z.config.Key, "out_trade_no": gatewaybOrderID(o.ID), "money": amount}, false)
	if err != nil {
		return Refunded{}, err
	}
	return Refunded{Confirmed: true, TradeNo: original.TradeNo}, nil
}
func (z *GATEWAYB) Notification(req *http.Request) (Result, error) {
	if req.Method != http.MethodGet || len(req.URL.RawQuery) > 16384 {
		return Result{}, errors.New("invalid GATEWAYB notification")
	}
	values, err := url.ParseQuery(req.URL.RawQuery)
	if err != nil {
		return Result{}, errors.New("invalid GATEWAYB notification")
	}
	r := map[string]string{}
	for k, v := range values {
		if len(v) != 1 {
			return Result{}, errors.New("duplicate GATEWAYB notification field")
		}
		r[k] = v[0]
	}
	if r["sign_type"] != "MD5" || subtle.ConstantTimeCompare([]byte(z.sign(r)), []byte(r["sign"])) != 1 || r["trade_status"] != "TRADE_SUCCESS" {
		return Result{}, errors.New("invalid GATEWAYB notification signature or status")
	}
	result, err := z.result(r, r["param"])
	result.Status = "paid"
	return result, err
}
