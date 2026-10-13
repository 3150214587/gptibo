package integration_test

import (
	"context"
	"testing"
	"time"

	"gptibo/storefront/internal/catalog"
	"gptibo/storefront/internal/management"
	"gptibo/storefront/internal/security"
)

func TestWorkspaceContractsAndDefaultPriceIsolation(t *testing.T) {
	owner, _, _, h, mail := identityFixture(t)
	admin := login(t, h, mail, "admin", "")
	object(t, request(h, "POST", "/api/admin/plans", map[string]any{"id": "plus", "productId": "chatgpt", "name": "Plus", "priceCents": 16000, "defaultCostCents": 10000, "enabled": true, "partnerEnabled": true, "note": "fixture"}, admin), 200)
	created := object(t, request(h, "POST", "/api/admin/partners", map[string]any{"email": "workspace@example.test"}, admin), 200)
	id := created["id"].(string)
	partner := login(t, h, mail, "partner", "workspace@example.test")
	now := time.Now().UnixMilli()
	execSQL(t, owner, `INSERT INTO orders(id,owner_hash,idempotency_key,plan,plan_name,price_cents,cost_cents,partner_id,mode,payment_status,status,created_at,updated_at,last_event_id) VALUES('fixture-order','fixture','fixture','plus','Plus',16000,10000,$1,'live','paid','success',$2,$2,'fixture')`, id, now)
	dashboard := object(t, request(h, "GET", "/api/partner/dashboard", nil, partner), 200)
	if dashboard["stats"].(map[string]any)["available"] != float64(6000) {
		t.Fatal("dashboard omitted actual earnings")
	}
	detail := object(t, request(h, "GET", "/api/admin/partners/"+id, nil, admin), 200)
	if detail["audit"] == nil || detail["payouts"] == nil || detail["totals"] == nil {
		t.Fatal("existing frontend contract missing")
	}
	object(t, request(h, "GET", "/api/admin/partners/"+id+"/preview", nil, admin), 200)
	summary := object(t, request(h, "GET", "/api/partner/order-summary?period=today", nil, partner), 200)
	if summary["totals"].(map[string]any)["profitCents"] != float64(6000) {
		t.Fatal("summary differs from payout formula")
	}
	settings := object(t, request(h, "GET", "/api/admin/partners/default-prices", nil, admin), 200)
	save := map[string]any{"version": settings["version"], "prices": []any{map[string]any{"plan": "plus", "costCents": 11000, "catalogVersion": 0}}}
	object(t, request(h, "POST", "/api/admin/partners/default-prices", save, admin), 200)
	object(t, request(h, "POST", "/api/admin/partners/default-prices", save, admin), 409)
	var cost int64
	if e := owner.DB.QueryRow(context.Background(), `SELECT cost_cents FROM partner_prices WHERE partner_id=$1 AND plan='plus'`, id).Scan(&cost); e != nil || cost != 10000 {
		t.Fatal("default overwrote existing partner price", e)
	}
	order := catalog.NormalizeProductOrder(nil)
	order[0], order[1] = order[1], order[0]
	object(t, request(h, "POST", "/api/partner/product-order", map[string]any{"version": 0, "productIds": order}, partner), 200)
	object(t, request(h, "POST", "/api/partner/product-order", map[string]any{"version": 0, "productIds": order}, partner), 409)
	public := object(t, request(h, "GET", "/api/catalog?partner="+created["slug"].(string), nil, nil), 200)
	if public["productOrder"].([]any)[0] != "gemini" {
		t.Fatal("saved ordering not visible")
	}
	object(t, request(h, "POST", "/api/admin/partners/"+id+"/product-order", map[string]any{"version": 1, "productIds": order}, admin), 405)
}

