package integration_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"gptibo/storefront/internal/catalog"
	"gptibo/storefront/internal/httpapi"
	"gptibo/storefront/internal/security"
)

func TestPartnerProductVisibilityIsolationAndOrderLifecycle(t *testing.T) {
	owner, app, auth, base, mail := identityFixture(t)
	admin := login(t, base, mail, "admin", "")
	h := httpapi.Router(app, "fixture", slog.New(slog.NewTextHandler(io.Discard, nil)), httpapi.Options{Origin: testOrigin, Auth: auth, OrderTesting: true})
	execSQL(t, owner, `INSERT INTO catalog_plans(id,product_id,name,price_cents,default_cost_cents,enabled,partner_enabled) VALUES('plus','chatgpt','Plus',1000,500,true,true),('pro-5x','chatgpt','Pro',2000,1000,true,true),('codex-sms-once','codex-sms','SMS',1000,500,true,true)`)
	id := object(t, request(h, "POST", "/api/admin/partners", map[string]any{"email": "visibility@example.test"}, admin), 200)["id"].(string)
	otherID := object(t, request(h, "POST", "/api/admin/partners", map[string]any{"email": "other-visibility@example.test"}, admin), 200)["id"].(string)
	partner := login(t, h, mail, "partner", "visibility@example.test")
	other := login(t, h, mail, "partner", "other-visibility@example.test")
	var slug, otherSlug string
	if e := owner.DB.QueryRow(context.Background(), `SELECT slug FROM partners WHERE id=$1`, id).Scan(&slug); e != nil {
		t.Fatal(e)
	}
	if e := owner.DB.QueryRow(context.Background(), `SELECT slug FROM partners WHERE id=$1`, otherID).Scan(&otherSlug); e != nil {
		t.Fatal(e)
	}
	buyer := &http.Cookie{Name: "__Host-sf_buyer", Value: security.Token()}
	in := map[string]any{"key": security.ID(), "plan": "plus", "email": "buyer@example.test", "partner": slug, "expectedPrice": 1000}
	order := object(t, request(h, "POST", "/api/orders", in, buyer), 201)["order"].(map[string]any)
	orderID := order["id"].(string)
	path := "/api/partner/product-order"
	initial := object(t, request(h, "GET", path, nil, partner), 200)
	if len(initial["hiddenProductIds"].([]any)) != 0 {
		t.Fatal("existing stores should list all products")
	}
	ids := catalog.NormalizeProductOrder(nil)
	save := map[string]any{"productIds": ids, "hiddenProductIds": []string{"chatgpt", "claude"}, "version": 0}
	object(t, request(h, "POST", path, save, nil), 401)
	saved := object(t, request(h, "POST", path, save, partner), 200)
	if saved["version"] != float64(1) || len(saved["hiddenProductIds"].([]any)) != 2 {
		t.Fatal("visibility not saved")
	}
	object(t, request(h, "POST", path, save, partner), 409)
	preview := object(t, request(h, "GET", "/api/admin/partners/"+id+"/product-order", nil, admin), 200)
	if len(preview["hiddenProductIds"].([]any)) != 2 {
		t.Fatal("admin preview lost hidden settings")
	}
	untouched := object(t, request(h, "GET", path, nil, other), 200)
	if len(untouched["hiddenProductIds"].([]any)) != 0 || untouched["version"] != float64(0) {
		t.Fatal("another partner's settings changed")
	}
	for _, shop := range []string{"", slug, otherSlug} {
		data := object(t, request(h, "GET", "/api/catalog?partner="+shop, nil, nil), 200)
		expected := 3
		if shop == slug {
			expected = 1
		}
		if len(data["plans"].([]any)) != expected {
			t.Fatalf("incorrect catalog for %q: %+v", shop, data["plans"])
		}
	}
	// A saved link cannot create any package belonging to an unlisted product.
	for _, plan := range []string{"plus", "pro-5x"} {
		amount := 1000
		if plan == "pro-5x" {
			amount = 2000
		}
		object(t, request(h, "POST", "/api/orders", map[string]any{"key": security.ID(), "plan": plan, "email": "buyer@example.test", "partner": slug, "expectedPrice": amount}, buyer), 409)
	}
	// Existing order retries, payment, and support remain available.
	object(t, request(h, "POST", "/api/orders", in, buyer), 200)
	object(t, request(h, "GET", "/api/orders/"+orderID, nil, buyer), 200)
	object(t, request(h, "POST", "/api/orders/"+orderID+"/demo-actions", map[string]any{"action": "pay", "version": 0}, buyer), 200)
	object(t, request(h, "POST", "/api/orders/"+orderID+"/refunds", map[string]any{"key": security.ID(), "reason": "existing order support", "version": 1}, buyer), 201)
	// Legacy sort-only clients must not accidentally relist products.
	legacy := object(t, request(h, "POST", path, map[string]any{"productIds": ids, "version": 1}, partner), 200)
	if len(legacy["hiddenProductIds"].([]any)) != 2 {
		t.Fatal("sorting reset visibility")
	}
	for _, hidden := range []any{[]string{"chatgpt", "chatgpt"}, []string{"invalid"}, "chatgpt"} {
		object(t, request(h, "POST", path, map[string]any{"productIds": ids, "hiddenProductIds": hidden, "version": 2}, partner), 400)
	}
	object(t, request(h, "POST", path, map[string]any{"productIds": ids, "hiddenProductIds": ids, "version": 2}, partner), 200)
	empty := object(t, request(h, "GET", "/api/catalog?partner="+slug, nil, nil), 200)
	if len(empty["plans"].([]any)) != 0 || len(empty["hiddenProductIds"].([]any)) != len(ids) {
		t.Fatal("all products should be hidden")
	}
	object(t, request(h, "POST", path, map[string]any{"productIds": ids, "hiddenProductIds": []string{}, "version": 3}, partner), 200)
	in["key"] = security.ID()
	object(t, request(h, "POST", "/api/orders", in, buyer), 201)
	// A partner cannot undo the platform's sales switch by relisting a product.
	execSQL(t, owner, `UPDATE catalog_plans SET partner_enabled=false WHERE product_id='chatgpt'`)
	in["key"] = security.ID()
	object(t, request(h, "POST", "/api/orders", in, buyer), 409)
	var audited int
	if e := owner.DB.QueryRow(context.Background(), `SELECT count(*) FROM audit_events WHERE target=$1 AND action='partner-product-order'`, id).Scan(&audited); e != nil || audited != 4 {
		t.Fatal("settings audit count incorrect", audited, e)
	}
}

