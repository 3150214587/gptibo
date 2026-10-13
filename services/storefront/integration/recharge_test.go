package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"gptibo/storefront/internal/httpapi"
	"gptibo/storefront/internal/management"
	"gptibo/storefront/internal/orders"
	"gptibo/storefront/internal/refunds"
	"gptibo/storefront/internal/security"
	"gptibo/storefront/internal/recharge"
)

type rechargeTransport func(*http.Request) (*http.Response, error)

func TestAdminStoredUpstreamOrderSearch(t *testing.T) {
	owner, app, a, base, mail := identityFixture(t)
	admin := login(t, base, mail, "admin", "")
	client, _ := newRechargeFake(t)
	supplier := &recharge.Service{CredentialSecret: strings.Repeat("ab", 32), Store: app, Client: client, Checkout: true, Products: map[string]string{"plus": "plus"}, Notify: "https://dev.example.test/api/recharge/webhook"}
	h := httpapi.Router(app, "fixture", slog.New(slog.NewTextHandler(io.Discard, nil)), httpapi.Options{Origin: testOrigin, Auth: a, Recharge: supplier})
	execSQL(t, owner, `INSERT INTO catalog_plans(id,product_id,name,price_cents,enabled) VALUES('plus','chatgpt','Plus',10000,true)`)
	buyer := &http.Cookie{Name: "__Host-sf_buyer", Value: security.Token()}
	created := object(t, request(h, "POST", "/api/orders", map[string]any{"plan": "plus", "email": "buyer@example.test", "key": security.ID(), "expectedPrice": 10000}, buyer), 201)["order"].(map[string]any)
	id := created["id"].(string)
	if e := supplier.Sync(context.Background(), id, true); e != nil {
		t.Fatal(e)
	}
	client.HTTP.Transport = rechargeTransport(func(*http.Request) (*http.Response, error) {
		t.Error("reading a stored upstream ID contacted the supplier")
		return nil, errors.New("supplier unavailable")
	})
	detail := object(t, request(h, "GET", "/api/admin/orders/"+id, nil, admin), 200)["order"].(map[string]any)
	if detail["platformStatus"].(map[string]any)["remoteId"] != "original" {
		t.Fatal("upstream ID missing from admin detail")
	}
	for _, q := range []string{"original", "ORIGINAL", "origin", id, "buyer@example.test", " original "} {
		result := object(t, request(h, "GET", "/api/admin/orders?q="+url.QueryEscape(q), nil, admin), 200)
		list := result["orders"].([]any)
		if result["total"] != float64(1) || len(list) != 1 || list[0].(map[string]any)["id"] != id || list[0].(map[string]any)["platformStatus"].(map[string]any)["remoteId"] != "original" {
			t.Fatalf("upstream search mismatch for %q: %v", q, result)
		}
	}
	for _, filter := range []string{"q=missing", "q=original&status=success", "q=original&plan=pro-5x"} {
		result := object(t, request(h, "GET", "/api/admin/orders?"+filter, nil, admin), 200)
		if result["total"] != float64(0) || len(result["orders"].([]any)) != 0 {
			t.Fatal("upstream search ignored filters", result)
		}
	}
	customer := object(t, request(h, "GET", "/api/orders/"+id, nil, buyer), 200)["order"].(map[string]any)
	if _, exists := customer["platformStatus"]; exists {
		t.Fatal("admin upstream snapshot exposed to customer")
	}
	object(t, request(h, "GET", "/api/admin/orders?q=original", nil, buyer), 401)
}

