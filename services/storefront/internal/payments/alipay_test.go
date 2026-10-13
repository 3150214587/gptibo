package payments

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func alipayFixture(t *testing.T) *Alipay {
	t.Helper()
	k, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	priv, e := x509.MarshalPKCS8PrivateKey(k)
	if e != nil {
		t.Fatal(e)
	}
	pub, e := x509.MarshalPKIXPublicKey(&k.PublicKey)
	if e != nil {
		t.Fatal(e)
	}
	a, e := NewAlipay(AlipayConfig{AppID: "1234567890", SellerID: "9876543210", PrivateKey: base64.StdEncoding.EncodeToString(priv), PublicKey: base64.StdEncoding.EncodeToString(pub)})
	if e != nil {
		t.Fatal(e)
	}
	return a
}
func TestAlipaySignedGatewayAndRawResponseBytes(t *testing.T) {
	a := alipayFixture(t)
	valid := true
	duplicate := false
	a.client.Transport = transport(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != a.config.Gateway || req.Method != "POST" {
			t.Fatal("wrong gateway")
		}
		if e := req.ParseForm(); e != nil {
			t.Fatal(e)
		}
		fields := map[string]string{}
		for k, v := range req.PostForm {
			fields[k] = v[0]
		}
		if !a.verify([]byte(canonical(fields, false)), fields["sign"]) {
			t.Fatal("outgoing signature invalid")
		}
		raw := `{ "code":"10000", "out_trade_no":"YMX-TEST", "qr_code":"https://qr.alipay.com/test", "ignored":"escaped \\" }`
		signature, e := a.sign(raw)
		if e != nil {
			t.Fatal(e)
		}
		if !valid {
			signature = "bad"
		}
		encoded, _ := json.Marshal(signature)
		envelope := `{"alipay_trade_precreate_response":` + raw + `,"sign":` + string(encoded) + `}`
		if duplicate {
			envelope = `{"alipay_trade_precreate_response":` + raw + `,"alipay_trade_precreate_response":` + raw + `,"sign":` + string(encoded) + `}`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(envelope))}, nil
	})
	order := Order{ID: "YMX-TEST", Amount: 123, Subject: "fixture", Expires: time.Now().UnixMilli() + 900000}
	if qr, e := a.Create(context.Background(), order, "https://fixture.example.test/api/payments/notify/alipay"); e != nil || qr != "https://qr.alipay.com/test" {
		t.Fatal("raw signed response rejected", e)
	}
	valid = false
	if _, e := a.Create(context.Background(), order, "https://fixture.example.test"); e == nil {
		t.Fatal("invalid signature accepted")
	}
	valid = true
	duplicate = true
	if _, e := a.Create(context.Background(), order, "https://fixture.example.test"); e == nil {
		t.Fatal("duplicate signed object accepted")
	}
}
func TestAlipayNotificationIdentityAmountAndDuplicateFields(t *testing.T) {
	a := alipayFixture(t)
	values := map[string]string{"app_id": a.config.AppID, "seller_id": a.config.SellerID, "out_trade_no": "YMX-TEST", "trade_no": "TRADE-1", "total_amount": "1.23", "trade_status": "TRADE_SUCCESS", "sign_type": "RSA2"}
	signed := func(extra string) *http.Request {
		sign, e := a.sign(canonical(values, true))
		if e != nil {
			t.Fatal(e)
		}
		v := url.Values{}
		for k, x := range values {
			v.Set(k, x)
		}
		v.Set("sign", sign)
		req := httptest.NewRequest("POST", "/api/payments/notify/alipay", strings.NewReader(v.Encode()+extra))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded;charset=UTF-8")
		return req
	}
	r, e := a.Notification(signed(""))
	if e != nil || r.Amount != 123 || r.Status != "paid" {
		t.Fatal("valid notification rejected", e)
	}
	if _, e = a.Notification(signed("&total_amount=1.23")); e == nil {
		t.Fatal("duplicate amount accepted")
	}
	values["seller_id"] = "1111"
	if _, e = a.Notification(signed("")); e == nil {
		t.Fatal("wrong seller accepted")
	}
	values["seller_id"] = a.config.SellerID
	for _, amount := range []string{"0", "-1", "1e2", "1.001", "01.00", "NaN", "100000.01"} {
		values["total_amount"] = amount
		if _, e = a.Notification(signed("")); e == nil {
			t.Fatal("invalid amount accepted", amount)
		}
	}
}
func TestAlipayQueryAndRefundIdentity(t *testing.T) {
	a := alipayFixture(t)
	body := `{"code":"10000","out_trade_no":"YMX-TEST","trade_no":"TRADE-1","total_amount":"1.23","trade_status":"TRADE_SUCCESS"}`
	a.client.Transport = transport(func(req *http.Request) (*http.Response, error) {
		_ = req.ParseForm()
		name := strings.ReplaceAll(req.PostForm.Get("method"), ".", "_") + "_response"
		signature, _ := a.sign(body)
		sign, _ := json.Marshal(signature)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"` + name + `":` + body + `,"sign":` + string(sign) + `}`))}, nil
	})
	o := Order{ID: "YMX-TEST", Amount: 123}
	r, e := a.Query(context.Background(), o)
	if e != nil || r.Status != "paid" {
		t.Fatal(e)
	}
	body = strings.ReplaceAll(body, "1.23", "1.24")
	if _, e = a.Query(context.Background(), o); e == nil {
		t.Fatal("wrong total accepted")
	}
	body = `{"code":"40004","sub_code":"ACQ.TRADE_NOT_EXIST"}`
	r, e = a.Query(context.Background(), o)
	if e != nil || r.Status != "not_found" {
		t.Fatal(e)
	}
	if closed, e := a.Close(context.Background(), o); e != nil || closed {
		t.Fatal("not found treated as closed")
	}
	body = `{"code":"10000","out_trade_no":"YMX-TEST","out_request_no":"CASE-1","trade_no":"TRADE-1","total_amount":"1.23","refund_amount":"1.00","refund_status":"REFUND_SUCCESS"}`
	f := Refund{ID: "CASE-1", Amount: 100}
	ref, e := a.QueryRefund(context.Background(), o, f)
	if e != nil || !ref.Confirmed {
		t.Fatal(e)
	}
	body = strings.ReplaceAll(body, "CASE-1", "CASE-2")
	if _, e = a.QueryRefund(context.Background(), o, f); e == nil {
		t.Fatal("refund identity mismatch accepted")
	}
}