func TestNewOrderWaitsForProductVisibilityCommit(t *testing.T) {
	owner, app, auth, _, _ := identityFixture(t)
	h := httpapi.Router(app, "fixture", slog.New(slog.NewTextHandler(io.Discard, nil)), httpapi.Options{Origin: testOrigin, Auth: auth, OrderTesting: true})
	execSQL(t, owner, `INSERT INTO catalog_plans(id,product_id,name,price_cents,enabled,partner_enabled) VALUES('plus','chatgpt','Plus',1000,true,true);
 INSERT INTO partners(id,slug,name,active) VALUES('visibility-lock','ABC123','Partner',true);
 INSERT INTO partner_prices(partner_id,plan,price_cents,cost_cents) VALUES('visibility-lock','plus',1000,500)`)
	ctx := context.Background()
	tx, e := owner.DB.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('visibility-lock',4))`); e != nil {
		t.Fatal(e)
	}
	done := make(chan int, 1)
	go func() {
		done <- request(h, "POST", "/api/orders", map[string]any{"key": security.ID(), "plan": "plus", "email": "buyer@example.test", "partner": "ABC123", "expectedPrice": 1000}, &http.Cookie{Name: "__Host-sf_buyer", Value: security.Token()}).Code
	}()
	select {
	case code := <-done:
		t.Fatalf("order bypassed visibility lock: %d", code)
	case <-time.After(100 * time.Millisecond):
	}
	if _, e = tx.Exec(ctx, `INSERT INTO partner_order_versions(partner_id,version,hidden_product_ids) VALUES('visibility-lock',1,'["chatgpt"]')`); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	select {
	case code := <-done:
		if code != 409 {
			t.Fatalf("order crossed delisting: %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("order stuck after visibility commit")
	}
	var count int
	if e = owner.DB.QueryRow(ctx, `SELECT count(*) FROM orders`).Scan(&count); e != nil || count != 0 {
		t.Fatal("delisted order persisted", count, e)
	}
}
