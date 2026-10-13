package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"gptibo/storefront/internal/delivery"
	"gptibo/storefront/internal/httpapi"
	"gptibo/storefront/internal/orders"
	"gptibo/storefront/internal/payments"
	"gptibo/storefront/internal/procurement"
	"gptibo/storefront/internal/security"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestProcurementRecoversOriginalPurchaseAndFreezesDelivery(t *testing.T) {
	for _, kind := range []string{"gmail", "delivery_supplier"} {
		t.Run(kind, func(t *testing.T) {
			owner, app, a, base, authMail := identityFixture(t)
			admin := login(t, base, authMail, "admin", "")
			sender := &frozenMail{fail: true}
			pay := payments.Service{Store: app, Provider: &paymentFake{account: "fixture"}, Checkout: true, NotifyOrigin: testOrigin}
			service := &procurement.Service{Store: app, Auth: a, Delivery: delivery.Service{Store: app, Sender: sender, Origin: testOrigin}, EmailEnabled: true, PaymentReady: true, GmailEnabled: true, NYEnabled: true, GoodsID: 123, MaxCents: 400, FX: 7.026315789}
			createCalls, queryCalls := 0, 0
			ambiguous := true
			var bodies, keys []string
			plan := "gmail-account"
			if kind == "gmail" {
				service.AccountSupplier, _ = procurement.NewAccountSupplier("https://gmail.example.test", "12345", "fixture-secret")
				service.AccountSupplier.HTTP.Transport = rechargeTransport(func(r *http.Request) (*http.Response, error) {
					payload, _ := io.ReadAll(r.Body)
					var data any
					if r.Header.Get("X-Signature") != procurement.LegacySignature("12345", "fixture-secret", r.Header.Get("X-Timestamp"), string(payload)) {
						t.Fatal("invalid legacy signature")
					}
					switch r.URL.Path {
					case "/api/v3/goods/getDetail":
						data = map[string]any{"goodsId": 123, "name": "Gmail", "salesPrice": 3, "goodsType": 1, "status": 1, "skuType": 0, "stockCount": 10}
					case "/api/v3/order/create":
						createCalls++
						if ambiguous {
							return nil, errors.New("timeout after purchase")
						}
						data = map[string]any{"orderNumber": "original-gmail"}
					case "/api/v3/order/getDetail":
						queryCalls++
						var args map[string]any
						_ = json.Unmarshal(payload, &args)
						data = map[string]any{"orderNumber": "original-gmail", "outerNumber": args["outerNumber"], "count": 1, "status": 3, "money": 3, "retMoney": 0, "cards": []any{map[string]any{"cardId": "card-one", "cardNo": "delivered@example.test", "cardPwd": "fixture-password", "status": 0}}}
					default:
						t.Fatal("unexpected supplier path", r.URL.Path)
					}
					b, _ := json.Marshal(map[string]any{"code": 1000, "data": data})
					return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(string(b)))}, nil
				})
			} else {
				plan = "duolingo-12m"
				service.DeliverySupplier, _ = procurement.NewDeliverySupplier("fixture-key")
				service.DeliverySupplier.HTTP.Transport = rechargeTransport(func(r *http.Request) (*http.Response, error) {
					var data any
					switch r.URL.Path {
					case "/api/v1/products":
						data = map[string]any{"services": []any{map[string]any{"service_id": "DEMO_PRODUCT_B", "name": "Duolingo", "price": 0.38, "stock": 2}}}
					case "/api/v1/me":
						data = map[string]any{"wallet_balance": 20, "username": "fixture"}
					case "/api/v1/order":
						createCalls++
						b, _ := io.ReadAll(r.Body)
						bodies = append(bodies, string(b))
						keys = append(keys, r.Header.Get("Idempotency-Key"))
						if ambiguous {
							return nil, errors.New("timeout after charge")
						}
						data = map[string]any{"order_id": "original-ny", "order_status": "success"}
					case "/api/v1/order/original-ny":
						queryCalls++
						data = map[string]any{"order": map[string]any{"order_id": "original-ny", "service_id": "DEMO_PRODUCT_B", "requested_quantity": 1, "order_status": "success", "charged_amount": 0.38, "refunded_amount": 0, "net_charged_amount": 0.38, "delivered_quantity": 1, "partial_delivery": false, "delivered_products": []string{"FIXTURE-CDK"}}}
					default:
						t.Fatal("unexpected supplier path", r.URL.Path)
					}
					b, _ := json.Marshal(data)
					return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(string(b)))}, nil
				})
			}
			execSQL(t, owner, `INSERT INTO catalog_plans(id,product_id,name,price_cents,enabled,delivery_instructions) VALUES($1,'fixture','Fixture',1000,true,'领取说明')`, plan)
			h := httpapi.Router(app, "fixture", slog.New(slog.NewTextHandler(io.Discard, nil)), httpapi.Options{Origin: testOrigin, Auth: a, MailSender: sender, Payments: &pay, Procurement: service})
			buyer := &http.Cookie{Name: "__Host-sf_buyer", Value: security.Token()}
			out := object(t, request(h, "POST", "/api/orders", map[string]any{"plan": plan, "email": "buyer@example.test", "key": security.ID(), "expectedPrice": 1000}, buyer), 201)["order"].(map[string]any)
			id := out["id"].(string)
			execSQL(t, owner, `UPDATE orders SET payment_status='paid',status='session' WHERE id=$1`, id)
			if e := service.Fulfill(context.Background(), id); e == nil || createCalls != 1 {
				t.Fatal("unknown purchase lost", e)
			}
			if kind == "delivery_supplier" {
				execSQL(t, owner, `UPDATE delivery_supplier_orders SET next_check_at=0 WHERE order_id=$1`, id)
			}
			ambiguous = false
			if e := service.Fulfill(context.Background(), id); e == nil {
				t.Fatal("mail timeout missing")
			}
			if kind == "gmail" && createCalls != 1 {
				t.Fatal("AccountSupplier create retried")
			}
			if kind == "delivery_supplier" && (createCalls != 2 || keys[0] != keys[1] || bodies[0] != bodies[1]) {
				t.Fatal("DeliverySupplier changed original purchase request")
			}
			sender.fail = false
			if kind == "delivery_supplier" {
				execSQL(t, owner, `UPDATE delivery_supplier_orders SET next_check_at=0 WHERE order_id=$1`, id)
			}
			// Duplicate wakeups and periodic scans may race for the same order.
			var concurrent sync.WaitGroup
			errorsSeen := make(chan error, 8)
			for range 8 {
				concurrent.Add(1)
				go func() { defer concurrent.Done(); errorsSeen <- service.Fulfill(context.Background(), id) }()
			}
			concurrent.Wait()
			close(errorsSeen)
			for err := range errorsSeen {
				if err != nil {
					t.Fatal(err)
				}
			}
			if queryCalls != 1 || len(sender.bodies) != 2 || sender.bodies[0] != sender.bodies[1] {
				t.Fatal("purchase or delivery repeated")
			}
			o, e := orders.Get(context.Background(), app.DB, id, nil, false)
			if e != nil || o.Status != "success" {
				t.Fatal("delivery not completed", e)
			}
			suffix := "account_supplier"
			if kind == "delivery_supplier" {
				suffix = "delivery_supplier"
			}
			view := object(t, request(h, "GET", "/api/admin/orders/"+id+"/"+suffix, nil, admin), 200)["purchase"].(map[string]any)
			want := float64(300)
			if kind == "delivery_supplier" {
				want = 267
			}
			if view["actualCostCents"] != want {
				t.Fatal("cost snapshot wrong", view["actualCostCents"])
			}
			body, _ := json.Marshal(view)
			if strings.Contains(string(body), "fixture-password") || strings.Contains(string(body), "FIXTURE-CDK") {
				t.Fatal("supplier credentials leaked through admin summary")
			}
			object(t, request(h, "POST", "/api/admin/orders/"+id+"/"+suffix, map[string]any{"action": "confirm-not-purchased", "confirmed": true, "orderVersion": o.Version, "updatedAt": view["updatedAt"], "note": "不允许撤销已完成采购"}, admin), 409)
		})
	}
}
func TestDeliverySupplierReceiptRecoveryRequiresExactRejectedAttempt(t *testing.T) {
	owner, app, a, h, authMail := identityFixture(t)
	admin := login(t, h, authMail, "admin", "")
	client, _ := procurement.NewDeliverySupplier("fixture-key")
	client.HTTP.Transport = rechargeTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 400, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error_code":"INVALID_IDEMPOTENCY_KEY"}`))}, nil
	})
	sender := &frozenMail{}
	s := &procurement.Service{Store: app, Auth: a, DeliverySupplier: client, Delivery: delivery.Service{Store: app, Sender: sender, Origin: testOrigin}}
	now := time.Now().UnixMilli()
	execSQL(t, owner, `INSERT INTO catalog_plans(id,product_id,name,price_cents) VALUES('duolingo-12m','duolingo','Duolingo',1000)`)
	execSQL(t, owner, `INSERT INTO orders(id,owner_hash,idempotency_key,plan,plan_name,price_cents,mode,payment_status,status,created_at,updated_at,last_event_id) VALUES('YMX-OLD','owner','key','duolingo-12m','Duolingo',1000,'live','paid','processing',$1,$1,'fixture')`, now)
	execSQL(t, owner, `INSERT INTO delivery_supplier_orders(order_id,service_id,credential_hash,request_key,request_body,max_usd_cents,cny_per_usd,instructions,state,attempted_at,updated_at) VALUES('YMX-OLD','DEMO_PRODUCT_B',$1,'ymx/YMX-OLD','{"service_id":"DEMO_PRODUCT_B","quantity":1}',38,7,'说明','unknown',$2,$2)`, security.Hash(client.Key), now)
	if e := s.Fulfill(context.Background(), "YMX-OLD"); e == nil {
		t.Fatal("explicit rejection lost")
	}
	v, e := s.View(context.Background(), "delivery_supplier", "YMX-OLD")
	if e != nil {
		t.Fatal(e)
	}
	updated := v["purchase"].(map[string]any)["updatedAt"].(int64)
	version := int64(1)
	e = s.Act(context.Background(), "delivery_supplier", "YMX-OLD", admin.Value, procurement.AdminInput{Action: "retry-rejected-receipt", Confirmed: true, OrderVersion: &version, Updated: &updated, Note: "已核对原编号明确被拒绝且未采购"})
	if e != nil {
		t.Fatal(e)
	}
	var key, state string
	if e = owner.DB.QueryRow(context.Background(), `SELECT request_key,state FROM delivery_supplier_orders WHERE order_id='YMX-OLD'`).Scan(&key, &state); e != nil || key != "ymx_YMX-OLD" || state != "prepared" {
		t.Fatal("legacy receipt not repaired", e)
	}
	e = s.Act(context.Background(), "delivery_supplier", "YMX-OLD", admin.Value, procurement.AdminInput{Action: "retry-rejected-receipt", Confirmed: true, OrderVersion: &version, Updated: &updated, Note: "重复修正不应再次生效"})
	if e == nil {
		t.Fatal("stale recovery accepted")
	}
}
