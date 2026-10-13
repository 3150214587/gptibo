package integration_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gptibo/storefront/internal/delivery"
	"gptibo/storefront/internal/domains"
	"gptibo/storefront/internal/httpapi"
	"gptibo/storefront/internal/security"
	"gptibo/storefront/internal/recharge"
)

func TestCheckoutPollRecoversQRAndAdvancesPaymentAndRecharge(t *testing.T) {
	for _, custom := range []bool{false, true} {
		name := "primary"
		if custom {
			name = "custom-domain"
		}
		t.Run(name, func(t *testing.T) { checkoutPollFlow(t, custom) })
	}
}

func checkoutPollFlow(t *testing.T, custom bool) {
	owner, app, auth, _, _ := identityFixture(t)
	client, fake := newRechargeFake(t)
	// Fake transport responds immediately; inspect the enclosing API deadline.
	client.HTTP.Timeout = 0
	link := "https://supplier.example.test/payments/original?token=abcdefghijklmnop"
	fake.order.URL = &link
	cashierCalls, starts := 0, 0
	failCashier := true
	client.HTTP.Transport = rechargeTransport(func(r *http.Request) (*http.Response, error) {
		// Check the actual HTTP guard budget without sleeping for a real timeout.
		if deadline, ok := r.Context().Deadline(); !ok || time.Until(deadline) < 30*time.Second {
			t.Error("supplier chain still has the short generic request deadline")
		}
		if strings.HasPrefix(r.URL.Path, "/payments/") {
			cashierCalls++
			if failCashier {
				return nil, errors.New("temporary cashier failure")
			}
			var qr any
			if strings.HasSuffix(r.URL.Path, "/start") {
				starts++
				qr = "https://qr.alipay.com/abcdefghijklmnop"
			}
			body := recharge.Raw(map[string]any{"status": "pending", "amount": fake.order.Sale, "expires_at": fake.order.Expires, "expired": false, "can_start": true, "qr_code": qr, "delivery_mode": fake.order.Delivery})
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
		}
		return fake.round(r)
	})
	supplier := &recharge.Service{CredentialSecret: strings.Repeat("ab", 32), Store: app, Client: client, Checkout: true, Products: map[string]string{"plus": "plus"}, Notify: testOrigin + "/api/recharge/webhook"}
	opts := httpapi.Options{Origin: testOrigin, Auth: auth, Recharge: supplier}
	execSQL(t, owner, `INSERT INTO catalog_plans(id,product_id,name,price_cents,enabled,partner_enabled) VALUES('plus','chatgpt','Plus',10000,true,true)`)
	if custom {
		execSQL(t, owner, `INSERT INTO partners(id,slug,name,email,active) VALUES('a','ABC123','A','a@example.test',true);
INSERT INTO partner_notification_settings(partner_id,order_completed_email,version) VALUES('a',true,1);
INSERT INTO partner_prices(partner_id,plan,price_cents,cost_cents) VALUES('a','plus',10000,7000);
INSERT INTO partner_domains(partner_id,hostname,verification,provider_id,state,checked_at) VALUES('a','shop.example.com','verified','cf-domain','active',now())`)
		opts.Domains = &domains.Service{Store: app, Provider: &domainProvider{}, Target: "shops.shop.example.invalid", Zone: "shop.example.invalid", Limit: 100}
		opts.TrustedProxies = []string{"127.0.0.1/32"}
	}
	h := httpapi.Router(app, "fixture", slog.New(slog.NewTextHandler(io.Discard, nil)), opts)
	// Exercise the same checkout through the trusted custom-domain ingress.
	call := func(method, path string, body any, cookie *http.Cookie) *httptest.ResponseRecorder {
		wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if custom {
				r.RemoteAddr = "127.0.0.1:1234"
				r.Header.Set("X-Storefront-Host", "shop.example.com")
				r.Header.Set("Origin", "https://shop.example.com")
			}
			h.ServeHTTP(w, r)
		})
		response := request(wrapped, method, path, body, cookie)
		if strings.Contains(response.Body.String(), "supplier.example.test") || strings.Contains(response.Body.String(), "token=abcdefghijklmnop") {
			t.Fatal("customer response exposed supplier cashier URL/token")
		}
		return response
	}
	buyer := &http.Cookie{Name: "__Host-sf_buyer", Value: security.Token()}
	created := object(t, call("POST", "/api/orders", map[string]any{"plan": "plus", "email": "buyer@example.test", "key": security.ID(), "expectedPrice": 10000}, buyer), 201)["order"].(map[string]any)
	id := created["id"].(string)
	path := "/api/orders/" + id + "/fulfillment"
	poll := func() map[string]any {
		t.Helper()
		return object(t, call("POST", path, map[string]any{"action": "poll"}, buyer), 200)["order"].(map[string]any)
	}
	// URL is saved before the cashier fails. Polling must retry this original order.
	object(t, call("POST", path, map[string]any{"action": "poll"}, buyer), 503)
	failCashier = false
	view := poll()
	if view["fulfillment"].(map[string]any)["paymentQr"] == nil || fake.createCalls != 1 || starts != 1 {
		t.Fatal("missing QR did not recover on original order")
	}
	if view["fulfillment"].(map[string]any)["paymentUrl"] != nil {
		t.Fatal("supplier URL published")
	}
	object(t, call("GET", "/api/orders/"+id, nil, buyer), 200)
	object(t, call("GET", "/api/orders", nil, buyer), 200)
	var savedURL string
	if err := owner.DB.QueryRow(context.Background(), `SELECT payment_url FROM recharge_orders WHERE order_id=$1`, id).Scan(&savedURL); err != nil || savedURL != link {
		t.Fatal("server lost original cashier required for recovery", err)
	}
	// A worker holding the lease is a normal poll response, not a user error.
	execSQL(t, owner, `UPDATE recharge_orders SET lease_until=$1 WHERE order_id=$2`, time.Now().Add(time.Minute).UnixMilli(), id)
	poll()
	execSQL(t, owner, `UPDATE recharge_orders SET lease_until=0,updated_at=0 WHERE order_id=$1`, id)
	calls := cashierCalls
	failCashier = true
	view = poll()
	if cashierCalls != calls || view["fulfillment"].(map[string]any)["paymentQr"] == nil {
		t.Fatal("valid cached QR was fetched again or discarded")
	}
	// A changed expiry invalidates the cached QR and fetches a newly verified one.
	failCashier = false
	fake.order.Expires = time.Now().Add(10 * time.Minute).UTC().Format(time.RFC3339Nano)
	execSQL(t, owner, `UPDATE recharge_orders SET updated_at=0 WHERE order_id=$1`, id)
	poll()
	if starts != 2 {
		t.Fatal("changed cashier binding reused stale QR")
	}
	fake.order.Payment = "paid"
	execSQL(t, owner, `UPDATE recharge_orders SET updated_at=0 WHERE order_id=$1`, id)
	view = poll()
	if view["paymentStatus"] != "paid" || view["status"] != "session" || view["fulfillment"].(map[string]any)["paymentQr"] != nil {
		t.Fatal("poll did not confirm payment and clear the QR")
	}
	object(t, call("POST", path, map[string]any{"action": "submit", "key": security.ID(), "customerConfirmedEmail": true, "credential": map[string]any{"mode": "access_token", "access_token": "fixture-token"}}, buyer), 200)
	stage, version := "confirming", int64(2)
	fake.redemption.Stage, fake.redemption.Version = &stage, &version
	execSQL(t, owner, `UPDATE recharge_orders SET updated_at=0 WHERE order_id=$1`, id)
	view = poll()
	if view["fulfillment"].(map[string]any)["progressStage"] != stage {
		t.Fatal("poll did not advance recharge progress")
	}
	fake.redemption.Status = "succeeded"
	stage, version = "completed", 3
	execSQL(t, owner, `UPDATE recharge_orders SET updated_at=0 WHERE order_id=$1`, id)
	if poll()["status"] != "success" || fake.submitCalls != 1 || fake.createCalls != 1 {
		t.Fatal("poll did not complete original recharge without duplicate submission")
	}
	object(t, call("POST", path, map[string]any{"action": "poll"}, &http.Cookie{Name: buyer.Name, Value: security.Token()}), 404)
	if custom {
		object(t, request(h, "POST", path, map[string]any{"action": "poll"}, buyer), 404)
		// Domain attribution must also drive the new completed-order email outbox.
		var partnerID, recipient string
		if err := app.DB.QueryRow(context.Background(), `SELECT partner_id,recipient FROM partner_order_notifications WHERE order_id=$1`, id).Scan(&partnerID, &recipient); err != nil || partnerID != "a" || recipient != "a@example.test" {
			t.Fatal("custom-domain completion notified the wrong partner", err)
		}
		mail := &frozenMail{}
		notifications := delivery.Service{Store: app, Sender: mail, Origin: testOrigin}
		result, err := notifications.PartnerOrderNotifications(context.Background(), 20)
		if err != nil || result["sent"] != 1 || len(mail.bodies) != 1 || !strings.Contains(mail.bodies[0], testOrigin+"/join?tab=orders") {
			t.Fatal("custom-domain order notification did not use the platform workspace", result, err)
		}
		result, err = notifications.PartnerOrderNotifications(context.Background(), 20)
		if err != nil || result["sent"] != 0 || len(mail.bodies) != 1 {
			t.Fatal("duplicate custom-domain order notification", result, err)
		}
	}
	var qr *string
	if err := owner.DB.QueryRow(context.Background(), `SELECT payment_qr FROM recharge_orders WHERE order_id=$1`, id).Scan(&qr); err != nil || qr != nil {
		t.Fatal("paid order retained QR", err)
	}
}
