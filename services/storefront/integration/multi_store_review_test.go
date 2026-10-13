package integration_test

import (
	"context"
	"testing"
)

func TestMultiStoreDefaultPriceRejectsStaleLegacyEditor(t *testing.T) {
	f := hierarchySetup(t)
	// A new store editor has version 0 before the legacy endpoint first writes.
	object(t, request(f.h, "POST", "/api/partner/prices/plus", map[string]any{"priceCents": 15000, "version": 1}, f.child), 200)
	object(t, request(f.h, "POST", "/api/partner/stores/prices", map[string]any{"storeId": f.childID, "plan": "plus", "priceCents": 14000, "enabled": true, "version": 0}, f.child), 409)
	object(t, request(f.h, "POST", "/api/partner/stores/prices", map[string]any{"storeId": f.childID, "plan": "plus", "priceCents": 16000, "enabled": false, "version": 1}, f.child), 200)
	// A later legacy price write must preserve the store's disabled status.
	object(t, request(f.h, "POST", "/api/partner/prices/plus", map[string]any{"priceCents": 17000, "version": 3}, f.child), 200)
	var price int64
	var enabled bool
	if e := f.owner.DB.QueryRow(context.Background(), `SELECT price_cents,enabled FROM store_prices WHERE store_id=$1 AND plan='plus'`, f.childID).Scan(&price, &enabled); e != nil || price != 17000 || enabled {
		t.Fatal("legacy editor lost price or store availability", price, enabled, e)
	}
}

func TestMultiStoreCouponQuoteHonorsSupplyAndStoreVisibility(t *testing.T) {
	f := hierarchySetup(t)
	shop := makeShop(t, f, "Second")
	object(t, request(f.h, "POST", "/api/partner/coupons", couponInput("VISIBLE", []map[string]string{{"storeId": f.childID, "plan": "plus"}, {"storeId": shop["id"].(string), "plan": "plus"}}, 1000, 20), f.child), 200)
	quote := map[string]any{"partner": f.slug, "plan": "plus", "email": "quote@example.test", "code": "VISIBLE"}
	object(t, request(f.h, "POST", "/api/orders/coupon-quote", quote, nil), 200)
	execSQL(t, f.owner, `INSERT INTO partner_order_versions(partner_id,hidden_product_ids) VALUES($1,'["chatgpt"]') ON CONFLICT(partner_id) DO UPDATE SET hidden_product_ids='["chatgpt"]'`, f.childID)
	object(t, request(f.h, "POST", "/api/orders/coupon-quote", quote, nil), 404)
	// Legacy product visibility affects only the default store.
	quote["partner"] = shop["slug"]
	object(t, request(f.h, "POST", "/api/orders/coupon-quote", quote, nil), 200)
	execSQL(t, f.owner, `UPDATE partner_prices SET enabled=false WHERE partner_id=$1 AND plan='plus'`, f.parentID)
	object(t, request(f.h, "POST", "/api/orders/coupon-quote", quote, nil), 404)
	execSQL(t, f.owner, `UPDATE partner_prices SET enabled=true,cost_cents=12000 WHERE partner_id=$1 AND plan='plus'`, f.parentID)
	object(t, request(f.h, "POST", "/api/orders/coupon-quote", quote, nil), 404)
}

func TestMultiStoreSupplyQuoteVersionAndRetailIndependence(t *testing.T) {
	f := hierarchySetup(t)
	object(t, request(f.h, "POST", "/api/partner/supply-quotes", map[string]any{"plan": "plus", "costCents": 12000, "version": 0}, f.parent), 200)
	object(t, request(f.h, "POST", "/api/partner/supply-quotes", map[string]any{"plan": "plus", "costCents": 12500, "version": 0}, f.parent), 409)
	object(t, request(f.h, "POST", "/api/partner/supply-quotes", map[string]any{"plan": "plus", "costCents": 9000, "version": 1}, f.parent), 409)
	object(t, request(f.h, "POST", "/api/partner/stores/prices", map[string]any{"storeId": f.parentID, "plan": "plus", "priceCents": 15000, "enabled": true, "version": 0}, f.parent), 200)
	child := object(t, request(f.h, "POST", "/api/partner/children", map[string]any{"email": "independent-child@example.test"}, f.parent), 200)
	var cost, price int64
	if e := f.owner.DB.QueryRow(context.Background(), `SELECT cost_cents,price_cents FROM partner_prices WHERE partner_id=$1 AND plan='plus'`, child["id"]).Scan(&cost, &price); e != nil || cost != 12000 || price != 12000 {
		t.Fatal("new child uses retail price or stale supply quote", cost, price, e)
	}
}
