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
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"
)

type snTransport func(*http.Request) (*http.Response, error)

func (f snTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Independent protocol fixture: URL values are sorted and joined without URL
// escaping; sign and sign_type are excluded. This also checks Unicode and &/+.
func snCanonical(v map[string]string) string {
	keys := []string{}
	for k, val := range v {
		if val != "" && k != "sign" && k != "sign_type" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	parts := []string{}
	for _, k := range keys {
		parts = append(parts, k+"="+v[k])
	}
	return strings.Join(parts, "&")
}
func snSign(t *testing.T, k *rsa.PrivateKey, v map[string]string) {
	t.Helper()
	v["timestamp"] = "1791331200"
	v["sign_type"] = "RSA"
	h := sha256.Sum256([]byte(snCanonical(v)))
	b, e := rsa.SignPKCS1v15(rand.Reader, k, crypto.SHA256, h[:])
	if e != nil {
		t.Fatal(e)
	}
	v["sign"] = base64.StdEncoding.EncodeToString(b)
}
func snFixture(t *testing.T, kind string) (*GATEWAYA, *rsa.PrivateKey) {
	t.Helper()
	merchant, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	platform, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	priv, _ := x509.MarshalPKCS8PrivateKey(merchant)
	pub, _ := x509.MarshalPKIXPublicKey(&platform.PublicKey)
	s, e := NewGATEWAYA(GATEWAYAConfig{PID: "12345", Type: kind, PrivateKey: base64.StdEncoding.EncodeToString(priv), PublicKey: base64.StdEncoding.EncodeToString(pub)})
	if e != nil {
		t.Fatal(e)
	}
	return s, platform
}
func snNotify(v map[string]string) *http.Request {
	q := url.Values{}
	for k, val := range v {
		q.Set(k, val)
	}
	return httptest.NewRequest("GET", "https://shop.test/api/payments/notify/gatewaya-alipay?"+q.Encode(), nil)
}
func TestGATEWAYANotificationIntegrity(t *testing.T) {
	s, k := snFixture(t, "alipay")
	base := map[string]string{"pid": "12345", "type": "alipay", "out_trade_no": "YMX-ORDER", "trade_no": "TRADE-123", "money": "69.90", "trade_status": "TRADE_SUCCESS", "name": "中文商品 & +", "param": "x+y", "new_extension": "signed"}
	snSign(t, k, base)
	r, e := s.Notification(snNotify(base))
	if e != nil || r.Amount != 6990 || r.Status != "paid" {
		t.Fatal(r, e)
	}
	for _, key := range []string{"money", "pid", "type", "out_trade_no", "trade_no", "new_extension", "timestamp", "sign_type"} {
		t.Run("tamper-"+key, func(t *testing.T) {
			v := map[string]string{}
			for k, val := range base {
				v[k] = val
			}
			v[key] += "x"
			if _, e := s.Notification(snNotify(v)); e == nil {
				t.Fatal("accepted tampered field")
			}
		})
	}
	for key, val := range map[string]string{"pid": "54321", "type": "wxpay", "trade_status": "TRADE_CLOSED", "money": "0.00"} {
		v := map[string]string{}
		for k, val := range base {
			v[k] = val
		}
		v[key] = val
		snSign(t, k, v)
		if _, e := s.Notification(snNotify(v)); e == nil {
			t.Fatalf("accepted signed wrong %s", key)
		}
	}
	req := snNotify(base)
	req.URL.RawQuery += "&money=69.90"
	if _, e := s.Notification(req); e == nil {
		t.Fatal("accepted duplicate field")
	}
	req = snNotify(base)
	req.Method = "POST"
	if _, e := s.Notification(req); e == nil {
		t.Fatal("accepted wrong method")
	}
}
func TestGATEWAYACreateQueryCloseAndRefund(t *testing.T) {
	for _, kind := range []string{"alipay", "wxpay"} {
		t.Run(kind, func(t *testing.T) {
			s, key := snFixture(t, kind)
			calls := []string{}
			s.client.Transport = snTransport(func(req *http.Request) (*http.Response, error) {
				if req.URL.Scheme != "https" || req.URL.Host != "gateway-a.example.invalid" || req.Method != "POST" {
					t.Fatal("unexpected gateway")
				}
				if e := req.ParseForm(); e != nil {
					t.Fatal(e)
				}
				v := map[string]string{}
				for k, vals := range req.PostForm {
					if len(vals) != 1 {
						t.Fatal("duplicate request")
					}
					v[k] = vals[0]
				}
				b, e := base64.StdEncoding.DecodeString(v["sign"])
				h := sha256.Sum256([]byte(snCanonical(v)))
				if e != nil || rsa.VerifyPKCS1v15(&s.private.PublicKey, crypto.SHA256, h[:], b) != nil || v["pid"] != "12345" || v["sign_type"] != "RSA" {
					t.Fatal("request signature invalid")
				}
				action := strings.TrimPrefix(req.URL.Path, "/api/pay/")
				calls = append(calls, action)
				r := map[string]string{"code": "0", "pid": "12345", "type": kind, "out_trade_no": "YMX-ORDER", "trade_no": "TRADE-123", "money": "69.90", "status": "1"}
				switch action {
				case "create":
					if v["clientip"] != "203.0.113.10" || v["method"] != "jump" || v["type"] != kind || v["money"] != "69.90" || v["fee_mode"] != "0" || v["notify_url"] != "https://shop.test/notify" || len(v["name"]) > 127 {
						t.Fatal("incorrect create request")
					}
					r["pay_type"] = "jump"
					r["pay_info"] = "https://gateway-a.example.invalid/cashier/fixture"
				case "refund", "refundquery":
					if v["out_refund_no"] != "CASE-123" {
						t.Fatal("unstable refund key")
					}
					r["out_refund_no"] = "CASE-123"
					r["money"] = "1.00"
				}
				snSign(t, key, r)
				raw, _ := json.Marshal(r)
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(raw))), Header: http.Header{}}, nil
			})
			o := Order{ID: "YMX-ORDER", Subject: strings.Repeat("中", 60), Amount: 6990, Expires: time.Now().Add(time.Minute).UnixMilli()}
			ctx := WithClientIP(context.Background(), "203.0.113.10")
			if _, e := s.Create(context.Background(), o, "https://shop.test/notify"); e == nil {
				t.Fatal("invented client IP")
			}
			if qr, e := s.Create(ctx, o, "https://shop.test/notify"); e != nil || !strings.HasPrefix(qr, "https://") {
				t.Fatal(qr, e)
			}
			if r, e := s.Query(ctx, o); e != nil || r.Status != "paid" {
				t.Fatal(r, e)
			}
			if closed, e := s.Close(ctx, o); e != nil || !closed {
				t.Fatal(closed, e)
			}
			f := Refund{ID: "CASE-123", Amount: 100}
			if r, e := s.Refund(ctx, o, f); e != nil || r.Confirmed {
				t.Fatal("must query receipt", r, e)
			}
			if r, e := s.QueryRefund(ctx, o, f); e != nil || !r.Confirmed {
				t.Fatal(r, e)
			}
			if strings.Join(calls, ",") != "create,query,close,refund,refundquery" {
				t.Fatal(calls)
			}
		})
	}
}
func TestGATEWAYAUnsignedCreateRequiresSignedMatchingQuery(t *testing.T) {
	for _, kind := range []string{"alipay", "wxpay"} {
		s, key := snFixture(t, kind)
		for _, mode := range []string{"valid", "unsigned-query", "bad-signature", "wrong-trade", "wrong-amount", "wrong-merchant", "partial-signature", "extra-field"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				queries := 0
				s.client.Transport = snTransport(func(req *http.Request) (*http.Response, error) {
					r := map[string]string{"code": "0", "trade_no": "TRADE-123", "pay_type": "jump", "pay_info": "https://gateway-a.example.invalid/cashier/fixture"}
					if strings.HasSuffix(req.URL.Path, "/create") {
						if mode == "partial-signature" {
							r["sign_type"] = "RSA"
						}
						if mode == "extra-field" {
							r["untrusted"] = "extra"
						}
					} else {
						queries++
						r = map[string]string{"code": "0", "pid": "12345", "type": kind, "out_trade_no": "YMX-ORDER", "trade_no": "TRADE-123", "money": "69.90", "status": "0"}
						if mode == "wrong-trade" {
							r["trade_no"] = "TRADE-OTHER"
						}
						if mode == "wrong-amount" {
							r["money"] = "69.91"
						}
						if mode == "wrong-merchant" {
							r["pid"] = "54321"
						}
						if mode != "unsigned-query" {
							snSign(t, key, r)
						}
						if mode == "bad-signature" {
							r["sign"] = "invalid"
						}
					}
					b, _ := json.Marshal(r)
					return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(string(b)))}, nil
				})
				qr, e := s.Create(WithClientIP(context.Background(), "203.0.113.10"), Order{ID: "YMX-ORDER", Amount: 6990}, "https://shop.test/notify")
				if mode == "valid" {
					if e != nil || qr == "" || queries != 1 {
						t.Fatal(qr, e, queries)
					}
				} else if e == nil || qr != "" {
					t.Fatal("unverified cashier exposed", mode)
				}
			})
		}
	}
}

