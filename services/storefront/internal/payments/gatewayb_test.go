package payments

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func zFixture(t *testing.T, kind string) *GATEWAYB {
	t.Helper()
	z, e := NewGATEWAYB(GATEWAYBConfig{PID: "1001", Key: strings.Repeat("test", 8), Type: kind, ChannelID: "12,13"})
	if e != nil {
		t.Fatal(e)
	}
	return z
}
func zResponse(v any) *http.Response {
	b, _ := json.Marshal(v)
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(b)))}
}
func TestGATEWAYBCreateQueryAndRefundProtocol(t *testing.T) {
	for _, kind := range []string{"alipay", "wxpay"} {
		t.Run(kind, func(t *testing.T) {
			z := zFixture(t, kind)
			o := Order{ID: "YMX-DA23DC4DDA2C4B9FB568793768D25A15", Subject: "会员 + 商品 & 发货", Amount: 10140, TradeNo: "GATEWAYB-TRADE"}
			calls := 0
			z.client.Transport = snTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.URL.Scheme != "https" || req.URL.Host != "gateway-b.example.invalid" {
					t.Fatal("unexpected credential destination")
				}
				if req.Method == "GET" {
					v := req.URL.Query()
					if v.Get("act") != "order" || v.Get("pid") != "1001" || v.Get("key") != z.config.Key || v.Get("out_trade_no") != gatewaybOrderID(o.ID) {
						t.Fatal("invalid query")
					}
					return zResponse(map[string]any{"code": 1, "pid": 1001, "type": kind, "money": "101.40", "out_trade_no": gatewaybOrderID(o.ID), "trade_no": "GATEWAYB-TRADE", "status": 1}), nil
				}
				if e := req.ParseMultipartForm(65536); e != nil {
					t.Fatal(e)
				}
				v := req.PostForm
				if req.URL.Query().Get("act") == "refund" {
					if v.Get("money") != "50.70" || v.Get("key") != z.config.Key || v.Get("out_trade_no") != gatewaybOrderID(o.ID) {
						t.Fatal("refund amount/binding")
					}
					return zResponse(map[string]any{"code": 1, "msg": "退款成功"}), nil
				}
				if req.URL.Path != "/mapi.php" || len(v.Get("out_trade_no")) != 32 || v.Get("param") != o.ID || v.Get("money") != "101.40" || v.Get("name") != o.Subject || v.Get("type") != kind || v.Get("cid") != "12,13" || v.Get("clientip") != "203.0.113.4" {
					t.Fatal("invalid create form")
				}
				values := map[string]string{}
				for k := range v {
					values[k] = v.Get(k)
				}
				h := md5.Sum([]byte(snCanonical(values) + z.config.Key))
				if v.Get("sign_type") != "MD5" || v.Get("sign") != hex.EncodeToString(h[:]) {
					t.Fatal("signature canonicalization")
				}
				return zResponse(map[string]any{"code": 1, "trade_no": "GATEWAYB-TRADE", "qrcode": "https://cashier.example.test/qr"}), nil
			})
			qr, e := z.Create(WithClientIP(context.Background(), "203.0.113.4"), o, "https://storefront.example.test/api/payments/notify/"+z.ID())
			if e != nil || qr != "https://cashier.example.test/qr" || calls != 2 {
				t.Fatal(qr, e, calls)
			}
			r, e := z.Refund(context.Background(), o, Refund{ID: "case-1", Amount: 5070})
			if e != nil || !r.Confirmed || r.TradeNo != "GATEWAYB-TRADE" || calls != 4 {
				t.Fatal(r, e, calls)
			}
			if closed, e := z.Close(context.Background(), o); e != nil || closed || calls != 4 {
				t.Fatal("invented close API")
			}
			if _, e = z.QueryRefund(context.Background(), o, Refund{}); e == nil {
				t.Fatal("invented refund receipt")
			}
			o.TradeNo = "ANOTHER-TRADE"
			if _, e = z.Refund(context.Background(), o, Refund{ID: "case-2", Amount: 5070}); e == nil || calls != 5 {
				t.Fatal("refunded a different original trade", e, calls)
			}
		})
	}
}
func TestGATEWAYBPendingCheckoutWithoutPaymentTrade(t *testing.T) {
	for _, kind := range []string{"alipay", "wxpay"} {
		for _, scenario := range []string{"null", "empty", "missing", "matching", "wrong-trade", "wrong-merchant", "wrong-order", "wrong-amount", "wrong-method", "paid-without-trade"} {
			t.Run(kind+"/"+scenario, func(t *testing.T) {
				z := zFixture(t, kind)
				o := Order{ID: "YMX-PENDING", Amount: 2020, Subject: "Gemini Pro"}
				query := map[string]any{"code": 1, "pid": "1001", "type": kind, "money": "20.20", "out_trade_no": gatewaybOrderID(o.ID), "trade_no": nil, "status": 0}
				switch scenario {
				case "empty":
					query["trade_no"] = ""
				case "missing":
					delete(query, "trade_no")
				case "matching":
					query["trade_no"] = "GATEWAYB-CASHIER"
				case "wrong-trade":
					query["trade_no"] = "OTHER-TRADE"
				case "wrong-merchant":
					query["pid"] = "999"
				case "wrong-order":
					query["out_trade_no"] = "other"
				case "wrong-amount":
					query["money"] = "20.21"
				case "wrong-method":
					query["type"] = "other"
				case "paid-without-trade":
					query["status"] = 1
				}
				z.client.Transport = snTransport(func(req *http.Request) (*http.Response, error) {
					if req.Method == "GET" {
						return zResponse(query), nil
					}
					return zResponse(map[string]any{"code": 1, "trade_no": "GATEWAYB-CASHIER", "qrcode": "https://cashier.example.test/qr"}), nil
				})
				qr, err := z.Create(WithClientIP(context.Background(), "203.0.113.4"), o, "https://shop.test/notify")
				valid := scenario == "null" || scenario == "empty" || scenario == "missing" || scenario == "matching"
				if valid {
					if err != nil || qr != "https://cashier.example.test/qr" {
						t.Fatalf("unpaid checkout rejected: %v", err)
					}
					r, err := z.Query(context.Background(), o)
					if err != nil || r.Status != "pending" {
						t.Fatalf("pending query: %+v, %v", r, err)
					}
				} else if err == nil {
					t.Fatal("accepted mismatched or unverified checkout")
				}
			})
		}
	}
}

