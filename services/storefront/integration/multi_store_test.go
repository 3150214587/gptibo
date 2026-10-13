package integration_test

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"gptibo/storefront/internal/catalog"
	"gptibo/storefront/internal/domainbilling"
	"gptibo/storefront/internal/domains"
	"gptibo/storefront/internal/payments"
	"gptibo/storefront/internal/security"
)

func storeRows(t *testing.T, f hierarchyFixture, c *http.Cookie) []any {
	t.Helper()
	return object(t, request(f.h, "GET", "/api/partner/stores", nil, c), 200)["stores"].([]any)
}
func makeShop(t *testing.T, f hierarchyFixture, name string) map[string]any {
	t.Helper()
	object(t, request(f.h, "POST", "/api/partner/stores", map[string]any{"name": name, "active": true, "settings": map[string]string{"brandName": name}}, f.child), 200)
	list := storeRows(t, f, f.child)
	return list[len(list)-1].(map[string]any)
}
func couponInput(code string, targets []map[string]string, discount, max int) map[string]any {
	return map[string]any{"code": code, "discountCents": discount, "startsAt": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339), "endsAt": time.Now().Add(time.Hour).UTC().Format(time.RFC3339), "maxUses": max, "oncePerEmail": true, "active": true, "targets": targets}
}
func TestMultiStoreIsolationCouponsAndSnapshots(t *testing.T) {
	f := hierarchySetup(t)
	ctx := context.Background()
	shop := makeShop(t, f, "Second store")
	id := shop["id"].(string)
	slug := shop["slug"].(string)
	object(t, request(f.h, "POST", "/api/partner/stores/prices", map[string]any{"storeId": id, "plan": "plus", "priceCents": 15000, "enabled": true, "version": 0}, f.child), 200)
	object(t, request(f.h, "POST", "/api/partner/stores/prices", map[string]any{"storeId": id, "plan": "plus", "priceCents": 15000, "enabled": true, "version": 1}, f.parent), 404)
	for code, want := range map[string]float64{f.slug: 13000, slug: 15000} {
		r := object(t, request(f.h, "GET", "/api/catalog?partner="+code, nil, nil), 200)
		if r["plans"].([]any)[0].(map[string]any)["price_cents"] != want {
			t.Fatal("store price leaked")
		}
	}
	in := couponInput("SAVE10", []map[string]string{{"storeId": id, "plan": "plus"}}, 1000, 4)
	object(t, request(f.h, "POST", "/api/partner/coupons", in, f.child), 200)
	bad := couponInput("BAD", []map[string]string{{"storeId": f.parentID, "plan": "plus"}}, 1000, 4)
	object(t, request(f.h, "POST", "/api/partner/coupons", bad, f.child), 400)
	quote := map[string]any{"partner": slug, "plan": "plus", "email": " Buyer@Example.Test ", "code": "save10"}
	r := object(t, request(f.h, "POST", "/api/orders/coupon-quote", quote, nil), 200)
	if r["priceCents"] != float64(14000) {
		t.Fatal(r)
	}
	quote["partner"] = f.slug
	object(t, request(f.h, "POST", "/api/orders/coupon-quote", quote, nil), 400)
	buyer := &http.Cookie{Name: "__Host-sf_buyer", Value: security.Token()}
	orderInput := map[string]any{"key": security.ID(), "plan": "plus", "email": "buyer@example.test", "partner": slug, "expectedPrice": 14000, "couponCode": "save10"}
	o := object(t, request(f.h, "POST", "/api/orders", orderInput, buyer), 201)["order"].(map[string]any)
	orderID := o["id"].(string)
	if o["discountCents"] != float64(1000) || o["storeId"] != id {
		t.Fatal("missing discount/store snapshot", o)
	}
	object(t, request(f.h, "POST", "/api/orders", orderInput, buyer), 200)
	orderInput["partner"] = f.slug
	object(t, request(f.h, "POST", "/api/orders", orderInput, buyer), 409)
	orderInput["partner"] = slug
	orderInput["key"] = security.ID()
	object(t, request(f.h, "POST", "/api/orders", orderInput, buyer), 409)
	// A new coupon target does not reset the shared email reservation.
	var couponID string
	if e := f.owner.DB.QueryRow(ctx, `SELECT id FROM partner_coupons WHERE partner_id=$1`, f.childID).Scan(&couponID); e != nil {
		t.Fatal(e)
	}
	in["id"] = couponID
	in["version"] = 0
	in["targets"] = []map[string]string{{"storeId": id, "plan": "plus"}, {"storeId": f.childID, "plan": "plus"}}
	object(t, request(f.h, "POST", "/api/partner/coupons", in, f.child), 200)
	orderInput["partner"] = f.slug
	orderInput["expectedPrice"] = 12000
	object(t, request(f.h, "POST", "/api/orders", orderInput, buyer), 409)
	// Price edits and upstream cost edits cannot put an active coupon below cost.
	object(t, request(f.h, "POST", "/api/partner/stores/prices", map[string]any{"storeId": id, "plan": "plus", "priceCents": 11500, "enabled": true, "version": 1}, f.child), 409)
	object(t, request(f.h, "POST", "/api/partner/children/"+f.childID+"/prices/plus", map[string]any{"costCents": 12500, "enabled": true, "version": 1}, f.parent), 409)
	// Explicitly confirmed unpaid closure releases the reservation, payment/refund keeps it.
	execSQL(t, f.owner, `INSERT INTO payments(order_id,provider,account,amount_cents,expires_at,created_at,updated_at) VALUES($1,'gatewayb-alipay','fixture',14000,$2,$2,$2)`, orderID, time.Now().UnixMilli())
	execSQL(t, f.owner, `UPDATE orders SET payment_status='unpaid',status='cancelled' WHERE id=$1`, orderID)
	object(t, request(f.h, "POST", "/api/orders", orderInput, buyer), 409)
	execSQL(t, f.owner, `UPDATE payments SET state='closed' WHERE order_id=$1`, orderID)
	object(t, request(f.h, "POST", "/api/orders", orderInput, buyer), 201)
	var price, cost, up, discount, list int64
	if e := f.owner.DB.QueryRow(ctx, `SELECT price_cents,cost_cents,upstream_cost_cents,discount_cents,list_price_cents FROM orders WHERE id=$1`, orderID).Scan(&price, &cost, &up, &discount, &list); e != nil || price != 14000 || cost != 11000 || up != 10000 || discount != 1000 || list != 15000 {
		t.Fatal("invalid snapshot", e)
	}
	var childProfit, parentProfit int64
	if e := f.owner.DB.QueryRow(ctx, `SELECT partner_order_profit($1,$2),partner_order_profit($1,$3)`, orderID, f.childID, f.parentID).Scan(&childProfit, &parentProfit); e != nil || childProfit != 3000 || parentProfit != 1000 {
		t.Fatal("coupon changed upstream margin", childProfit, parentProfit, e)
	}
}
func TestMultiStoreNewProductsAndAdminDomainWaiver(t *testing.T) {
	f := hierarchySetup(t)
	shop := makeShop(t, f, "Independent prices")
	object(t, request(f.h, "POST", "/api/admin/plans", map[string]any{"id": "pro-5x", "productId": "chatgpt", "name": "Pro", "priceCents": 30000, "defaultCostCents": 20000, "enabled": true, "partnerEnabled": true, "note": "new product"}, f.admin), 200)
	object(t, request(f.h, "POST", "/api/partner/prices/pro-5x", map[string]any{"priceCents": 35000, "version": 0}, f.child), 200)
	var price int64
	if e := f.owner.DB.QueryRow(context.Background(), `SELECT price_cents FROM store_prices WHERE store_id=$1 AND plan='pro-5x'`, shop["id"]).Scan(&price); e != nil || price != 30000 {
		t.Fatal("new product inherited subsequent default store price", price, e)
	}
	execSQL(t, f.owner, `INSERT INTO partner_domains(id,partner_id,hostname,verification,free) VALUES('admin-domain',$1,'waiver.example.com','v',false)`, f.childID)
	input := map[string]any{"months": 2, "version": 0, "note": "customer service waiver"}
	object(t, request(f.h, "POST", "/api/admin/partners/"+f.childID+"/domains/admin-domain", input, f.child), 401)
	object(t, request(f.h, "POST", "/api/admin/partners/"+f.childID+"/domains/admin-domain", input, f.admin), 200)
	object(t, request(f.h, "POST", "/api/admin/partners/"+f.childID+"/domains/admin-domain", input, f.admin), 409)
	var valid bool
	if e := f.owner.DB.QueryRow(context.Background(), `SELECT expires_at>now()+interval '55 days' AND billing_version=1 FROM partner_domains WHERE id='admin-domain'`).Scan(&valid); e != nil || !valid {
		t.Fatal("waiver period wrong", e)
	}
	var count int
	if e := f.owner.DB.QueryRow(context.Background(), `SELECT count(*) FROM audit_events WHERE action='domain-fee-waiver'`).Scan(&count); e != nil || count != 1 {
		t.Fatal("waiver audit not atomic", count, e)
	}
}