func TestGATEWAYARejectsInvalidGatewayReceipts(t *testing.T) {
	s, key := snFixture(t, "alipay")
	for _, tc := range []string{"unsigned", "duplicate", "wrong-order", "wrong-amount", "wrong-merchant", "wrong-type", "frozen", "refunded", "wrong-refund", "failed-refund", "unsafe-url", "html"} {
		t.Run(tc, func(t *testing.T) {
			s.client.Transport = snTransport(func(req *http.Request) (*http.Response, error) {
				r := map[string]string{"code": "0", "pid": "12345", "type": "alipay", "out_trade_no": "YMX-ORDER", "trade_no": "TRADE-123", "money": "69.90", "status": "1", "out_refund_no": "CASE-123", "pay_type": "jump", "pay_info": "https://gateway-a.example.invalid/cashier"}
				switch tc {
				case "wrong-order":
					r["out_trade_no"] = "OTHER"
				case "wrong-amount":
					r["money"] = "70.00"
				case "wrong-merchant":
					r["pid"] = "54321"
				case "wrong-type":
					r["type"] = "wxpay"
				case "frozen":
					r["status"] = "3"
				case "refunded":
					r["status"] = "2"
				case "wrong-refund":
					r["out_refund_no"] = "OTHER"
				case "failed-refund":
					r["status"] = "0"
				case "unsafe-url":
					r["pay_info"] = "javascript:alert(1)"
				case "html":
					r["pay_type"] = "html"
					r["pay_info"] = "<script>bad</script>"
				}
				snSign(t, key, r)
				if tc == "unsigned" {
					delete(r, "sign")
				}
				raw, _ := json.Marshal(r)
				if tc == "duplicate" {
					raw = append([]byte(`{"code":0,`), raw[1:]...)
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
			})
			o := Order{ID: "YMX-ORDER", Amount: 6990, Subject: "fixture"}
			ctx := WithClientIP(context.Background(), "203.0.113.10")
			var e error
			switch tc {
			case "wrong-refund", "failed-refund":
				_, e = s.QueryRefund(ctx, o, Refund{ID: "CASE-123", Amount: 6990})
			case "unsafe-url", "html":
				_, e = s.Create(ctx, o, "https://shop.test/notify")
			default:
				_, e = s.Query(ctx, o)
			}
			if e == nil {
				t.Fatal("accepted invalid gateway receipt")
			}
		})
	}
}
