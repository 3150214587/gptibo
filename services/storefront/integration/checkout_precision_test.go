package integration_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"gptibo/storefront/internal/delivery"
	"gptibo/storefront/internal/httpapi"
	"gptibo/storefront/internal/orders"
	"gptibo/storefront/internal/payments"
	"gptibo/storefront/internal/procurement"
	"gptibo/storefront/internal/security"
)

func TestEmailStepChoiceAndFractionalGeminiFulfillment(t *testing.T) {
	owner, app, a, base, authMail := identityFixture(t)
	admin := login(t, base, authMail, "admin", "")
	ali := &channelFake{paymentFake: paymentFake{account: "merchant"}, id: "gatewaya-alipay"}
	wx := &channelFake{paymentFake: paymentFake{account: "merchant"}, id: "gatewaya-wxpay"}
	pay := payments.Service{Store: app, Provider: ali, Providers: map[string]payments.Provider{ali.ID(): ali, wx.ID(): wx}, Checkout: true, NotifyOrigin: testOrigin}
	sender := &frozenMail{}
	service := &procurement.Service{Store: app, Auth: a, Delivery: delivery.Service{Store: app, Sender: sender, Origin: testOrigin}, EmailEnabled: true, PaymentReady: true, NYEnabled: true, FX: 6.7}
	service.DeliverySupplier, _ = procurement.NewDeliverySupplier("fixture-key")
	purchases, productCalls := 0, 0
	charged, refunded, net := "0.445", "0", "0.445"
	service.DeliverySupplier.HTTP.Transport = rechargeTransport(func(r *http.Request) (*http.Response, error) {
		var body string
		switch r.URL.Path {
		case "/api/v1/products":
			productCalls++
			body = `{"services":[{"service_id":"DEMO_PRODUCT_A","name":"Gemini","price":0.445,"stock":5}]}`
		case "/api/v1/me":
			body = `{"wallet_balance":20.123456}`
		case "/api/v1/order":
			purchases++
			body = `{"order_id":"original-gemini","order_status":"success"}`
		case "/api/v1/order/original-gemini":
			body = fmt.Sprintf(`{"order":{"order_id":"original-gemini","service_id":"DEMO_PRODUCT_A","requested_quantity":1,"order_status":"success","charged_amount":%s,"refunded_amount":%s,"net_charged_amount":%s,"delivered_quantity":1,"partial_delivery":false,"delivered_products":["http://claim.example.invalid/claim/fixture?token=a%%2Fb"]}}`, charged, refunded, net)
		default:
			t.Fatal("unexpected supplier call", r.URL.Path)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	execSQL(t, owner, `INSERT INTO catalog_plans(id,product_id,name,price_cents,enabled,delivery_instructions) VALUES('gemini-pro-18m','gemini','Gemini',9900,true,'领取说明')`)
	h := httpapi.Router(app, "fixture", slog.New(slog.NewTextHandler(io.Discard, nil)), httpapi.Options{Origin: testOrigin, Auth: a, MailSender: sender, Payments: &pay, Procurement: service})
	object(t, request(h, "POST", "/api/admin/payments/settings", map[string]any{"providers": []string{ali.ID(), wx.ID()}, "version": 0}, admin), 200)
	catalog := object(t, request(h, "GET", "/api/catalog", nil, nil), 200)
	if len(catalog["paymentMethods"].([]any)) != 2 || productCalls != 0 {
		t.Fatal("catalog choices missing or requested supplier")
	}
	buyer := &http.Cookie{Name: "__Host-sf_buyer", Value: security.Token()}
	ctx := context.Background()
	for _, method := range []string{"alipay", "wxpay"} {
		in := map[string]any{"plan": "gemini-pro-18m", "email": "buyer@example.test", "key": security.ID(), "expectedPrice": 9900, "paymentMethod": method}
		o := object(t, request(h, "POST", "/api/orders", in, buyer), 201)["order"].(map[string]any)
		id := o["id"].(string)
		original := object(t, request(h, "POST", "/api/orders", in, buyer), 200)["order"].(map[string]any)
		if original["id"] != id {
			t.Fatal("retry created another order")
		}
		intent, e := payments.Find(ctx, app.DB, id, false)
		if e != nil || intent.Provider != "gatewaya-"+method || intent.QR != nil || ali.creates+wx.creates != 0 || purchases != 0 {
			t.Fatal("choice not atomically reserved", intent, e)
		}
		other := "wxpay"
		if method == "wxpay" {
			other = "alipay"
		}
		in["paymentMethod"] = other
		object(t, request(h, "POST", "/api/orders", in, buyer), 409)
		if method == "alipay" {
			continue
		}
		object(t, request(h, "POST", "/api/orders/"+id+"/payment", map[string]any{"action": "create"}, buyer), 200)
		if wx.creates != 1 || ali.creates != 0 {
			t.Fatal("checkout ignored saved choice")
		}
		wx.query = payments.Result{OrderID: id, Amount: 9900, Status: "paid", TradeNo: "WX-GEMINI"}
		object(t, request(h, "POST", "/api/orders/"+id+"/payment", map[string]any{"action": "query"}, buyer), 200)
		// Sub-cent refund and mismatched net must not be rounded away into success.
		for _, amounts := range [][3]string{{"0.600001", "0", "0.600001"}, {"0.445", "0.000001", "0.444999"}, {"0.445", "0", "0.444999"}} {
			charged, refunded, net = amounts[0], amounts[1], amounts[2]
			if e = service.Fulfill(ctx, id); e == nil {
				t.Fatal("invalid financial receipt accepted", amounts)
			}
			execSQL(t, owner, `UPDATE delivery_supplier_orders SET next_check_at=0 WHERE order_id=$1`, id)
		}
		charged, refunded, net = "0.445", "0", "0.445"
		if e = service.Fulfill(ctx, id); e != nil {
			t.Fatal(e)
		}
		var micros, cents, cny int64
		if e = owner.DB.QueryRow(ctx, `SELECT actual_usd_micros,actual_usd_cents,actual_cost_cents FROM delivery_supplier_orders WHERE order_id=$1`, id).Scan(&micros, &cents, &cny); e != nil || micros != 445000 || cents != 45 || cny != 298 {
			t.Fatal("cost rounded before conversion", micros, cents, cny, e)
		}
		done, e := orders.Get(ctx, app.DB, id, nil, false)
		if e != nil || done.Status != "success" || purchases != 1 || len(sender.bodies) != 1 {
			t.Fatal("fulfillment incomplete or duplicate", done.Status, purchases, len(sender.bodies), e)
		}
		var stored, delivered string
		if e = owner.DB.QueryRow(ctx, `SELECT n.value,d.value FROM delivery_supplier_orders n JOIN email_deliveries d ON d.order_id=n.order_id WHERE n.order_id=$1`, id).Scan(&stored, &delivered); e != nil || stored != "https://claim.example.invalid/claim/fixture?token=a%2Fb" || delivered != stored {
			t.Fatal("HTTPS claim not preserved through delivery", e)
		}
	}
	// Disabled choices reject the whole transaction, without orphaned orders.
	object(t, request(h, "POST", "/api/admin/payments/settings", map[string]any{"providers": []string{ali.ID()}, "version": 1}, admin), 200)
	for _, method := range []string{"wxpay", "invalid"} {
		key := security.ID()
		want := 409
		if method == "invalid" {
			want = 400
		}
		object(t, request(h, "POST", "/api/orders", map[string]any{"plan": "gemini-pro-18m", "email": "buyer@example.test", "key": key, "expectedPrice": 9900, "paymentMethod": method}, buyer), want)
		var count int
		if e := owner.DB.QueryRow(ctx, `SELECT count(*) FROM orders WHERE idempotency_key=$1`, key).Scan(&count); e != nil || count != 0 {
			t.Fatal("orphaned order", count, e)
		}
	}
}

func TestSoldOutCheckoutRollsBackAndCanRetryAfterRestock(t *testing.T) {
	owner, app, a, _, _ := identityFixture(t)
	provider := &paymentFake{account: "fixture"}
	pay := payments.Service{Store: app, Provider: provider, Checkout: true, NotifyOrigin: testOrigin}
	sender := &frozenMail{}
	service := &procurement.Service{EmailEnabled: true, NYEnabled: true, FX: 6.7}
	service.DeliverySupplier, _ = procurement.NewDeliverySupplier("fixture-key")
	stock, availability, price := 0, "sold_out", "0.41"
	service.DeliverySupplier.HTTP.Transport = rechargeTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" || r.URL.Path != "/api/v1/products" {
			t.Fatal("checkout must not purchase from supplier", r.Method, r.URL.Path)
		}
		body := fmt.Sprintf(`{"services":[{"service_id":"DEMO_PRODUCT_A","name":"Gemini","price":%s,"stock":%d,"availability_state":%q}]}`, price, stock, availability)
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	execSQL(t, owner, `INSERT INTO catalog_plans(id,product_id,name,price_cents,enabled) VALUES('gemini-pro-18m','gemini','Gemini',9900,true)`)
	h := httpapi.Router(app, "fixture", slog.New(slog.NewTextHandler(io.Discard, nil)), httpapi.Options{Origin: testOrigin, Auth: a, MailSender: sender, Payments: &pay, Procurement: service})
	buyer := &http.Cookie{Name: "__Host-sf_buyer", Value: security.Token()}
	in := map[string]any{"plan": "gemini-pro-18m", "email": "buyer@example.test", "key": security.ID(), "expectedPrice": 9900}
	for _, tc := range []struct {
		stock               int
		availability, price string
		status              int
		message             string
	}{
		{0, "sold_out", "0.41", 409, "商品暂时缺货，请稍后再试。"},
		{0, "available", "0.41", 409, "商品暂时缺货，请稍后再试。"},
		{10, "sold_out", "0.41", 409, "商品暂时缺货，请稍后再试。"},
		{10, "available", "0.600001", 503, "服务暂时不可用，请稍后重试。"},
	} {
		stock, availability, price = tc.stock, tc.availability, tc.price
		out := object(t, request(h, "POST", "/api/orders", in, buyer), tc.status)
		if out["error"] != tc.message {
			t.Fatalf("unexpected checkout message: %v", out["error"])
		}
		var count int
		if err := owner.DB.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM orders)+(SELECT count(*) FROM order_events)+(SELECT count(*) FROM delivery_supplier_orders)+(SELECT count(*) FROM payments)`).Scan(&count); err != nil || count != 0 {
			t.Fatal("failed checkout left records", count, err)
		}
	}
	stock, availability, price = 10, "available", "0.41"
	created := object(t, request(h, "POST", "/api/orders", in, buyer), 201)["order"].(map[string]any)
	replayed := object(t, request(h, "POST", "/api/orders", in, buyer), 200)["order"].(map[string]any)
	if created["id"] != replayed["id"] || len(sender.bodies) != 0 {
		t.Fatal("restock retry duplicated order or sent mail")
	}
}