// Accounts without recruitment access cannot edit supply quotes. Raising their
// own cost must keep the unused quote valid without making the account uneditable.
func TestMultiStoreAdminCostRaiseKeepsSupplyQuoteFloor(t *testing.T) {
	f := hierarchySetup(t)
	id := object(t, request(f.h, "POST", "/api/admin/partners", map[string]any{"email": "plain-agent@example.test"}, f.admin), 200)["id"].(string)
	partner := login(t, f.h, f.mail, "partner", "plain-agent@example.test")
	object(t, request(f.h, "POST", "/api/partner/stores/prices", map[string]any{"storeId": id, "plan": "plus", "priceCents": 16000, "enabled": true, "version": 0}, partner), 200)
	object(t, request(f.h, "POST", "/api/admin/partners/"+id+"/prices/plus", map[string]any{"costCents": 15000, "version": 1}, f.admin), 200)
	var cost, quote int64
	if e := f.owner.DB.QueryRow(context.Background(), `SELECT cost_cents,child_cost_cents FROM partner_prices WHERE partner_id=$1 AND plan='plus'`, id).Scan(&cost, &quote); e != nil || cost != 15000 || quote != 15000 {
		t.Fatal("supply quote below revised cost", cost, quote, e)
	}
}

func TestMultiStoreConcurrentLimitAndCouponReservations(t *testing.T) {
	f := hierarchySetup(t)
	// More simultaneous creates than available slots still produce only five stores.
	var wg sync.WaitGroup
	codes := make(chan int, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes <- request(f.h, "POST", "/api/partner/stores", map[string]any{"name": fmt.Sprint("store", i), "active": true}, f.child).Code
		}(i)
	}
	wg.Wait()
	close(codes)
	successes := 0
	for c := range codes {
		if c == 200 {
			successes++
		} else if c != 409 {
			t.Fatalf("unexpected create status %d", c)
		}
	}
	if successes != 4 || len(storeRows(t, f, f.child)) != 5 {
		t.Fatal("five store limit failed", successes)
	}
	list := storeRows(t, f, f.child)
	targets := []map[string]string{}
	slugs := []string{}
	for _, v := range list {
		s := v.(map[string]any)
		targets = append(targets, map[string]string{"storeId": s["id"].(string), "plan": "plus"})
		slugs = append(slugs, s["slug"].(string))
	}
	in := couponInput("LIMIT", targets, 2000, 2)
	in["oncePerEmail"] = false
	object(t, request(f.h, "POST", "/api/partner/coupons", in, f.child), 200)
	codes = make(chan int, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			buyer := &http.Cookie{Name: "__Host-sf_buyer", Value: security.Token()}
			codes <- request(f.h, "POST", "/api/orders", map[string]any{"key": security.ID(), "plan": "plus", "email": fmt.Sprintf("buyer%d@example.test", i), "partner": slugs[i%len(slugs)], "expectedPrice": 11000, "couponCode": "LIMIT"}, buyer).Code
		}(i)
	}
	wg.Wait()
	close(codes)
	successes = 0
	for c := range codes {
		if c == 201 {
			successes++
		} else if c != 409 {
			t.Fatalf("unexpected coupon status %d", c)
		}
	}
	if successes != 2 {
		t.Fatal("coupon oversold", successes)
	}
}