func (f rechargeTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type rechargeFake struct {
	order                    recharge.RemoteOrder
	redemption               recharge.Redemption
	createCalls, submitCalls int
	ambiguous                bool
	keys, bodies             []string
	refundStatus             string
	refundCalls              int
	refundKeys, refundBodies []string
	refundAuto               bool
	refund                   recharge.RemoteRefund
}

func (f *rechargeFake) round(r *http.Request) (*http.Response, error) {
	var data any
	switch r.URL.Path {
	case "/v1/products":
		data = []recharge.Product{{Code: "plus", Supply: "60.00", MaxSale: "200.00", Currency: "CNY", Quantity: 1, Available: true, Modes: []string{"auto_recharge"}}}
	case "/v1/payment-methods":
		data = []map[string]string{{"code": "alipay"}}
	case "/v1/orders":
		f.createCalls++
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.order.Merchant = body["merchant_order_no"].(string)
		data = f.order
	case "/v1/orders/original":
		data = f.order
	case "/v1/redemptions":
		f.submitCalls++
		b, _ := io.ReadAll(r.Body)
		f.bodies = append(f.bodies, string(b))
		f.keys = append(f.keys, r.Header.Get("Idempotency-Key"))
		if f.ambiguous {
			return nil, errors.New("connection lost")
		}
		data = f.redemption
	case "/v1/orders/original/fulfillments":
		data = []map[string]any{}
		if f.submitCalls > 0 && !f.ambiguous {
			number := int64(1)
			if f.redemption.Attempt != nil {
				number = *f.redemption.Attempt
			}
			data = []map[string]any{{"fulfillment_id": f.redemption.ID, "order_id": "original", "attempt_no": number}}
		}
	case "/v1/redemptions/attempt", "/v1/redemptions/attempt-2":
		data = f.redemption
	case "/v1/orders/original/refunds":
		f.refundCalls++
		raw, _ := io.ReadAll(r.Body)
		f.refundBodies = append(f.refundBodies, string(raw))
		f.refundKeys = append(f.refundKeys, r.Header.Get("Idempotency-Key"))
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		if f.refundAuto && f.refundStatus == "succeeded" {
			f.order.Payment, f.order.Refunded, f.order.Margin = "refunded", "100.00", "-60.00"
		}
		f.refund = recharge.RemoteRefund{ID: "refund", Order: "original", Merchant: body["merchant_refund_no"].(string), Amount: body["amount"].(string), Status: f.refundStatus}
		data = f.refund
	case "/v1/refunds/refund":
		f.refund.Status = f.refundStatus
		data = f.refund
	default:
		return &http.Response{StatusCode: 404, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"order_not_found"}}`))}, nil
	}
	status := 200
	if r.URL.Path == "/v1/orders/original/refunds" {
		status = 202
	}
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(recharge.Raw(map[string]any{"data": data})))}, nil
}

func TestRechargeRefundWorkerOnlyFollowsInitiatedRefund(t *testing.T) {
	owner, app, a, base, mail := identityFixture(t)
	admin := login(t, base, mail, "admin", "")
	client, fake := newRechargeFake(t)
	supplier := &recharge.Service{CredentialSecret: strings.Repeat("ab", 32), Store: app, Client: client, Checkout: true, Products: map[string]string{"plus": "plus"}, Notify: "https://dev.example.test/api/recharge/webhook"}
	h := httpapi.Router(app, "fixture", slog.New(slog.NewTextHandler(io.Discard, nil)), httpapi.Options{Origin: testOrigin, Auth: a, Recharge: supplier})
	execSQL(t, owner, `INSERT INTO catalog_plans(id,product_id,name,price_cents,enabled) VALUES('plus','chatgpt','Plus',10000,true)`)
	buyer := &http.Cookie{Name: "__Host-sf_buyer", Value: security.Token()}
	created := object(t, request(h, "POST", "/api/orders", map[string]any{"plan": "plus", "email": "buyer@example.test", "key": security.ID(), "expectedPrice": 10000}, buyer), 201)["order"].(map[string]any)
	id := created["id"].(string)
	fake.order.Payment = "paid"
	if e := supplier.Sync(context.Background(), id, true); e != nil {
		t.Fatal(e)
	}
	// A pre-upgrade request remains subject to the original administrator flow.
	execSQL(t, owner, `INSERT INTO refund_cases(id,order_id,request_key,reason,requested_cents,created_at,updated_at,last_event_id) VALUES($1,$2,$3,$4,10000,1,1,$1)`, security.ID(), id, security.ID(), "尚未交付全额退款")
	execSQL(t, owner, `UPDATE orders SET refund_hold=1,version=version+1 WHERE id=$1`, id)
	cases := object(t, request(h, "GET", "/api/orders/"+id+"/refunds", nil, buyer), 200)
	caseID := cases["cases"].([]any)[0].(map[string]any)["id"].(string)
	path := "/api/admin/refunds/" + caseID
	object(t, request(h, "POST", path, map[string]any{"action": "approve", "version": 0, "amountCents": 10000, "note": "批准全额退款"}, admin), 200)
	worker := refunds.Service{Store: app, Supplier: supplier}
	if e := worker.Reconcile(context.Background()); e != nil || fake.refundCalls != 0 {
		t.Fatal("worker initiated unrequested channel refund", e)
	}
	fake.refundStatus = "processing"
	object(t, request(h, "POST", path, map[string]any{"action": "execute-refund", "version": 1, "note": "原路退款"}, admin), 200)
	fake.refundStatus = "succeeded"
	fake.order.Payment = "refunded"
	fake.order.Refunded = "100.00"
	fake.order.Margin = "-60.00"
	execSQL(t, owner, `UPDATE recharge_refunds SET updated_at=0 WHERE case_id=$1`, caseID)
	if e := worker.Reconcile(context.Background()); e != nil {
		t.Fatal(e)
	}
	if fake.refundCalls != 1 {
		t.Fatal("worker created new refund")
	}
	var status string
	var amount int64
	var hold int
	if e := owner.DB.QueryRow(context.Background(), `SELECT r.status,o.refunded_cents,o.refund_hold FROM refund_cases r JOIN orders o ON o.id=r.order_id WHERE r.id=$1`, caseID).Scan(&status, &amount, &hold); e != nil || status != "refunded" || amount != 10000 || hold != 0 {
		t.Fatal("refund ledger mismatch", e)
	}
}
func newRechargeFake(t *testing.T) (*recharge.Client, *rechargeFake) {
	t.Helper()
	client, e := recharge.NewClient("https://supplier.example.test/v1", "tenant", "key", "fixture-secret")
	if e != nil {
		t.Fatal(e)
	}
	yes := true
	version := int64(1)
	fake := &rechargeFake{order: recharge.RemoteOrder{ID: "original", Product: "plus", Quantity: 1, Currency: "CNY", Collection: "platform_collect", Scope: "retail", Delivery: "auto_recharge", Channel: "alipay", Sale: "100.00", Supply: "60.00", Margin: "40.00", Payment: "pending", Expires: time.Now().Add(15 * time.Minute).UTC().Format(time.RFC3339Nano)}, redemption: recharge.Redemption{ID: "attempt", Order: "original", Status: "running", Fallback: &yes, Version: &version, Attempt: &version}}
	client.HTTP.Transport = rechargeTransport(fake.round)
	return client, fake
}

func TestRechargeApproveAndSubmitShowsOrderAndPendingRefund(t *testing.T) {
	owner, app, a, base, mail := identityFixture(t)
	admin := login(t, base, mail, "admin", "")
	client, fake := newRechargeFake(t)
	supplier := &recharge.Service{CredentialSecret: strings.Repeat("ab", 32), Store: app, Client: client, Checkout: true, Products: map[string]string{"plus": "plus"}, Notify: "https://dev.example.test/api/recharge/webhook"}
	h := httpapi.Router(app, "fixture", slog.New(slog.NewTextHandler(io.Discard, nil)), httpapi.Options{Origin: testOrigin, Auth: a, Recharge: supplier})
	execSQL(t, owner, `INSERT INTO catalog_plans(id,product_id,name,price_cents,enabled) VALUES('plus','chatgpt','Plus',10000,true)`)
	buyer := &http.Cookie{Name: "__Host-sf_buyer", Value: security.Token()}
	created := object(t, request(h, "POST", "/api/orders", map[string]any{"plan": "plus", "email": "buyer@example.test", "key": security.ID(), "expectedPrice": 10000}, buyer), 201)["order"].(map[string]any)
	id := created["id"].(string)
	fake.order.Payment = "paid"
	if e := supplier.Sync(context.Background(), id, true); e != nil {
		t.Fatal(e)
	}
	// A pre-upgrade request remains subject to the original administrator flow.
	execSQL(t, owner, `INSERT INTO refund_cases(id,order_id,request_key,reason,requested_cents,created_at,updated_at,last_event_id) VALUES($1,$2,$3,$4,10000,1,1,$1)`, security.ID(), id, security.ID(), "充值失败申请退款")
	execSQL(t, owner, `UPDATE orders SET refund_hold=1,version=version+1 WHERE id=$1`, id)
	cases := object(t, request(h, "GET", "/api/orders/"+id+"/refunds", nil, buyer), 200)
	caseID := cases["cases"].([]any)[0].(map[string]any)["id"].(string)
	path := "/api/admin/refunds/" + caseID
	in := map[string]any{"action": "approve-submit-platform", "version": 0, "amountCents": 10000, "note": "同意并提交"}
	object(t, request(h, "POST", path, in, buyer), 401)
	fake.refundStatus = "requested"
	object(t, request(h, "POST", path, in, admin), 200)
	object(t, request(h, "POST", path, in, admin), 409)
	detail := object(t, request(h, "GET", path, nil, admin), 200)
	o := detail["order"].(map[string]any)
	if o["payment_status"] != "paid" || o["status"] != "session" || o["created_at"] == nil || o["refunded_cents"] != float64(0) {
		t.Fatal("missing order context or prematurely refunded", o)
	}
	if detail["upstreamRefund"].(map[string]any)["status"] != "requested" || detail["platformStatus"].(map[string]any)["remote_id"] != "original" {
		t.Fatal("upstream progress missing")
	}
	var raw string
	if e := owner.DB.QueryRow(context.Background(), `SELECT create_body FROM recharge_refunds WHERE case_id=$1`, caseID).Scan(&raw); e != nil || !strings.Contains(raw, "充值失败申请退款") {
		t.Fatal("customer reason not submitted", e)
	}
	object(t, request(h, "POST", path, map[string]any{"action": "execute-refund", "version": 1, "note": "核对原申请"}, admin), 200)
	if fake.refundCalls != 1 {
		t.Fatal("duplicate upstream refund")
	}
}
func TestRechargeUnknownRetryIdentityAndMonotonicProgress(t *testing.T) {
	owner, app, a, base, mail := identityFixture(t)
	admin := login(t, base, mail, "admin", "")
	client, fake := newRechargeFake(t)
	supplier := &recharge.Service{CredentialSecret: strings.Repeat("ab", 32), Store: app, Client: client, Checkout: true, Products: map[string]string{"plus": "plus"}, Notify: "https://dev.example.test/api/recharge/webhook", WebhookSecret: "fixture-webhook"}
	h := httpapi.Router(app, "fixture", slog.New(slog.NewTextHandler(io.Discard, nil)), httpapi.Options{Origin: testOrigin, Auth: a, Recharge: supplier})
	execSQL(t, owner, `INSERT INTO catalog_plans(id,product_id,name,price_cents,enabled,partner_enabled) VALUES('plus','chatgpt','Plus',10000,true,true)`)
	buyer := &http.Cookie{Name: "__Host-sf_buyer", Value: security.Token()}
	in := map[string]any{"plan": "plus", "email": "buyer@example.test", "key": security.ID(), "expectedPrice": 10000}
	created := object(t, request(h, "POST", "/api/orders", in, buyer), 201)["order"].(map[string]any)
	id := created["id"].(string)
	if created["mode"] != "live" {
		t.Fatal("not live")
	}
	ctx := context.Background()
	if e := supplier.Sync(ctx, id, true); e != nil {
		t.Fatal(e)
	}
	fake.order.Payment = "paid"
	if e := supplier.Sync(ctx, id, true); e != nil {
		t.Fatal(e)
	}
	if fake.createCalls != 1 {
		t.Fatal("supplier duplicate order")
	}
	path := "/api/orders/" + id + "/fulfillment"
	key := security.ID()
	submit := map[string]any{"action": "submit", "key": key, "customerConfirmedEmail": true, "credential": map[string]any{"mode": "access_token", "access_token": "sensitive-fixture-token"}}
	client.HTTP.Transport = rechargeTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/v1/redemptions" {
			stored, err := orders.Get(ctx, owner.DB, id, nil, false)
			if err != nil || stored.Status != "session" || stored.Started != nil {
				t.Fatal("order claimed recharge before dispatch confirmation", err)
			}
		}
		return fake.round(r)
	})
	fake.ambiguous = true
	response := object(t, request(h, "POST", path, submit, buyer), 503)
	message, _ := response["error"].(string)
	if !strings.Contains(message, "结果尚未确认") || strings.Contains(message, "sensitive-fixture-token") {
		t.Fatal("submission failure missing safe recovery guidance", message)
	}
	unconfirmed := object(t, request(h, "POST", path, map[string]any{"action": "query"}, buyer), 200)["order"].(map[string]any)
	unknown := unconfirmed["fulfillment"].(map[string]any)
	if unconfirmed["status"] != "session" || unconfirmed["startedAt"] != nil {
		t.Fatal("unaccepted submission shown as recharging", unconfirmed)
	}
	if unknown["rechargeStatus"] != "unknown" || unknown["retryKey"] != key || fake.submitCalls != 2 {
		t.Fatal("read-only query lost the original submission or replayed it", unknown)
	}
	execSQL(t, owner, `UPDATE recharge_orders SET updated_at=0,verified_at=0 WHERE order_id=$1`, id)
	if err := supplier.ReconcileActive(ctx); err != nil {
		t.Fatal(err)
	}
	var verified int64
	if err := owner.DB.QueryRow(ctx, `SELECT verified_at FROM recharge_orders WHERE order_id=$1`, id).Scan(&verified); err != nil || verified == 0 || fake.submitCalls != 2 {
		t.Fatal("unconfirmed submission lost read-only reconciliation", err)
	}
	execSQL(t, owner, `INSERT INTO partners(id,slug,name,active) VALUES('submission-partner','SUBMIT','submission fixture',true)`)
	execSQL(t, owner, `UPDATE orders SET partner_id='submission-partner' WHERE id=$1`, id)
	partnerView, err := (management.Service{Store: app}).PartnerOrders(ctx, "submission-partner", 1)
	if err != nil {
		t.Fatal(err)
	}
	partnerRows := partnerView["rows"].([]map[string]any)
	if len(partnerRows) != 1 || partnerRows[0]["recharge_status"] != "unknown" {
		t.Fatal("partner lost submission state", partnerView)
	}
	submit["key"] = security.ID()
	object(t, request(h, "POST", path, submit, buyer), 409)
	submit["key"] = key
	submit["credential"] = map[string]any{"mode": "access_token", "access_token": "different-token"}
	object(t, request(h, "POST", path, submit, buyer), 409)
	submit["credential"] = map[string]any{"mode": "access_token", "access_token": "sensitive-fixture-token"}
	fake.ambiguous = false
	object(t, request(h, "POST", path, submit, buyer), 200)
	if fake.submitCalls != 3 || fake.keys[0] != fake.keys[1] || fake.keys[0] != fake.keys[2] || fake.bodies[0] != fake.bodies[1] || fake.bodies[0] != fake.bodies[2] {
		t.Fatal("unknown retry not bound")
	}
	accepted, err := orders.Get(ctx, app.DB, id, nil, false)
	if err != nil || accepted.Status != "processing" || accepted.Started == nil {
		t.Fatal("accepted task did not start recharge", err)
	}
	var raw string
	if e := owner.DB.QueryRow(ctx, `SELECT to_jsonb(t)::text FROM recharge_redemptions t WHERE id=$1`, key).Scan(&raw); e != nil || strings.Contains(raw, "sensitive-fixture-token") {
		t.Fatal("raw credential persisted", e)
	}
	v2 := int64(2)
	fake.redemption.Version = &v2
	fake.redemption.Status = "succeeded"
	if e := supplier.Verify(ctx, id); e != nil {
		t.Fatal(e)
	}
	v1 := int64(1)
	fake.redemption.Version = &v1
	fake.redemption.Status = "failed"
	fake.redemption.Retry = true
	fake.redemption.Next = "resubmit"
	if e := supplier.Verify(ctx, id); e != nil {
		t.Fatal(e)
	}
	o, e := orders.Get(ctx, app.DB, id, nil, false)
	if e != nil || o.Status != "success" {
		t.Fatal("old snapshot downgraded success", e)
	}
	object(t, request(h, "POST", "/api/admin/orders/"+id+"/platform-status", map[string]any{}, admin), 200)
	object(t, request(h, "POST", path, submit, &http.Cookie{Name: buyer.Name, Value: security.Token()}), 404)
	fake.order.Refunded = "100.00"
	fake.order.Payment = "refunded"
	fake.order.Margin = "-60.00"
	if e = supplier.Verify(ctx, id); e != nil {
		t.Fatal(e)
	}
	var held int
	if e = owner.DB.QueryRow(ctx, `SELECT financial_hold FROM recharge_orders WHERE order_id=$1`, id).Scan(&held); e != nil || held != 1 {
		t.Fatal("external refund did not freeze finance", e)
	}
	fake.order.Payment = "paid"
	fake.order.Refunded = "0.00"
	fake.order.Margin = "40.00"
	if e = supplier.Verify(ctx, id); e != nil {
		t.Fatal(e)
	}
	if e = owner.DB.QueryRow(ctx, `SELECT financial_hold FROM recharge_orders WHERE order_id=$1`, id).Scan(&held); e != nil || held != 1 {
		t.Fatal("old payment released hold", e)
	}
}
func TestRechargeWebhookSchedulesOnlyAndBindsReplay(t *testing.T) {
	owner, app := fixture(t, true)
	client, fake := newRechargeFake(t)
	s := &recharge.Service{CredentialSecret: strings.Repeat("ab", 32), Store: app, Client: client, WebhookSecret: "fixture-secret"}
	now := time.Now().UnixMilli()
	execSQL(t, owner, `INSERT INTO catalog_plans(id,product_id,name,price_cents) VALUES('plus','chatgpt','Plus',10000)`)
	execSQL(t, owner, `INSERT INTO orders(id,owner_hash,idempotency_key,plan,plan_name,price_cents,mode,payment_status,status,created_at,updated_at,last_event_id) VALUES('YMX-WEBHOOK','owner','key','plus','Plus',10000,'live','unpaid','pending',$1,$1,'fixture')`, now)
	execSQL(t, owner, `INSERT INTO recharge_orders(order_id,account,product_code,collection_mode,create_body,updated_at) VALUES('YMX-WEBHOOK',$1,'plus','platform_collect','{}',$2)`, client.Account(), now)
	execSQL(t, owner, `INSERT INTO orders(id,owner_hash,idempotency_key,plan,plan_name,price_cents,mode,payment_status,status,created_at,updated_at,last_event_id) VALUES('YMX-UNRELATED','owner','other','plus','Plus',10000,'live','unpaid','pending',$1,$1,'fixture')`, now)
	execSQL(t, owner, `INSERT INTO recharge_orders(order_id,account,product_code,collection_mode,create_body,updated_at) VALUES('YMX-UNRELATED',$1,'plus','platform_collect','{}',$2)`, client.Account(), now)
	send := func(raw, stamp string) error {
		req := httptest.NewRequest("POST", "/api/recharge/webhook", strings.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Quefa-Timestamp", stamp)
		req.Header.Set("X-Quefa-Signature", "t="+stamp+",v1="+recharge.HMAC("fixture-secret", stamp+"."+raw))
		req.Header.Set("X-Quefa-Event-Id", "event-one")
		req.Header.Set("X-Quefa-Event", "order.paid")
		req.Header.Set("X-Quefa-Delivery", "delivery-one")
		return s.Webhook(context.Background(), req)
	}
	stamp := strconv.FormatInt(time.Now().Unix(), 10)
	raw := `{"event_id":"event-one","type":"order.paid","data":{"payment_status":"paid","merchant_order_no":"YMX-WEBHOOK"}}`
	if e := send(raw, stamp); e != nil {
		t.Fatal(e)
	}
	if e := send(raw, stamp); e != nil {
		t.Fatal(e)
	}
	if e := send(`{"event_id":"event-one","type":"order.paid","data":{}}`, stamp); e == nil {
		t.Fatal("conflicting replay accepted")
	}
	if e := send(raw, strconv.FormatInt(time.Now().Unix()-600, 10)); e == nil {
		t.Fatal("expired callback accepted")
	}
	var untouched bool
	if err := owner.DB.QueryRow(context.Background(), `SELECT updated_at=$1 AND notify_version=0 FROM recharge_orders WHERE order_id='YMX-UNRELATED'`, now).Scan(&untouched); err != nil || !untouched {
		t.Fatal("callback reset unrelated order queue", err)
	}
	var version int64
	var payment string
	if e := owner.DB.QueryRow(context.Background(), `SELECT t.notify_version,o.payment_status FROM recharge_orders t JOIN orders o ON o.id=t.order_id WHERE o.id='YMX-WEBHOOK'`).Scan(&version, &payment); e != nil || version != 1 || payment != "unpaid" || fake.createCalls != 0 {
		t.Fatal("callback changed money or duplicated schedule", e)
	}
}

func TestRechargePublicReasonReachesCustomerAndPartner(t *testing.T) {
	owner, app, a, base, mail := identityFixture(t)
	execSQL(t, owner, `INSERT INTO partners(id,slug,name,email,active) VALUES('reason-shop','REASON','A','reason@example.test',true)`)
	partner := login(t, base, mail, "partner", "reason@example.test")
	client, fake := newRechargeFake(t)
	supplier := &recharge.Service{CredentialSecret: strings.Repeat("ab", 32), Store: app, Client: client, Checkout: true, Products: map[string]string{"plus": "plus"}, Notify: "https://dev.example.test/api/recharge/webhook"}
	h := httpapi.Router(app, "fixture", slog.New(slog.NewTextHandler(io.Discard, nil)), httpapi.Options{Origin: testOrigin, Auth: a, Recharge: supplier})
	execSQL(t, owner, `INSERT INTO catalog_plans(id,product_id,name,price_cents,enabled) VALUES('plus','chatgpt','Plus',10000,true)`)
	buyer := &http.Cookie{Name: "__Host-sf_buyer", Value: security.Token()}
	created := object(t, request(h, "POST", "/api/orders", map[string]any{"plan": "plus", "email": "buyer@example.test", "key": security.ID(), "expectedPrice": 10000}, buyer), 201)["order"].(map[string]any)
	id := created["id"].(string)
	ctx := context.Background()
	fake.order.Payment = "paid"
	if e := supplier.Sync(ctx, id, true); e != nil {
		t.Fatal(e)
	}
	execSQL(t, owner, `UPDATE orders SET partner_id='reason-shop' WHERE id=$1`, id)
	object(t, request(h, "POST", "/api/orders/"+id+"/fulfillment", map[string]any{"action": "submit", "key": security.ID(), "customerConfirmedEmail": true, "credential": map[string]any{"mode": "access_token", "access_token": "never-expose-this-token"}}, buyer), 200)
	version := int64(2)
	failure, category, reason := "precheck_rejected", "account", "账号尚未完成验证，请完成账号验证后再提交。"
	fake.redemption.Version = &version
	fake.redemption.Status = "failed"
	fake.redemption.Failure = &failure
	fake.redemption.Category = &category
	fake.redemption.Message = &reason
	fake.redemption.Retry = true
	fake.redemption.Next = "resubmit"
	if e := supplier.Verify(ctx, id); e != nil {
		t.Fatal(e)
	}
	check := func(want string, retry bool) {
		t.Helper()
		for _, endpoint := range []string{"/api/orders/" + id, "/api/partner/orders/" + id} {
			cookie := buyer
			if strings.Contains(endpoint, "/partner/") {
				cookie = partner
			}
			v := object(t, request(h, "GET", endpoint, nil, cookie), 200)["order"].(map[string]any)
			f := v["fulfillment"].(map[string]any)
			if f["message"] != want || f["canSubmit"] != retry || f["failureReason"] == nil {
				t.Fatal("reason/action mismatch", endpoint, f)
			}
			if strings.Contains(endpoint, "/partner/") {
				raw, _ := json.Marshal(v)
				for _, secret := range []string{"retryKey", "retryMode", "never-expose-this-token", "request_hash"} {
					if strings.Contains(string(raw), secret) {
						t.Fatal("private attempt data leaked")
					}
				}
			}
		}
	}
	check(reason, true)
	version = 1
	stale := "旧原因"
	fake.redemption.Message = &stale
	if e := supplier.Verify(ctx, id); e != nil {
		t.Fatal(e)
	}
	check(reason, true)
	version = 2
	fake.redemption.Recovery = "refund"
	if e := supplier.Verify(ctx, id); e != nil {
		t.Fatal(e)
	}
	check("平台正在处理退款，请在原订单查看售后进度。", false)
	version = 3
	fake.redemption.Recovery = ""
	unsafe := "https://private.example?access_token=secret"
	fake.redemption.Message = &unsafe
	if e := supplier.Verify(ctx, id); e != nil {
		t.Fatal(e)
	}
	check("账号未通过预检，请完成账号验证或联系客服。", true)
	for _, unsafe := range []string{"请前往 recharge.example.invalid 处理。", "请前往 //RECHARGE.INK/pay 处理。", "请联系 checkout.recharge.example.invalid 客服。"} {
		version++
		fake.redemption.Message = &unsafe
		if e := supplier.Verify(ctx, id); e != nil {
			t.Fatal(e)
		}
		var stored string
		if e := owner.DB.QueryRow(ctx, `SELECT public_message FROM recharge_redemptions WHERE order_id=$1 ORDER BY attempt_no DESC LIMIT 1`, id).Scan(&stored); e != nil || stored != "" {
			t.Fatal("supplier domain stored as public reason", e)
		}
		check("账号未通过预检，请完成账号验证或联系客服。", true)
		// Historical messages must be sanitized on reads without a provider sync.
		execSQL(t, owner, `UPDATE recharge_redemptions SET public_message=$1 WHERE order_id=$2`, unsafe, id)
		check("账号未通过预检，请完成账号验证或联系客服。", true)
		for _, endpoint := range []string{"/api/orders", "/api/orders/" + id, "/api/partner/orders", "/api/partner/orders/" + id} {
			cookie := buyer
			if strings.Contains(endpoint, "/partner/") {
				cookie = partner
			}
			response := request(h, "GET", endpoint, nil, cookie)
			if response.Code != 200 || strings.Contains(strings.ToLower(response.Body.String()), "recharge.example.invalid") {
				t.Fatal("historical supplier domain leaked", endpoint, response.Code)
			}
		}
	}
}