func TestCustomerMembershipIsolationIdempotencyAndExport(t *testing.T) {
	owner, _, _, h, mail := identityFixture(t)
	admin := login(t, h, mail, "admin", "")
	execSQL(t, owner, `INSERT INTO catalog_plans(id,product_id,name,price_cents) VALUES('plus','chatgpt','Plus',16000),('pro-5x','chatgpt','Pro',70000)`)
	object(t, request(h, "POST", "/api/admin/partners", map[string]any{"email": "crm-a@example.test"}, admin), 200)
	object(t, request(h, "POST", "/api/admin/partners", map[string]any{"email": "crm-b@example.test"}, admin), 200)
	ca := login(t, h, mail, "partner", "crm-a@example.test")
	cb := login(t, h, mail, "partner", "crm-b@example.test")
	id := security.ID()
	create := map[string]any{"key": id, "name": "客户甲", "wechat": "fixture", "email": "client@example.test", "source": "web", "note": "备注"}
	object(t, request(h, "POST", "/api/partner/customers", create, ca), 200)
	object(t, request(h, "POST", "/api/partner/customers", create, ca), 200)
	create["name"] = "changed"
	object(t, request(h, "POST", "/api/partner/customers", create, ca), 409)
	object(t, request(h, "GET", "/api/partner/customers/"+id, nil, cb), 404)
	account := security.ID()
	object(t, request(h, "POST", "/api/partner/customers/"+id+"/accounts", map[string]any{"key": account, "account": "member@example.test", "note": "fixture"}, ca), 200)
	opened := time.Now().UnixMilli() - 1000
	sub := map[string]any{"key": security.ID(), "version": 0, "plan": "plus", "kind": "open", "paidCents": 16000, "openedAt": opened, "note": "fixture"}
	object(t, request(h, "POST", "/api/partner/customers/accounts/"+account+"/subscriptions", sub, ca), 200)
	object(t, request(h, "POST", "/api/partner/customers/accounts/"+account+"/subscriptions", sub, ca), 200)
	sub["key"] = security.ID()
	sub["version"] = 1
	sub["kind"] = "renew"
	object(t, request(h, "POST", "/api/partner/customers/accounts/"+account+"/subscriptions", sub, ca), 409)
	sub["kind"] = "upgrade"
	sub["plan"] = "pro-5x"
	object(t, request(h, "POST", "/api/partner/customers/accounts/"+account+"/subscriptions", sub, ca), 200)
	list := object(t, request(h, "GET", "/api/partner/customers?plan=pro-5x&expiry=active&q=member", nil, ca), 200)
	if list["total"] != float64(1) || list["stats"].(map[string]any)["paid_cents"] != float64(32000) {
		t.Fatal("CRM totals or filters wrong")
	}
	exported := object(t, request(h, "GET", "/api/partner/customers/export", nil, ca), 200)
	if len(exported["subscriptions"].([]any)) != 2 {
		t.Fatal("history lost")
	}
	isolated := object(t, request(h, "GET", "/api/partner/customers/export", nil, cb), 200)
	if len(isolated["customers"].([]any)) != 0 {
		t.Fatal("cross-partner export")
	}
	object(t, request(h, "GET", "/api/admin/customers/"+id, nil, admin), 200)
	loc := time.FixedZone("CST", 8*3600)
	jan := time.Date(2024, 1, 31, 23, 30, 0, 0, loc)
	if got := time.UnixMilli(management.MonthAfter(jan.UnixMilli())).In(loc); got.Day() != 29 || got.Month() != 2 || got.Hour() != 23 {
		t.Fatal("calendar month clamping failed")
	}
}

func TestVisitorAttributionAndInterestCancellation(t *testing.T) {
	_, _, _, h, mail := identityFixture(t)
	admin := login(t, h, mail, "admin", "")
	created := object(t, request(h, "POST", "/api/admin/partners", map[string]any{"email": "visits@example.test"}, admin), 200)
	partner := login(t, h, mail, "partner", "visits@example.test")
	visitor := security.ID()
	for _, path := range []string{"/", "/r/" + created["slug"].(string), "/r/" + created["slug"].(string) + "/orders"} {
		object(t, request(h, "POST", "/api/visitors", map[string]any{"visitorId": visitor, "path": path}, nil), 200)
	}
	own := object(t, request(h, "GET", "/api/partner/visitors", nil, partner), 200)
	all := object(t, request(h, "GET", "/api/admin/visitors", nil, admin), 200)
	if own["pageViews"] != float64(2) || own["visitors"] != float64(1) || all["pageViews"] != float64(3) || all["visitors"] != float64(1) || len(own["trend"].([]any)) != 7 {
		t.Fatal("visitor aggregation incorrect")
	}
	object(t, request(h, "GET", "/api/admin/partners/ranking", nil, admin), 200)
	token := security.Token()
	in := map[string]any{"token": token, "email": "interest@example.test", "product": "claude", "consent": true}
	object(t, request(h, "POST", "/api/interest", in, nil), 200)
	in["token"] = security.Token()
	other := object(t, request(h, "POST", "/api/interest", in, nil), 200)
	if other["manageable"] != false {
		t.Fatal("duplicate signup leaked cancellation capability")
	}
	object(t, request(h, "POST", "/api/interest/cancel", map[string]any{"token": in["token"]}, nil), 200)
	list := object(t, request(h, "GET", "/api/admin/interest", nil, admin), 200)
	if list["total"] != float64(1) {
		t.Fatal("foreign cancellation removed signup")
	}
	object(t, request(h, "POST", "/api/interest/cancel", map[string]any{"token": token}, nil), 200)
	list = object(t, request(h, "GET", "/api/admin/interest", nil, admin), 200)
	if list["total"] != float64(0) {
		t.Fatal("cancellation failed")
	}
}