func TestGATEWAYBNotificationValidation(t *testing.T) {
	z := zFixture(t, "wxpay")
	id := "YMX-DA23DC4DDA2C4B9FB568793768D25A15"
	good := map[string]string{"pid": "1001", "type": "wxpay", "money": "101.40", "trade_no": "GATEWAYB-1", "out_trade_no": gatewaybOrderID(id), "param": id, "trade_status": "TRADE_SUCCESS", "name": "会员 + 商品 & 发货", "sign_type": "MD5"}
	for _, bad := range []string{"", "merchant", "type", "amount", "param", "wire", "status", "tamper", "duplicate", "missing-trade"} {
		t.Run(bad, func(t *testing.T) {
			v := map[string]string{}
			for k, s := range good {
				v[k] = s
			}
			switch bad {
			case "merchant":
				v["pid"] = "2"
			case "type":
				v["type"] = "alipay"
			case "amount":
				v["money"] = "1.001"
			case "param":
				v["param"] = "YMX-OTHER"
			case "wire":
				v["out_trade_no"] = "other"
			case "status":
				v["trade_status"] = "WAIT_BUYER_PAY"
			case "missing-trade":
				v["trade_no"] = ""
				v["status"] = "0"
			}
			h := md5.Sum([]byte(snCanonical(v) + z.config.Key))
			v["sign"] = hex.EncodeToString(h[:])
			if bad == "tamper" {
				v["money"] = "99.00"
			}
			values := url.Values{}
			for k, s := range v {
				values.Set(k, s)
			}
			if bad == "duplicate" {
				values.Add("money", "101.40")
			}
			r, e := z.Notification(httptest.NewRequest("GET", "https://storefront.example.test/notify?"+values.Encode(), nil))
			if bad == "" {
				if e != nil || r.OrderID != id || r.Amount != 10140 || r.Status != "paid" {
					t.Fatal(r, e)
				}
			} else if e == nil {
				t.Fatal("accepted", bad)
			}
		})
	}
}
func TestGATEWAYBQueryFailureNeverBecomesClosedAndSecretsAreRedacted(t *testing.T) {
	z := zFixture(t, "alipay")
	z.client.Transport = snTransport(func(req *http.Request) (*http.Response, error) { return nil, errors.New(req.URL.String()) })
	_, e := z.Query(context.Background(), Order{ID: "YMX-1", Amount: 100})
	if e == nil || strings.Contains(e.Error(), z.config.Key) || strings.Contains(e.Error(), "act=order") {
		t.Fatal("credential leaked")
	}
	for _, raw := range []string{`{"code":1,"code":1}`, `{"code":"error","msg":"not found"}`, `{"code":1,"pid":"1001","type":"alipay","money":"1.00","out_trade_no":"other","trade_no":"GATEWAYB-1","status":1}`} {
		z.client.Transport = snTransport(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(raw))}, nil
		})
		if _, e = z.Query(context.Background(), Order{ID: "YMX-1", Amount: 100}); e == nil {
			t.Fatal("unverified result accepted")
		}
	}
}
