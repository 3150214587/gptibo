package integration_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"gptibo/storefront/internal/httpapi"
	"gptibo/storefront/internal/orders"
	"gptibo/storefront/internal/security"
)

func TestOrderLifecycleIdempotencyAndBuyerIsolation(t *testing.T) {
	owner, app, a, closed, mail := identityFixture(t)
	admin := login(t, closed, mail, "admin", "")
	execSQL(t, owner, `INSERT INTO catalog_plans(id,product_id,name,price_cents,enabled,partner_enabled) VALUES('plus','chatgpt','Plus',16000,true,true)`)
	h := httpapi.Router(app, "fixture", slog.New(slog.NewTextHandler(io.Discard, nil)), httpapi.Options{Origin: testOrigin, Auth: a, OrderTesting: true})
	buyer := &http.Cookie{Name: "__Host-sf_buyer", Value: security.Token()}
	in := map[string]any{"plan": "plus", "email": "buyer@example.test", "key": security.ID(), "expectedPrice": 16000}
	object(t, request(closed, "POST", "/api/orders", in, buyer), 409)
	object(t, request(h, "POST", "/api/orders", in, nil), 401)
	created := object(t, request(h, "POST", "/api/orders", in, buyer), 201)["order"].(map[string]any)
	id := created["id"].(string)
	replay := object(t, request(h, "POST", "/api/orders", in, buyer), 200)["order"].(map[string]any)
	if replay["id"] != id || created["mode"] != "demo" {
		t.Fatal("duplicate order or wrong mode")
	}
	in["email"] = "different@example.test"
	object(t, request(h, "POST", "/api/orders", in, buyer), 409)
	other := &http.Cookie{Name: buyer.Name, Value: security.Token()}
	object(t, request(h, "GET", "/api/orders/"+id, nil, other), 404)
	got := object(t, request(h, "GET", "/api/orders", nil, other), 200)
	if len(got["orders"].([]any)) != 0 {
		t.Fatal("buyer isolation failed")
	}
	object(t, request(h, "POST", "/api/orders/"+id+"/demo-actions", map[string]any{"action": "pay", "version": 0}, buyer), 200)
	object(t, request(h, "POST", "/api/orders/"+id+"/demo-actions", map[string]any{"action": "pay", "version": 0}, buyer), 409)
	object(t, request(h, "POST", "/api/orders/"+id+"/demo-actions", map[string]any{"action": "submit-success", "version": 1}, buyer), 200)
	execSQL(t, owner, `UPDATE orders SET started_at=$1 WHERE id=$2`, time.Now().UnixMilli()-5000, id)
	detail := object(t, request(h, "GET", "/api/orders/"+id, nil, buyer), 200)["order"].(map[string]any)
	if detail["status"] != "success" || detail["version"] != float64(3) {
		t.Fatal("demo state did not settle")
	}
	for _, key := range []string{"owner_hash", "idempotency_key", "cost_cents"} {
		if _, ok := detail[key]; ok {
			t.Fatal("internal data exposed")
		}
	}
	managed := object(t, request(h, "GET", "/api/admin/orders/"+id, nil, admin), 200)
	if len(managed["events"].([]any)) != 4 {
		t.Fatal("state transition event missing")
	}
	list := object(t, request(h, "GET", "/api/admin/orders?status=success&q="+id, nil, admin), 200)
	if list["total"] != float64(1) {
		t.Fatal("admin filter incorrect")
	}
	overview := object(t, request(h, "GET", "/api/admin/overview", nil, admin), 200)
	if overview["stats"].(map[string]any)["paid_amount"] != float64(0) {
		t.Fatal("demo counted as real revenue")
	}
}

func TestConcurrentOrderCreationAndFinancialGuards(t *testing.T) {
	owner, app, a, h, mail := identityFixture(t)
	admin := login(t, h, mail, "admin", "")
	execSQL(t, owner, `INSERT INTO catalog_plans(id,product_id,name,price_cents,enabled) VALUES('pro-25x','chatgpt','Pro',99000,true)`)
	service := orders.Service{Store: app, Auth: a, Testing: true}
	access := orders.Access{OwnerHash: security.Hash(security.Token())}
	in := orders.CreateInput{Plan: "pro-25x", Email: "race@example.test", Key: security.ID(), ExpectedPrice: 99000}
	type result struct {
		id      string
		created bool
		err     error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			o, new, e := service.Create(context.Background(), access, in)
			results <- result{o.ID, new, e}
		}()
	}
	wg.Wait()
	close(results)
	var id string
	created := 0
	for r := range results {
		if r.err != nil {
			t.Fatal(r.err)
		}
		if id != "" && id != r.id {
			t.Fatal("duplicate idempotent order")
		}
		id = r.id
		if r.created {
			created++
		}
	}
	if created != 1 {
		t.Fatal("creation not serialized")
	}
	execSQL(t, owner, `UPDATE orders SET mode='live',payment_status='paid',status='session' WHERE id=$1`, id)
	var reminders int
	e := owner.DB.QueryRow(context.Background(), `SELECT count(*) FROM recharge_reminders WHERE order_id=$1`, id).Scan(&reminders)
	if e != nil || reminders != 1 {
		t.Fatal("paid reminder not queued", e)
	}
	object(t, request(h, "POST", "/api/admin/orders/"+id+"/actions", map[string]any{"action": "manual-start", "version": 0, "note": "start"}, admin), 200)
	object(t, request(h, "POST", "/api/admin/orders/"+id+"/actions", map[string]any{"action": "redemption-control", "blocked": true, "version": 1, "note": "hold"}, admin), 200)
	object(t, request(h, "POST", "/api/admin/orders/"+id+"/actions", map[string]any{"action": "manual-complete", "version": 2, "note": "complete"}, admin), 409)
	object(t, request(h, "POST", "/api/admin/orders/"+id+"/actions", map[string]any{"action": "note", "version": 2, "note": "support"}, admin), 200)
	execSQL(t, owner, `INSERT INTO xgift_purchases(order_id,username,recipient_id,product_code,points,credential_hash,state,lease_until,updated_at) VALUES($1,'fixture','recipient','x-premium-3m',100,'fixture','unknown',0,$2)`, id, time.Now().UnixMilli())
	object(t, request(h, "POST", "/api/admin/orders/"+id+"/actions", map[string]any{"action": "redemption-control", "blocked": false, "version": 3, "note": "resume"}, admin), 409)
	_, e = owner.DB.Exec(context.Background(), `UPDATE xgift_purchases SET credential_hash='changed' WHERE order_id=$1`, id)
	if e == nil || !strings.Contains(e.Error(), "immutable_business_record") {
		t.Fatal("supplier binding mutable", e)
	}
}
