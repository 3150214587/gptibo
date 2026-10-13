package integration_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"gptibo/storefront/internal/security"
)

func TestCatalogCouponVisibilityIsScopedAndUsable(t *testing.T) {
	f := hierarchySetup(t)
	object(t, request(f.h, "POST", "/api/admin/plans", map[string]any{"id": "pro-5x", "productId": "chatgpt", "name": "Pro", "priceCents": 15000, "defaultCostCents": 10000, "enabled": true, "partnerEnabled": true, "note": "fixture"}, f.admin), 200)
	shop := makeShop(t, f, "Coupon shop")
	id, slug := shop["id"].(string), shop["slug"].(string)
	check := func(shop string, want bool) {
		t.Helper()
		out := object(t, request(f.h, "GET", "/api/catalog?partner="+shop, nil, nil), 200)
		encoded, _ := json.Marshal(out)
		if strings.Contains(string(encoded), "SECRET_PROMO") {
			t.Fatal("catalog leaked coupon code")
		}
		found := false
		for _, value := range out["plans"].([]any) {
			p := value.(map[string]any)
			if p["id"] == "plus" {
				found = true
			}
			if p["has_coupons"] != (want && p["id"] == "plus") {
				t.Fatalf("shop=%s plan=%v availability=%v expected=%v", shop, p["id"], p["has_coupons"], want)
			}
		}
		if !found {
			t.Fatal("missing plan")
		}
	}
	check(slug, false)
	object(t, request(f.h, "POST", "/api/partner/coupons", couponInput("SECRET_PROMO", []map[string]string{{"storeId": id, "plan": "plus"}}, 1000, 1), f.child), 200)
	check(slug, true)
	check(f.slug, false)
	check("", false)
	for _, sql := range []string{
		`UPDATE partner_coupons SET active=false`,
		`UPDATE partner_coupons SET active=true,starts_at=now()+interval '10 minutes'`,
		`UPDATE partner_coupons SET starts_at=now()-interval '2 hours',ends_at=now()-interval '1 hour'`,
	} {
		execSQL(t, f.owner, sql)
		check(slug, false)
	}
	execSQL(t, f.owner, `UPDATE partner_coupons SET ends_at=now()+interval '1 hour'`)
	check(slug, true)
	// A later price reduction may make an otherwise valid code violate the cost floor.
	execSQL(t, f.owner, `UPDATE store_prices SET price_cents=11000 WHERE store_id=$1 AND plan='plus'`, id)
	check(slug, false)
	execSQL(t, f.owner, `UPDATE store_prices SET price_cents=13000 WHERE store_id=$1 AND plan='plus'`, id)
	check(slug, true)
	buyer := &http.Cookie{Name: "__Host-sf_buyer", Value: security.Token()}
	object(t, request(f.h, "POST", "/api/orders", map[string]any{"key": security.ID(), "plan": "plus", "email": "buyer@example.test", "partner": slug, "couponCode": "SECRET_PROMO", "expectedPrice": 12000}, buyer), 201)
	check(slug, false)
	// Another usable code keeps the field available when the first is exhausted.
	object(t, request(f.h, "POST", "/api/partner/coupons", couponInput("ANOTHER", []map[string]string{{"storeId": id, "plan": "plus"}}, 500, 5), f.child), 200)
	check(slug, true)
}
