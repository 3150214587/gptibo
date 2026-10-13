package integration_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"
	"gptibo/storefront/internal/catalog"
)

func TestCatalogCompletedOrdersByProduct(t *testing.T) {
	owner, app := fixture(t, true)
	execSQL(t, owner, `INSERT INTO catalog_plans(id,product_id,name,price_cents,enabled,partner_enabled) VALUES
 ('plus','chatgpt','Plus',10000,true,true),('pro-5x','chatgpt','Pro',20000,true,true),
 ('gemini','gemini','Gemini',10000,true,true),('gmail','gmail','Gmail',10000,true,true);
 INSERT INTO partners(id,slug,name,active) VALUES('a','ABC123','A',true),('b','DEF456','B',true);
 INSERT INTO partner_prices(partner_id,plan,price_cents,cost_cents)
 SELECT p.id,c.id,c.price_cents,5000 FROM partners p CROSS JOIN catalog_plans c;
 INSERT INTO orders(id,owner_hash,idempotency_key,plan,plan_name,price_cents,partner_id,mode,payment_status,status,created_at,updated_at,last_event_id)
 SELECT id,'buyer',id,plan,plan,10000,partner,'live','paid','success',1,1,'fixture'
 FROM (VALUES ('main','plus',NULL),('a-plus','plus','a'),('a-pro','pro-5x','a'),
 ('b-plus','plus','b'),('a-gemini','gemini','a'),('test','plus','a'),('demo','plus','a'),
 ('unpaid','plus','a'),('pending','plus','a'),('processing','plus','a'),('failed','plus','a'),
 ('cancelled','plus','a'),('partial-refund','plus','a'),('full-refund','plus','a'),('hold','plus','a')) AS f(id,plan,partner);
 UPDATE orders SET is_test=1 WHERE id='test';
 UPDATE orders SET mode='demo',payment_status='simulated' WHERE id='demo';
 UPDATE orders SET payment_status='unpaid' WHERE id='unpaid';
 UPDATE orders SET status=id WHERE id IN ('pending','processing','failed','cancelled');
 UPDATE orders SET refunded_cents=100 WHERE id='partial-refund';
 UPDATE orders SET refunded_cents=10000 WHERE id='full-refund';
 UPDATE orders SET refund_hold=1 WHERE id='hold';
 UPDATE partners SET parent_id='a' WHERE id='b';
 UPDATE orders SET upstream_partner_id='a',upstream_cost_cents=4000,cost_cents=5000 WHERE id='b-plus';`)
	svc := catalog.Service{Store: app}
	check := func(slug string, want map[string]int64) {
		t.Helper()
		got, err := svc.Read(context.Background(), slug)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got.CompletedOrdersByProduct, want) {
			t.Fatalf("shop %q counts = %v, want %v", slug, got.CompletedOrdersByProduct, want)
		}
	}
	check("", map[string]int64{"chatgpt": 4, "gemini": 1, "gmail": 0})
	check("ABC123", map[string]int64{"chatgpt": 4, "gemini": 1, "gmail": 0})
	check("DEF456", map[string]int64{"chatgpt": 4, "gemini": 1, "gmail": 0})
	// Completing an existing order is visible on the next catalog refresh.
	execSQL(t, owner, `UPDATE orders SET status='success' WHERE id='processing'`)
	check("ABC123", map[string]int64{"chatgpt": 5, "gemini": 1, "gmail": 0})
	execSQL(t, owner, `UPDATE orders SET refunded_cents=10000 WHERE id='a-pro';
 INSERT INTO partner_order_versions(partner_id,hidden_product_ids) VALUES('a','["gemini"]')`)
	check("ABC123", map[string]int64{"chatgpt": 4, "gmail": 0})
}

func TestCatalogGlobalCountCacheKeepsShopSettingsFresh(t *testing.T) {
	owner, app := fixture(t, true)
	execSQL(t, owner, `INSERT INTO partners(id,slug,name,active) VALUES('a','ABC123','A',true),('b','DEF456','B',true)`)
	completedDomainOrders(t, owner, "a", 2)
	completedDomainOrders(t, owner, "b", 1)
	execSQL(t, owner, `UPDATE catalog_plans SET enabled=true,partner_enabled=true;
 INSERT INTO partner_prices(partner_id,plan,price_cents,cost_cents) VALUES('a','plus',12000,5000),('b','plus',13000,5000)`)
	svc := catalog.Service{Store: app, CompletedOrders: catalog.NewCompletedOrderCache(app)}
	first, err := svc.Read(context.Background(), "ABC123")
	if err != nil || first.CompletedOrdersByProduct["chatgpt"] != 3 {
		t.Fatalf("initial global total: %v %v", first.CompletedOrdersByProduct, err)
	}
	execSQL(t, owner, `UPDATE orders SET status='processing' WHERE partner_id='a';
 UPDATE partner_prices SET price_cents=14000 WHERE partner_id='b'`)
	for _, slug := range []string{"", "DEF456"} {
		got, err := svc.Read(context.Background(), slug)
		if err != nil || got.CompletedOrdersByProduct["chatgpt"] != 3 {
			t.Fatalf("shop %q did not share cached platform total: %v %v", slug, got.CompletedOrdersByProduct, err)
		}
		if slug != "" && got.Plans[0].PriceCents != 14000 {
			t.Fatal("price change was incorrectly cached")
		}
	}
	execSQL(t, owner, `INSERT INTO partner_order_versions(partner_id,hidden_product_ids) VALUES('a','["chatgpt"]')`)
	hidden, err := svc.Read(context.Background(), "ABC123")
	if err != nil || len(hidden.Plans) != 0 || len(hidden.CompletedOrdersByProduct) != 0 {
		t.Fatal("cached counts bypassed shop visibility")
	}
	execSQL(t, owner, `UPDATE partners SET active=false WHERE id='b'`)
	if _, err := svc.Read(context.Background(), "DEF456"); err != catalog.ErrShopUnavailable {
		t.Fatal("cached counts bypassed disabled shop")
	}
}

func TestCatalogCountFailureDoesNotDisableCheckout(t *testing.T) {
	owner, app := fixture(t, true)
	execSQL(t, owner, `INSERT INTO catalog_plans(id,product_id,name,price_cents,enabled) VALUES('plus','chatgpt','Plus',10000,true)`)
	// Drop only the count reader's access; catalog prices and checkout stay valid.
	var role string
	if err := app.DB.QueryRow(context.Background(), "SELECT current_user").Scan(&role); err != nil {
		t.Fatal(err)
	}
	execSQL(t, owner, "REVOKE SELECT ON orders FROM "+pgx.Identifier{role}.Sanitize())
	svc := catalog.Service{Store: app, CompletedOrders: catalog.NewCompletedOrderCache(app), Availability: func(_ context.Context, plans []catalog.Plan) []catalog.Plan { return plans }}
	got, err := svc.Read(context.Background(), "")
	if err != nil || len(got.Plans) != 1 || !got.CheckoutEnabled || got.CompletedOrdersByProduct != nil {
		t.Fatalf("statistics failure broke catalog or fabricated counts: %+v %v", got, err)
	}
}
