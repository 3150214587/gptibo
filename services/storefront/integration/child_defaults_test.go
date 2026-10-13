package integration_test

import (
	"context"
	"net/http"
	"testing"
)

func defaultPrices(t *testing.T, f hierarchyFixture, cost float64) map[string]any {
	t.Helper()
	data := object(t, request(f.h, "GET", "/api/partner/child-default-prices", nil, f.parent), 200)
	prices := []map[string]any{}
	for _, value := range data["plans"].([]any) {
		p := value.(map[string]any)
		if p["id"] == "plus" {
			p["cost_cents"] = cost
		}
		prices = append(prices, map[string]any{"plan": p["id"], "costCents": p["cost_cents"], "version": p["version"]})
	}
	return map[string]any{"prices": prices}
}
func assertChildCost(t *testing.T, f hierarchyFixture, id string, want int64) {
	t.Helper()
	var cost int64
	if e := f.owner.DB.QueryRow(context.Background(), `SELECT cost_cents FROM partner_prices WHERE partner_id=$1 AND plan='plus'`, id).Scan(&cost); e != nil || cost != want {
		t.Fatalf("cost=%d want=%d error=%v", cost, want, e)
	}
}

func TestChildDefaultsSaveInheritanceAndIsolation(t *testing.T) {
	f := hierarchySetup(t)
	in := defaultPrices(t, f, 12000)
	object(t, request(f.h, "POST", "/api/partner/child-default-prices", in, f.parent), 200)
	assertChildCost(t, f, f.childID, 11000)
	assertChildCost(t, f, f.parentID, 10000)
	child := object(t, request(f.h, "POST", "/api/partner/children", map[string]any{"email": "default-child@example.test"}, f.parent), 200)
	assertChildCost(t, f, child["id"].(string), 12000)
	object(t, request(f.h, "POST", "/api/partner/child-default-prices", in, f.parent), 409)
	object(t, request(f.h, "POST", "/api/partner/child-default-prices/apply", in, f.parent), 409)
	object(t, request(f.h, "POST", "/api/partner/child-default-prices", defaultPrices(t, f, 9999), f.parent), 400)
	for _, cookie := range []*http.Cookie{nil, f.child} {
		status := 401
		if cookie != nil {
			status = 403
		}
		for _, path := range []string{"/api/partner/child-default-prices", "/api/partner/child-default-prices/apply"} {
			object(t, request(f.h, "POST", path, in, cookie), status)
		}
		object(t, request(f.h, "GET", "/api/partner/child-default-prices", nil, cookie), status)
	}
}

func TestChildDefaultsApplyAllPreservesRetailHistoryAndAvailability(t *testing.T) {
	f := hierarchySetup(t)
	second := object(t, request(f.h, "POST", "/api/partner/children", map[string]any{"email": "second-default@example.test"}, f.parent), 200)["id"].(string)
	execSQL(t, f.owner, `UPDATE partners SET active=false WHERE id=$1`, second)
	execSQL(t, f.owner, `UPDATE partner_prices SET enabled=false WHERE partner_id=$1 AND plan='plus'`, second)
	other := object(t, request(f.h, "POST", "/api/admin/partners", map[string]any{"email": "unrelated-default@example.test"}, f.admin), 200)["id"].(string)
	execSQL(t, f.owner, `INSERT INTO orders(id,owner_hash,idempotency_key,plan,plan_name,price_cents,cost_cents,partner_id,mode,payment_status,status,created_at,updated_at,last_event_id) VALUES('default-history','fixture','default-history','plus','Plus',13000,11000,$1,'live','paid','success',1,1,'fixture')`, f.childID)
	object(t, request(f.h, "POST", "/api/partner/child-default-prices", defaultPrices(t, f, 12000), f.parent), 200)
	in := defaultPrices(t, f, 12000)
	result := object(t, request(f.h, "POST", "/api/partner/child-default-prices/apply", in, f.parent), 200)
	if result["children"] != float64(2) {
		t.Fatal(result)
	}
	assertChildCost(t, f, f.childID, 12000)
	assertChildCost(t, f, second, 12000)
	assertChildCost(t, f, other, 10000)
	var price, history, audits int64
	var enabled bool
	if e := f.owner.DB.QueryRow(context.Background(), `SELECT price_cents,enabled,(SELECT cost_cents FROM orders WHERE id='default-history'),(SELECT count(*) FROM audit_events WHERE action='child-default-prices-apply') FROM partner_prices WHERE partner_id=$1 AND plan='plus'`, second).Scan(&price, &enabled, &history, &audits); e != nil || price != 13000 || enabled || history != 11000 || audits != 1 {
		t.Fatal(price, enabled, history, audits, e)
	}
	// An old per-child editor cannot undo the batch unnoticed.
	object(t, request(f.h, "POST", "/api/partner/children/"+f.childID+"/prices/plus", map[string]any{"costCents": 11000, "enabled": true, "version": 1}, f.parent), 409)
	// Repeating the same application is safe and leaves child revisions unchanged.
	object(t, request(f.h, "POST", "/api/partner/child-default-prices/apply", in, f.parent), 200)
	var version int64
	if e := f.owner.DB.QueryRow(context.Background(), `SELECT version FROM partner_prices WHERE partner_id=$1 AND plan='plus'`, f.childID).Scan(&version); e != nil || version != 2 {
		t.Fatal(version, e)
	}
}

