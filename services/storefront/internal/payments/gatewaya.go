package payments

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// GATEWAYA uses V2 RSA signatures, including on API responses. Never accept the
// browser return URL as proof of payment. Keys are supplied by server Secrets.
type GATEWAYAConfig struct{ PID, PrivateKey, PublicKey, Type string }
type GATEWAYA struct {
	config  GATEWAYAConfig
	private *rsa.PrivateKey
	public  *rsa.PublicKey
	client  *http.Client
}

type clientIPKey struct{}

func WithClientIP(ctx context.Context, ip string) context.Context {
	return context.WithValue(ctx, clientIPKey{}, ip)
}

func NewGATEWAYA(c GATEWAYAConfig) (*GATEWAYA, error) {
	if !regexp.MustCompile(`^[1-9][0-9]{0,19}$`).MatchString(c.PID) || (c.Type != "alipay" && c.Type != "wxpay") {
		return nil, errors.New("invalid GATEWAYA merchant or payment type")
	}
	b, err := keyBytes(c.PrivateKey)
	if err != nil {
		return nil, errors.New("invalid GATEWAYA private key")
	}
	var private *rsa.PrivateKey
	if k, e := x509.ParsePKCS8PrivateKey(b); e == nil {
		private, _ = k.(*rsa.PrivateKey)
	} else {
		private, _ = x509.ParsePKCS1PrivateKey(b)
	}
	b, err = keyBytes(c.PublicKey)
	if err != nil {
		return nil, errors.New("invalid GATEWAYA public key")
	}
	var public *rsa.PublicKey
	if k, e := x509.ParsePKIXPublicKey(b); e == nil {
		public, _ = k.(*rsa.PublicKey)
	} else {
		public, _ = x509.ParsePKCS1PublicKey(b)
	}
	if private == nil || public == nil || private.N.BitLen() < 2048 || public.N.BitLen() < 2048 || private.Validate() != nil {
		return nil, errors.New("GATEWAYA requires valid RSA keys of at least 2048 bits")
	}
	return &GATEWAYA{c, private, public, &http.Client{Timeout: 8 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (s *GATEWAYA) ID() string      { return "gatewaya-" + s.config.Type }
func (s *GATEWAYA) Account() string { return s.config.PID + ":https://gateway-a.example.invalid" }
func (s *GATEWAYA) sign(v map[string]string) (string, error) {
	h := sha256.Sum256([]byte(canonical(v, true)))
	b, e := rsa.SignPKCS1v15(rand.Reader, s.private, crypto.SHA256, h[:])
	return base64.StdEncoding.EncodeToString(b), e
}
func (s *GATEWAYA) verify(v map[string]string) error {
	if v["sign_type"] != "RSA" || !regexp.MustCompile(`^[0-9]{10}$`).MatchString(v["timestamp"]) {
		return errors.New("invalid GATEWAYA signature metadata")
	}
	b, e := base64.StdEncoding.DecodeString(v["sign"])
	if e != nil {
		return errors.New("invalid GATEWAYA signature")
	}
	h := sha256.Sum256([]byte(canonical(v, true)))
	if rsa.VerifyPKCS1v15(s.public, crypto.SHA256, h[:], b) != nil {
		return errors.New("invalid GATEWAYA signature")
	}
	// Signed notifications may be retried long after payment. Replay is handled
	// by immutable merchant/order/amount checks and transactional state changes.
	return nil
}
func (s *GATEWAYA) call(ctx context.Context, action string, v map[string]string) (map[string]string, error) {
	v["pid"], v["timestamp"], v["sign_type"] = s.config.PID, strconv.FormatInt(time.Now().Unix(), 10), "RSA"
	sign, e := s.sign(v)
	if e != nil {
		return nil, e
	}
	v["sign"] = sign
	form := url.Values{}
	for k, val := range v {
		form.Set(k, val)
	}
	req, e := http.NewRequestWithContext(ctx, "POST", "https://gateway-a.example.invalid/api/pay/"+action, strings.NewReader(form.Encode()))
	if e != nil {
		return nil, e
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded;charset=UTF-8")
	res, e := s.client.Do(req)
	if e != nil {
		return nil, errors.New("GATEWAYA gateway unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, errors.New("GATEWAYA gateway unavailable")
	}
	raw, e := io.ReadAll(io.LimitReader(res.Body, 100001))
	if e != nil || len(raw) > 100000 || uniqueJSON(raw) != nil {
		return nil, errors.New("malformed GATEWAYA response")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return nil, errors.New("malformed GATEWAYA response")
	}
	out := map[string]string{}
	for k, val := range fields {
		var text string
		if json.Unmarshal(val, &text) == nil {
			out[k] = text
			continue
		}
		// The protocol signs scalar strings/numbers, never re-serialized objects.
		if string(val) == "null" {
			out[k] = ""
			continue
		}
		if !regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?$`).Match(val) {
			return nil, errors.New("unsupported GATEWAYA response field")
		}
		out[k] = string(val)
	}
	// The live create endpoint can return only code/trade_no/pay_type/pay_info.
	// Permit that exact unsigned variant only for creation; Create verifies the
	// trade against a signed query before exposing the cashier URL. Partial or
	// invalid signatures must still fail closed, including on create responses.
	_, hasSign := out["sign"]
	_, hasType := out["sign_type"]
	_, hasTime := out["timestamp"]
	if action != "create" || hasSign || hasType || hasTime {
		if e = s.verify(out); e != nil {
			return nil, e
		}
	} else if len(out) != 4 {
		return nil, errors.New("unexpected unsigned GATEWAYA create response")
	}
	if out["code"] != "0" {
		return nil, errors.New("GATEWAYA request rejected; verify merchant channel configuration")
	}
	return out, nil
}
func (s *GATEWAYA) Create(ctx context.Context, o Order, notify string) (string, error) {
	amount, e := AmountText(o.Amount)
	if e != nil {
		return "", e
	}
	ip, _ := ctx.Value(clientIPKey{}).(string)
	if net.ParseIP(ip) == nil {
		return "", errors.New("GATEWAYA requires a valid client IP")
	}
	name := o.Subject
	for len(name) > 127 {
		_, size := utf8.DecodeLastRuneInString(name)
		name = name[:len(name)-size]
	}
	r, e := s.call(ctx, "create", map[string]string{"method": "jump", "type": s.config.Type, "out_trade_no": o.ID, "notify_url": notify, "name": name, "money": amount, "clientip": ip, "fee_mode": "0"})
	if e != nil {
		return "", e
	}
	u, e := url.Parse(r["pay_info"])
	if e != nil || r["pay_type"] != "jump" || !tradePattern.MatchString(r["trade_no"]) || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return "", errors.New("invalid GATEWAYA cashier URL")
	}
	if _, signed := r["sign"]; !signed {
		verified, err := s.Query(ctx, o)
		if err != nil || verified.TradeNo != r["trade_no"] {
			return "", errors.New("GATEWAYA cashier trade not verified")
		}
	}
	return u.String(), nil
}
func (s *GATEWAYA) result(r map[string]string) (Result, error) {
	amount, e := AmountCents(r["money"])
	if e != nil || r["pid"] != s.config.PID || r["type"] != s.config.Type || r["out_trade_no"] == "" || !tradePattern.MatchString(r["trade_no"]) {
		return Result{}, errors.New("GATEWAYA payment identity mismatch")
	}
	return Result{OrderID: r["out_trade_no"], Amount: amount, TradeNo: r["trade_no"]}, nil
}
func (s *GATEWAYA) Query(ctx context.Context, o Order) (Result, error) {
	r, e := s.call(ctx, "query", map[string]string{"out_trade_no": o.ID})
	if e != nil {
		return Result{}, e
	}
	result, e := s.result(r)
	if e != nil || result.OrderID != o.ID || result.Amount != o.Amount {
		return Result{}, errors.New("GATEWAYA query identity mismatch")
	}
	switch r["status"] {
	case "0":
		result.Status = "pending"
	case "1":
		result.Status = "paid"
	default:
		return Result{}, errors.New("GATEWAYA transaction requires reconciliation")
	}
	return result, nil
}
func (s *GATEWAYA) Close(ctx context.Context, o Order) (bool, error) {
	_, e := s.call(ctx, "close", map[string]string{"out_trade_no": o.ID})
	return e == nil, e
}

// GATEWAYA documents out_refund_no as the duplicate-refund guard, but does not
// document a refund-not-found code. Submit the same refund ID on retry, then
// query its final receipt; never guess that a gateway error means not found.
func (s *GATEWAYA) SubmitRefundFirst() bool { return true }
func (s *GATEWAYA) Refund(ctx context.Context, o Order, f Refund) (Refunded, error) {
	amount, e := AmountText(f.Amount)
	if e != nil || f.Amount > o.Amount || f.ID == "" {
		return Refunded{}, errors.New("invalid GATEWAYA refund")
	}
	r, e := s.call(ctx, "refund", map[string]string{"out_trade_no": o.ID, "out_refund_no": f.ID, "money": amount})
	if e != nil {
		return Refunded{}, e
	}
	actual, e := AmountCents(r["money"])
	if e != nil || actual != f.Amount || r["out_refund_no"] != f.ID || !tradePattern.MatchString(r["trade_no"]) {
		return Refunded{}, errors.New("GATEWAYA refund identity mismatch")
	}
	return Refunded{TradeNo: r["trade_no"]}, nil // Confirmation requires refundquery.
}
func (s *GATEWAYA) QueryRefund(ctx context.Context, o Order, f Refund) (Refunded, error) {
	r, e := s.call(ctx, "refundquery", map[string]string{"out_refund_no": f.ID})
	if e != nil {
		return Refunded{}, e
	}
	amount, e := AmountCents(r["money"])
	if e != nil || amount != f.Amount || r["out_trade_no"] != o.ID || r["out_refund_no"] != f.ID || !tradePattern.MatchString(r["trade_no"]) {
		return Refunded{}, errors.New("GATEWAYA refund receipt mismatch")
	}
	if r["status"] != "1" {
		return Refunded{}, fmt.Errorf("GATEWAYA refund not confirmed")
	}
	return Refunded{Confirmed: true, TradeNo: r["trade_no"]}, nil
}
func (s *GATEWAYA) Notification(req *http.Request) (Result, error) {
	if req.Method != http.MethodGet || len(req.URL.RawQuery) > 16000 {
		return Result{}, errors.New("invalid GATEWAYA notification")
	}
	q, e := url.ParseQuery(req.URL.RawQuery)
	if e != nil {
		return Result{}, e
	}
	r := map[string]string{}
	for k, v := range q {
		if len(v) != 1 {
			return Result{}, errors.New("duplicate GATEWAYA notification field")
		}
		r[k] = v[0]
	}
	if e = s.verify(r); e != nil {
		return Result{}, e
	}
	if r["trade_status"] != "TRADE_SUCCESS" {
		return Result{}, errors.New("unsupported GATEWAYA notification state")
	}
	result, e := s.result(r)
	result.Status = "paid"
	return result, e
}