type domainPaymentFake struct{ paymentFake }

func (p *domainPaymentFake) ID() string { return "gatewayb-alipay" }
func TestDomainBillingPaymentBalanceGraceAndIdempotency(t *testing.T) {
	owner, app := fixture(t, true)
	ctx := context.Background()
	now := time.Now().UnixMilli()
	execSQL(t, owner, `INSERT INTO partners(id,slug,name,email,active) VALUES('agent','ABC123','A','a@example.test',true);INSERT INTO catalog_plans(id,product_id,name,price_cents) VALUES('plus','chatgpt','Plus',10000);INSERT INTO partner_prices(partner_id,plan,price_cents,cost_cents) VALUES('agent','plus',10000,6000);INSERT INTO partner_domains(id,partner_id,store_id,hostname,verification,provider_id,state,checked_at,free) VALUES('free','agent','agent','free.example.com','v','cf-free','active',now(),true),('paid','agent','agent','paid.example.com','v','cf-paid','active',now(),false);UPDATE payment_settings SET enabled_providers=ARRAY['gatewayb-alipay']`)
	fake := &domainPaymentFake{paymentFake: paymentFake{account: "original"}}
	billing := domainbilling.Service{MonthlyFeeCents: 500, Store: app, Payments: payments.Service{Store: app, Provider: fake, Providers: map[string]payments.Provider{fake.ID(): fake}, Checkout: true, NotifyOrigin: "https://example.test"}}
	svc := &domains.Service{Store: app, Provider: &domainProvider{}, Target: "shops.shop.example.invalid", Zone: "shop.example.invalid", Limit: 100}
	if _, e := svc.Resolve(ctx, "paid.example.com"); e == nil {
		t.Fatal("unpaid domain served")
	}
	payment, e := billing.Payment(ctx, "agent", "paid", "create", 0)
	if e != nil {
		t.Fatal(e)
	}
	id := payment["payment"].(map[string]any)["id"].(string)
	if e = billing.Apply(ctx, fake, payments.Result{OrderID: id, Status: "paid", Amount: 1999, TradeNo: "wrong"}); e == nil {
		t.Fatal("wrong amount accepted")
	}
	receipt := payments.Result{OrderID: id, Status: "paid", Amount: 500, TradeNo: "TRADE-DOMAIN"}
	for range 3 {
		if e = billing.Apply(ctx, fake, receipt); e != nil {
			t.Fatal(e)
		}
	}
	var version int64
	var expiry time.Time
	if e = owner.DB.QueryRow(ctx, `SELECT billing_version,expires_at FROM partner_domains WHERE id='paid'`).Scan(&version, &expiry); e != nil || version != 1 {
		t.Fatal("duplicate external payment extended period", e)
	}
	if _, e = billing.Payment(ctx, "agent", "paid", "create", 0); e != nil {
		t.Fatal("paid retry failed", e)
	}
	// Once activated, no new external cashier may block expiry's balance debit.
	if _, e = billing.Payment(ctx, "agent", "paid", "create", 1); e == nil {
		t.Fatal("active domain opened an external renewal cashier")
	}
	var count int
	owner.DB.QueryRow(ctx, `SELECT count(*) FROM domain_charges`).Scan(&count)
	if count != 1 {
		t.Fatal("retry generated another monthly charge")
	}
	// Two days overdue: serve during grace; lack of balance must never create debt.
	execSQL(t, owner, `UPDATE partner_domains SET expires_at=now()-interval '2 days',checked_at=now() WHERE id='paid'`)
	if _, e = svc.Resolve(ctx, "paid.example.com"); e != nil {
		t.Fatal("grace denied", e)
	}
	if e = billing.Reconcile(ctx); e != nil {
		t.Fatal(e)
	}
	owner.DB.QueryRow(ctx, `SELECT count(*) FROM partner_debits`).Scan(&count)
	if count != 0 {
		t.Fatal("insufficient balance was debited")
	}
	execSQL(t, owner, `UPDATE partner_domains SET expires_at=now()-interval '4 days' WHERE id='paid'`)
	if _, e = svc.Resolve(ctx, "paid.example.com"); e == nil {
		t.Fatal("expired domain served after grace")
	}
	if _, e = (catalog.Service{Store: app, Testing: true}).Read(ctx, "ABC123"); e != nil {
		t.Fatal("domain expiry affected main referral", e)
	}
	if _, e = svc.Resolve(ctx, "free.example.com"); e != nil {
		t.Fatal("free domain suspended", e)
	}
	// Balance from completed orders renews the domain exactly once under competing workers.
	execSQL(t, owner, `INSERT INTO orders(id,owner_hash,idempotency_key,plan,plan_name,price_cents,cost_cents,partner_id,mode,payment_status,status,created_at,updated_at,last_event_id) VALUES('earnings','fixture','earnings','plus','Plus',10000,6000,'agent','live','paid','success',$1,$1,'fixture')`, now)
	var wg sync.WaitGroup
	errs := make(chan error, 5)
	for range 5 {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- billing.Reconcile(ctx) }()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	var debit int64
	if e = owner.DB.QueryRow(ctx, `SELECT count(*),COALESCE(sum(amount_cents),0)::bigint FROM partner_debits WHERE partner_id='agent'`).Scan(&count, &debit); e != nil || count != 1 || debit != 500 {
		t.Fatal("auto renewal duplicated", count, debit, e)
	}
	if e = owner.DB.QueryRow(ctx, `SELECT billing_version,expires_at FROM partner_domains WHERE id='paid'`).Scan(&version, &expiry); e != nil || version != 2 || expiry.Before(time.Now().Add(27*24*time.Hour)) {
		t.Fatal("suspended renewal has wrong period", e)
	}
	execSQL(t, owner, `UPDATE partner_domains SET checked_at=now() WHERE id='paid'`)
	if _, e = svc.Resolve(ctx, "paid.example.com"); e != nil {
		t.Fatal("renewed domain unavailable", e)
	}
}

