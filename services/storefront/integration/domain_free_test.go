package integration_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"gptibo/storefront/internal/domainbilling"
	"gptibo/storefront/internal/httpapi"
	"gptibo/storefront/internal/payments"
	"gptibo/storefront/internal/security"
	"gptibo/storefront/internal/store"
)

func TestDomainFreeActivationRenewalAndReturnToCharging(t *testing.T) {
	var app *store.Store
	f := hierarchySetup(t, func(o *httpapi.Options) {
		enableTestDomains(o)
		o.Domains.MonthlyFeeCents = 0
		app = o.Auth.Store
	})
	ctx := context.Background()
	view := object(t, request(f.h, "GET", "/api/partner/domain", nil, f.child), 200)
	if view["monthlyFeeCents"] != float64(0) {
		t.Fatal("free policy not exposed", view)
	}
	execSQL(t, f.owner, `INSERT INTO partner_domains(id,partner_id,hostname,verification,free) VALUES('free-period',$1,'free-period.example.com','v',false)`, f.childID)
	path := "/api/partner/domains/free-period/payment"
	body := map[string]any{"action": "balance", "version": 0}
	object(t, request(f.h, "POST", path, body, nil), 401)
	object(t, request(f.h, "POST", path, body, f.parent), 404)
	// Zero balance and no configured payment provider must still allow activation.
	var wg sync.WaitGroup
	responses := make(chan *httptest.ResponseRecorder, 6)
	for i := range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			action := "balance"
			if i%2 == 0 {
				action = "create"
			}
			responses <- request(f.h, "POST", path, map[string]any{"action": action, "version": 0}, f.child)
		}()
	}
	wg.Wait()
	close(responses)
	var receiptID any
	for r := range responses {
		p := object(t, r, 200)["payment"].(map[string]any)
		if p["amount_cents"] != float64(0) || p["source"] != "promotion" || p["state"] != "paid" || p["qr"] != nil {
			t.Fatal(p)
		}
		if receiptID != nil && p["id"] != receiptID {
			t.Fatal("duplicate free activation")
		}
		receiptID = p["id"]
	}
	object(t, request(f.h, "POST", path, map[string]any{"action": "balance", "version": 1}, f.child), 409)
	// Unverifiable or frozen supplier earnings must not block a free renewal.
	f.seed(t, "free-funds", "plus", &http.Cookie{Value: security.Token()})
	execSQL(t, f.owner, `INSERT INTO recharge_orders(order_id,account,product_code,collection_mode,create_body,updated_at,merchant_margin_cents,financial_hold) VALUES('free-funds','fixture','fixture','platform_collect','{}',1,3000,1); UPDATE partner_domains SET expires_at=now()-interval '4 days' WHERE id='free-period'`)
	billing := domainbilling.Service{Store: app, VerifySupplier: func(context.Context, string) error { return errors.New("supplier unavailable") }}
	errs := make(chan error, 5)
	for range 5 {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- billing.Reconcile(ctx) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var valid bool
	if err := f.owner.DB.QueryRow(ctx, `SELECT (SELECT count(*)=2 AND bool_and(amount_cents=0 AND source='promotion') FROM domain_charges) AND NOT EXISTS(SELECT 1 FROM partner_debits) AND (SELECT billing_version=2 AND NOT free AND expires_at>now()+interval '27 days' FROM partner_domains WHERE id='free-period')`).Scan(&valid); err != nil || !valid {
		t.Fatal("free renewal changed money or period", err)
	}
	// Restoring the tariff leaves the free period intact, then charges once at expiry.
	billing.MonthlyFeeCents = 500
	if _, err := billing.Payment(ctx, f.childID, "free-period", "balance", 0); err != nil {
		t.Fatal("old promotion receipt retry", err)
	}
	if err := billing.Reconcile(ctx); err != nil {
		t.Fatal("not-yet-due period charged", err)
	}
	execSQL(t, f.owner, `UPDATE recharge_orders SET financial_hold=0 WHERE order_id='free-funds'; UPDATE partner_domains SET expires_at=now()-interval '1 day' WHERE id='free-period'`)
	billing.VerifySupplier = func(context.Context, string) error { return nil }
	if err := billing.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.owner.DB.QueryRow(ctx, `SELECT (SELECT count(*)=1 AND sum(amount_cents)=500 FROM partner_debits) AND (SELECT billing_version=3 AND NOT free FROM partner_domains WHERE id='free-period')`).Scan(&valid); err != nil || !valid {
		t.Fatal("restored tariff not applied exactly once", err)
	}
}

func TestDomainFreePolicyPreservesPendingPaidReceipts(t *testing.T) {
	owner, app := fixture(t, true)
	ctx := context.Background()
	execSQL(t, owner, `INSERT INTO partners(id,slug,name,email,active) VALUES('agent','FREE99','Agent','free@example.test',true); INSERT INTO partner_domains(id,partner_id,hostname,verification,free) VALUES('old','agent','old.example.com','v',false),('disabled','agent','disabled.example.com','v',false); INSERT INTO domain_charges(id,domain_id,partner_id,hostname,period,amount_cents,source,provider,account,qr) VALUES('old','old','agent','old.example.com',0,500,'gatewayb','gatewayb-alipay','original','old-qr')`)
	fake := &feePaymentFake{domainPaymentFake: domainPaymentFake{paymentFake: paymentFake{account: "original"}}, created: map[string]int64{}, queried: map[string]int64{}}
	billing := domainbilling.Service{Store: app, Payments: payments.Service{Store: app, Providers: map[string]payments.Provider{fake.ID(): fake}}}
	if _, err := billing.Payment(ctx, "agent", "old", "balance", 0); err == nil {
		t.Fatal("pending receipt overwritten by promotion")
	}
	if err := billing.Apply(ctx, fake, payments.Result{OrderID: "old", Amount: 0, Status: "paid", TradeNo: "wrong"}); err == nil {
		t.Fatal("zero callback accepted")
	}
	if err := billing.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if fake.queried["old"] != 500 || len(fake.created) != 0 {
		t.Fatal("historical amount lost or cashier created")
	}
	billing.RenewEnabled = func() bool { return false }
	if _, err := billing.Payment(ctx, "agent", "disabled", "create", 0); err == nil {
		t.Fatal("disabled service granted free period")
	}
}
