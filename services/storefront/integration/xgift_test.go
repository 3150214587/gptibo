package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"gptibo/storefront/internal/httpapi"
	"gptibo/storefront/internal/orders"
	"gptibo/storefront/internal/payments"
	"gptibo/storefront/internal/security"
	"gptibo/storefront/internal/recharge"
	"gptibo/storefront/internal/xgift"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"
)

type giftFake struct {
	backend, merchant, recipient, status, failure, receipt, stage string
	points                                                        int64
	createCalls, queryCalls                                       int
	ambiguous                                                     bool
}

func (f *giftFake) round(r *http.Request) (*http.Response, error) {
	var data any
	switch {
	case r.URL.Path == "/v1/products":
		if f.backend == "legacy" {
			data = []any{map[string]any{"code": "x-premium-3m", "name": "X3", "points": 1700, "enabled": 1}, map[string]any{"code": "x-premium-6m", "name": "X6", "points": 3400, "enabled": 1}}
		} else {
			data = []any{map[string]any{"code": "x-3m", "name": "X3", "business": "x", "supply_minor": 1700, "enabled": true, "available": true}, map[string]any{"code": "x-6m", "name": "X6", "business": "x", "supply_minor": 3400, "enabled": true, "available": true}}
		}
	case r.URL.Path == "/v1/balance":
		if f.backend == "legacy" {
			data = map[string]any{"available": 100000, "frozen": 0}
		} else {
			data = map[string]any{"currency": "CNY", "available_minor": 100000, "frozen_minor": 0}
		}
	case r.URL.Path == "/v1/capabilities":
		data = map[string]any{"execution_ready": true, "accepts_orders": true}
	case r.URL.Path == "/v1/eligibility" || r.URL.Path == "/v1/preflight":
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		username := in["username"]
		if f.backend == "platform" {
			username = in["recipient"]
		}
		data = map[string]any{"username": username, "recipient": username, "recipient_id": "123456789", "eligible": true}
	case r.URL.Path == "/v1/orders" && r.Method == "POST":
		f.createCalls++
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		f.merchant = in["merchant_order_no"].(string)
		f.recipient = in["recipient"].(string)
		if f.ambiguous {
			return nil, errors.New("lost response after supplier purchase")
		}
		data = f.result()
	case (r.URL.Path == "/v1/orders" && r.Method == "GET") || strings.HasPrefix(r.URL.Path, "/v1/orders/by-merchant/"):
		f.queryCalls++
		data = f.result()
	default:
		return &http.Response{StatusCode: 404, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"order_missing"}}`))}, nil
	}
	return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(recharge.Raw(map[string]any{"data": data})))}, nil
}
func (f *giftFake) result() map[string]any {
	out := map[string]any{"id": "ord_" + strings.Repeat("a", 32), "merchant_order_no": f.merchant, "recipient": f.recipient, "status": f.status, "receipt": nil, "failure_code": nil}
	if f.receipt != "" {
		out["receipt"] = f.receipt
	}
	if f.failure != "" {
		out["failure_code"] = f.failure
	}
	if f.backend == "platform" {
		out["stage"] = f.stage
		out["business"] = "x"
		out["product_code"] = "x-3m"
		out["supply_minor"] = f.points
		out["created_at"] = time.Now().UTC().Format(time.RFC3339Nano)
		out["updated_at"] = out["created_at"]
	} else {
		out["product_code"] = "x-premium-3m"
		out["points"] = f.points
		out["currency"] = "bdt"
		out["amount_minor"] = 30000
		out["created_at"] = time.Now().UnixMilli()
		out["updated_at"] = out["created_at"]
	}
	return out
}
func TestXGiftOriginalIdentityPreflightAndRecovery(t *testing.T) {
	for _, scenario := range []string{"legacy", "platform", "platform-price-change"} {
		t.Run(scenario, func(t *testing.T) {
			backend := scenario
			if scenario == "platform-price-change" {
				backend = "platform"
			}
			owner, app, a, base, mail := identityFixture(t)
			admin := login(t, base, mail, "admin", "")
			client, e := xgift.NewClient("https://gift.example.test/v1", backend, "tenant", "key", "fixture-secret")
			if e != nil {
				t.Fatal(e)
			}
			fake := &giftFake{backend: backend, status: "queued", points: 1700, ambiguous: true}
			client.HTTP.Transport = rechargeTransport(fake.round)
			s := &xgift.Service{Store: app, Auth: a, Backend: backend, Checkout: true, PaymentReady: true, Clients: map[string]*xgift.Client{backend: client}}
			pay := payments.Service{Store: app, Provider: &paymentFake{account: "fixture"}, Checkout: true, NotifyOrigin: testOrigin}
			h := httpapi.Router(app, "fixture", slog.New(slog.NewTextHandler(io.Discard, nil)), httpapi.Options{Origin: testOrigin, Auth: a, Payments: &pay, XGift: s})
			execSQL(t, owner, `INSERT INTO catalog_plans(id,product_id,name,price_cents,enabled) VALUES('x-premium-3m','x-premium','X3',3500,true)`)
			buyer := &http.Cookie{Name: "__Host-sf_buyer", Value: security.Token()}
			pre := object(t, request(h, "POST", "/api/orders/xgift/eligibility", map[string]any{"plan": "x-premium-3m", "username": "@Fixture_User"}, buyer), 200)
			in := map[string]any{"plan": "x-premium-3m", "username": "fixture_user", "preflightToken": pre["token"], "email": "buyer@example.test", "key": security.ID(), "expectedPrice": 3500}
			object(t, request(h, "POST", "/api/orders", in, &http.Cookie{Name: buyer.Name, Value: security.Token()}), 409)
			created := object(t, request(h, "POST", "/api/orders", in, buyer), 201)["order"].(map[string]any)
			id := created["id"].(string)
			in["username"] = "other_user"
			object(t, request(h, "POST", "/api/orders", in, buyer), 409)
			execSQL(t, owner, `UPDATE orders SET payment_status='paid',status='session' WHERE id=$1`, id)
			if e = s.Fulfill(context.Background(), id); e == nil || fake.createCalls != 1 {
				t.Fatal("purchase timeout lost", e)
			}
			fake.ambiguous = false
			fake.points = 1701
			execSQL(t, owner, `UPDATE xgift_purchases SET next_check=0 WHERE order_id=$1`, id)
			if e = s.Fulfill(context.Background(), id); e == nil || fake.createCalls != 1 {
				t.Fatal("mismatched original purchase accepted", e)
			}
			fake.points = 1700
			fake.status = "succeeded"
			fake.receipt = "invalid"
			execSQL(t, owner, `UPDATE xgift_purchases SET next_check=0 WHERE order_id=$1`, id)
			if e = s.Fulfill(context.Background(), id); e == nil {
				t.Fatal("invalid success receipt accepted")
			}
			if backend == "platform" {
				fake.status = "failed"
				fake.receipt = ""
				fake.failure = "card_wallet_insufficient"
				if scenario == "platform-price-change" {
					fake.failure = "x_price_changed"
				}
				execSQL(t, owner, `UPDATE xgift_purchases SET next_check=0 WHERE order_id=$1`, id)
				if e = s.Fulfill(context.Background(), id); e != nil {
					t.Fatal(e)
				}
				o, e := orders.Get(context.Background(), app.DB, id, nil, false)
				if e != nil || o.Status != "failed" {
					t.Fatal("known failure lost", e)
				}
				if scenario == "platform-price-change" {
					for _, stage := range []string{"", "payment", "purchase"} {
						fake.stage = stage
						object(t, request(h, "POST", "/api/admin/orders/"+id+"/actions", map[string]any{"action": "xgift-recovery", "version": o.Version, "note": "不明付款阶段禁止补发"}, admin), 409)
					}
					fake.stage = "preflight"
				}
				object(t, request(h, "POST", "/api/admin/orders/"+id+"/actions", map[string]any{"action": "xgift-recovery", "version": o.Version, "note": "核实原单尚未付款后恢复"}, admin), 200)
				var history int
				if e = owner.DB.QueryRow(context.Background(), `SELECT count(*) FROM xgift_recovery_attempts WHERE order_id=$1`, id).Scan(&history); e != nil || history != 1 {
					t.Fatal("recovery history not saved", e)
				}
				object(t, request(h, "POST", "/api/admin/orders/"+id+"/actions", map[string]any{"action": "xgift-recovery", "version": o.Version, "note": "旧版本不得重复补发"}, admin), 409)
				fake.status = "succeeded"
				fake.failure = ""
				fake.receipt = "rcpt_" + strings.Repeat("b", 24)
			} else {
				fake.receipt = "cs_live_fixture123"
			}
			execSQL(t, owner, `UPDATE xgift_purchases SET next_check=0 WHERE order_id=$1`, id)
			if e = s.Fulfill(context.Background(), id); e != nil {
				t.Fatal(e)
			}
			o, e := orders.Get(context.Background(), app.DB, id, nil, false)
			if e != nil || o.Status != "success" {
				t.Fatal("gift success not applied", e)
			}
			expected := 1
			if backend == "platform" {
				expected = 2
				if fake.merchant != id+":retry:1" {
					t.Fatal("recovery did not preserve new audited merchant")
				}
			}
			if fake.createCalls != expected {
				t.Fatal("duplicated gift purchase")
			}
		})
	}
}
