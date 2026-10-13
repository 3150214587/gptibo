package integration_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"gptibo/storefront/internal/httpapi"
	"gptibo/storefront/internal/refunds"
	"gptibo/storefront/internal/security"
)

func TestWarrantyCurrentProductPolicyAppliesToAllOrders(t *testing.T) {
	owner, app, auth, base, mail := identityFixture(t)
	admin := login(t, base, mail, "admin", "")
	h := httpapi.Router(app, "fixture", slog.New(slog.NewTextHandler(io.Discard, nil)), httpapi.Options{Origin: testOrigin, Auth: auth, OrderTesting: true})
	execSQL(t, owner, `INSERT INTO catalog_plans(id,product_id,name,price_cents,enabled,partner_enabled) VALUES('plus','chatgpt','Plus',1000,true,true);
INSERT INTO partners(id,slug,name,active) VALUES('warranty-partner','ABC123','Partner',true);
INSERT INTO partner_prices(partner_id,plan,price_cents,cost_cents) VALUES('warranty-partner','plus',1200,500)`)
	buyer := &http.Cookie{Name: "__Host-sf_buyer", Value: security.Token()}
	create := func(partner string, policy any, status int) map[string]any {
		t.Helper()
		price := 1000
		if partner != "" {
			price = 1200
		}
		in := map[string]any{"plan": "plus", "key": security.ID(), "email": "buyer@example.test", "expectedPrice": price, "partner": partner}
		if policy != nil {
			in["expectedSupportsWarranty"] = policy
		}
		result := object(t, request(h, "POST", "/api/orders", in, buyer), status)
		if status != 201 {
			return result
		}
		order := result["order"].(map[string]any)
		id := order["id"].(string)
		object(t, request(h, "POST", "/api/orders/"+id+"/demo-actions", map[string]any{"action": "pay", "version": 0}, buyer), 200)
		return order
	}
	oldOrder := create("", nil, 201)
	oldPartnerOrder := create("ABC123", true, 201)
	if oldOrder["supportsWarranty"] != true {
		t.Fatal("existing policy must default to support")
	}
	input := map[string]any{"priceCents": 1000, "enabled": true, "supportsWarranty": false, "note": "关闭商品全部订单售后质保", "version": 0}
	object(t, request(h, "POST", "/api/admin/plans/plus", input, buyer), 401)
	object(t, request(h, "POST", "/api/admin/plans/plus", input, admin), 200)
	object(t, request(h, "POST", "/api/admin/plans/plus", input, admin), 409)
	plans := object(t, request(h, "GET", "/api/admin/plans", nil, admin), 200)["plans"].([]any)
	if plans[0].(map[string]any)["supports_warranty"] != false {
		t.Fatal("admin policy was not saved")
	}
	for _, suffix := range []string{"", "?partner=ABC123"} {
		plans := object(t, request(h, "GET", "/api/catalog"+suffix, nil, nil), 200)["plans"].([]any)
		if plans[0].(map[string]any)["supports_warranty"] != false {
			t.Fatal("store policy not disclosed", suffix)
		}
	}
	create("", nil, 409)
	create("", true, 409)
	newOrder := create("", false, 201)
	partnerOrder := create("ABC123", false, 201)
	all := []map[string]any{oldOrder, oldPartnerOrder, newOrder, partnerOrder}
	// Deliberately conflicting obsolete order flags must never decide eligibility.
	execSQL(t, owner, `UPDATE orders SET supports_warranty=false WHERE id=$1 OR id=$2`, newOrder["id"], partnerOrder["id"])
	for _, o := range all {
		id := o["id"].(string)
		view := object(t, request(h, "GET", "/api/orders/"+id+"/refunds", nil, buyer), 200)
		if view["supportsWarranty"] != false || view["warrantyMessage"] != refunds.WarrantyUnavailableMessage {
			t.Fatal("refund view must use current product policy for old and new orders")
		}
		for _, path := range []string{"/api/orders/" + id, "/api/admin/orders/" + id} {
			cookie := buyer
			if strings.HasPrefix(path, "/api/admin/") {
				cookie = admin
			}
			detail := object(t, request(h, "GET", path, nil, cookie), 200)["order"].(map[string]any)
			if detail["supportsWarranty"] != false {
				t.Fatal("order detail must reflect current product policy", path)
			}
		}
		result := request(h, "POST", "/api/orders/"+id+"/refunds", map[string]any{"key": security.ID(), "reason": "申请质保", "version": 1}, buyer)
		object(t, result, 409)
		if !strings.Contains(result.Body.String(), refunds.WarrantyUnavailableMessage) {
			t.Fatal("missing customer explanation")
		}
		var hold, version, cases int
		if err := owner.DB.QueryRow(context.Background(), `SELECT refund_hold,version,(SELECT count(*) FROM refund_cases WHERE order_id=$1) FROM orders WHERE id=$1`, id).Scan(&hold, &version, &cases); err != nil || hold != 0 || version != 1 || cases != 0 {
			t.Fatal("rejected request changed financial state", err)
		}
		object(t, request(h, "POST", "/api/orders/"+id+"/refunds", map[string]any{"key": security.ID(), "reason": "申请质保", "version": 1}, &http.Cookie{Name: buyer.Name, Value: security.Token()}), 404)
	}
	for _, o := range object(t, request(h, "GET", "/api/orders", nil, buyer), 200)["orders"].([]any) {
		if o.(map[string]any)["supportsWarranty"] != false {
			t.Fatal("order list must reflect current product policy")
		}
	}
	// Re-enabling the product restores eligibility even for orders created while excluded.
	input["supportsWarranty"], input["version"] = true, 1
	object(t, request(h, "POST", "/api/admin/plans/plus", input, admin), 200)
	for _, o := range all {
		id := o["id"].(string)
		view := object(t, request(h, "GET", "/api/orders/"+id+"/refunds", nil, buyer), 200)
		if view["supportsWarranty"] != true || view["warrantyMessage"] != "" {
			t.Fatal("re-enabled product must support all orders regardless of obsolete flags")
		}
		detail := object(t, request(h, "GET", "/api/orders/"+id, nil, buyer), 200)["order"].(map[string]any)
		if detail["supportsWarranty"] != true {
			t.Fatal("order detail did not refresh policy")
		}
	}
	id := newOrder["id"].(string)
	requestInput := map[string]any{"key": security.ID(), "reason": "商品重新支持售后", "version": 1}
	object(t, request(h, "POST", "/api/orders/"+id+"/refunds", requestInput, buyer), 201)
	object(t, request(h, "POST", "/api/orders/"+id+"/refunds", requestInput, buyer), 200)
	// Disabling again blocks new cases, while existing pending cases remain viewable/cancellable.
	input["supportsWarranty"], input["version"] = false, 2
	object(t, request(h, "POST", "/api/admin/plans/plus", input, admin), 200)
	view := object(t, request(h, "GET", "/api/orders/"+id+"/refunds", nil, buyer), 200)
	if view["supportsWarranty"] != false {
		t.Fatal("policy did not change for an existing case")
	}
	caseID := view["cases"].([]any)[0].(map[string]any)["id"]
	object(t, request(h, "POST", "/api/orders/"+id+"/refunds", requestInput, buyer), 200)
	object(t, request(h, "POST", "/api/orders/"+id+"/refunds", map[string]any{"action": "cancel", "id": caseID, "version": 0}, buyer), 200)
	object(t, request(h, "POST", "/api/orders/"+id+"/refunds", map[string]any{"key": security.ID(), "reason": "关闭后再次申请", "version": 3}, buyer), 409)
}