func TestChildDefaultsConflictsRollbackEntireBatch(t *testing.T) {
	for _, reason := range []string{"retail", "coupon", "audit"} {
		t.Run(reason, func(t *testing.T) {
			f := hierarchySetup(t)
			second := object(t, request(f.h, "POST", "/api/partner/children", map[string]any{"email": "blocked-default@example.test"}, f.parent), 200)["id"].(string)
			// Ensure at least one successful row is written before the rejected child.
			ids := []string{f.childID, second}
			if ids[0] > ids[1] {
				ids[0], ids[1] = ids[1], ids[0]
			}
			object(t, request(f.h, "POST", "/api/partner/child-default-prices", defaultPrices(t, f, 12000), f.parent), 200)
			if reason == "retail" {
				execSQL(t, f.owner, `UPDATE partner_prices SET price_cents=11500,cost_cents=11000 WHERE partner_id=$1 AND plan='plus'`, ids[1])
			}
			if reason == "coupon" {
				shop := makeShop(t, f, "Discount shop")
				object(t, request(f.h, "POST", "/api/partner/coupons", couponInput("FLOOR", []map[string]string{{"storeId": shop["id"].(string), "plan": "plus"}}, 1500, 20), f.child), 200)
			}
			if reason == "audit" {
				execSQL(t, f.owner, `CREATE FUNCTION reject_default_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='child-default-prices-apply' THEN RAISE EXCEPTION 'fixture'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_default_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_default_audit()`)
			}
			var before int64
			if e := f.owner.DB.QueryRow(context.Background(), `SELECT sum(cost_cents) FROM partner_prices WHERE partner_id IN ($1,$2)`, f.childID, second).Scan(&before); e != nil {
				t.Fatal(e)
			}
			code := 409
			if reason == "audit" {
				code = 503
			}
			object(t, request(f.h, "POST", "/api/partner/child-default-prices/apply", defaultPrices(t, f, 12000), f.parent), code)
			var after, audits int64
			if e := f.owner.DB.QueryRow(context.Background(), `SELECT sum(cost_cents),(SELECT count(*) FROM audit_events WHERE action='child-default-prices-apply') FROM partner_prices WHERE partner_id IN ($1,$2)`, f.childID, second).Scan(&after, &audits); e != nil || before != after || audits != 0 {
				t.Fatal(before, after, audits, e)
			}
		})
	}
}

func TestChildDefaultsRejectIncompleteDuplicateAndUnsaved(t *testing.T) {
	f := hierarchySetup(t)
	in := defaultPrices(t, f, 12000)
	object(t, request(f.h, "POST", "/api/partner/child-default-prices/apply", in, f.parent), 409)
	prices := in["prices"].([]map[string]any)
	object(t, request(f.h, "POST", "/api/partner/child-default-prices", map[string]any{"prices": prices[:len(prices)-1]}, f.parent), 400)
	in["prices"] = append(prices, prices[0])
	object(t, request(f.h, "POST", "/api/partner/child-default-prices", in, f.parent), 400)
}

func TestChildDefaultsCompleteSetAndConcurrentSave(t *testing.T) {
	f := hierarchySetup(t)
	object(t, request(f.h, "POST", "/api/admin/plans", map[string]any{"id": "gemini", "productId": "gemini", "name": "Gemini", "priceCents": 9000, "defaultCostCents": 6000, "enabled": true, "partnerEnabled": true, "note": "fixture"}, f.admin), 200)
	in := defaultPrices(t, f, 12000)
	prices := in["prices"].([]map[string]any)
	object(t, request(f.h, "POST", "/api/partner/child-default-prices", map[string]any{"prices": prices[:1]}, f.parent), 409)
	stale := defaultPrices(t, f, 12000)
	rows := stale["prices"].([]map[string]any)
	rows[len(rows)-1]["version"] = float64(999)
	object(t, request(f.h, "POST", "/api/partner/child-default-prices", stale, f.parent), 409)
	var old int64
	if e := f.owner.DB.QueryRow(context.Background(), `SELECT child_cost_cents FROM partner_prices WHERE partner_id=$1 AND plan='plus'`, f.parentID).Scan(&old); e != nil || old != 13000 {
		t.Fatal(old, e)
	}
	codes := make(chan int, 2)
	for range 2 {
		go func() { codes <- request(f.h, "POST", "/api/partner/child-default-prices", in, f.parent).Code }()
	}
	a, b := <-codes, <-codes
	if !((a == 200 && b == 409) || (a == 409 && b == 200)) {
		t.Fatal(a, b)
	}
}