func TestDomainRenewalCompetesWithWithdrawalWithoutDoubleSpending(t *testing.T) {
	owner, app, _, h, mail := identityFixture(t)
	admin := login(t, h, mail, "admin", "")
	execSQL(t, owner, `INSERT INTO catalog_plans(id,product_id,name,price_cents,default_cost_cents) VALUES('plus','chatgpt','Plus',10000,6000)`)
	id := object(t, request(h, "POST", "/api/admin/partners", map[string]any{"email": "billing-race@example.test"}, admin), 200)["id"].(string)
	partner := login(t, h, mail, "partner", "billing-race@example.test")
	object(t, request(h, "POST", "/api/partner/payout-account", map[string]any{"phone": "13800138000", "name": "测试收款人", "version": 0}, partner), 200)
	execSQL(t, owner, `INSERT INTO orders(id,owner_hash,idempotency_key,plan,plan_name,price_cents,cost_cents,partner_id,mode,payment_status,status,created_at,updated_at,last_event_id) VALUES('race','fixture','race','plus','Plus',10000,6000,$1,'live','paid','success',$2,$2,'fixture');`, id, time.Now().UnixMilli())
	execSQL(t, owner, `INSERT INTO partner_domains(id,partner_id,hostname,verification,free,expires_at) VALUES('paid',$1,'race.example.com','v',false,now()-interval '1 day')`, id)
	service := domainbilling.Service{MonthlyFeeCents: 500, Store: app}
	ctx := context.Background()
	var wg sync.WaitGroup
	var renewalErr error
	var status int
	wg.Add(2)
	go func() { defer wg.Done(); renewalErr = service.Reconcile(ctx) }()
	go func() {
		defer wg.Done()
		status = request(h, "POST", "/api/partner/payouts", map[string]any{"key": security.ID(), "accountVersion": 1}, partner).Code
	}()
	wg.Wait()
	if renewalErr != nil || status != 201 {
		t.Fatal("concurrent operations failed", renewalErr, status)
	}
	var payout, charges int64
	if e := owner.DB.QueryRow(ctx, `SELECT (SELECT COALESCE(sum(amount_cents),0)::bigint FROM partner_payouts WHERE partner_id=$1),(SELECT COALESCE(sum(amount_cents),0)::bigint FROM domain_charges WHERE partner_id=$1 AND source='balance')`, id).Scan(&payout, &charges); e != nil || payout+charges != 4000 {
		t.Fatal("double spending", payout, charges, e)
	}
	if e := service.Reconcile(ctx); e != nil {
		t.Fatal(e)
	}
	var after int64
	owner.DB.QueryRow(ctx, `SELECT COALESCE(sum(amount_cents),0)::bigint FROM domain_charges`).Scan(&after)
	if after != charges {
		t.Fatal("reserved payout funds used for renewal")
	}
	// Domain debit lines remain visible in the payout detail instead of disappearing in a refund-only join.
	var payoutID string
	owner.DB.QueryRow(ctx, `SELECT id FROM partner_payouts WHERE partner_id=$1`, id).Scan(&payoutID)
	detail := object(t, request(h, "GET", "/api/admin/partner-payouts/"+payoutID, nil, admin), 200)
	if charges > 0 && len(detail["deductions"].([]any)) != 1 {
		t.Fatal("domain fee missing from payout audit")
	}
}