func TestWarrantyMigrationPreservesExistingOrders(t *testing.T) {
	owner, app := fixture(t, true)
	ctx := context.Background()
	// Reconstruct the previous schema with already purchased orders.
	execSQL(t, owner, `ALTER TABLE catalog_plans DROP COLUMN supports_warranty;
ALTER TABLE orders DROP COLUMN supports_warranty;
DELETE FROM storefront_migrations WHERE name='023_after_sales_warranty.sql';
INSERT INTO catalog_plans(id,product_id,name,price_cents) VALUES('plus','chatgpt','Plus',1000);
INSERT INTO orders(id,owner_hash,idempotency_key,plan,plan_name,price_cents,mode,status,payment_status,created_at,updated_at,last_event_id)
VALUES('YMX-OLD','fixture','old','plus','Plus',1000,'live','success','paid',1,1,'fixture')`)
	if err := owner.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := owner.Migrate(ctx); err != nil {
		t.Fatal("migration replay", err)
	}
	if err := app.VerifyRuntime(ctx); err != nil {
		t.Fatal(err)
	}
	var preserved bool
	if err := app.DB.QueryRow(ctx, `SELECT (SELECT supports_warranty FROM catalog_plans WHERE id='plus') AND
(SELECT supports_warranty AND price_cents=1000 AND status='success' AND version=0 FROM orders WHERE id='YMX-OLD')`).Scan(&preserved); err != nil || !preserved {
		t.Fatal("migration changed historical agreement or order state", err)
	}
}
