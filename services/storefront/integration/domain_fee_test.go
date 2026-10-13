package integration_test

import (
	"context"
	"gptibo/storefront/internal/domainbilling"
	"gptibo/storefront/internal/payments"
	"testing"
)

type feePaymentFake struct {
	domainPaymentFake
	created, queried map[string]int64
}

func (p *feePaymentFake) Create(_ context.Context, o payments.Order, _ string) (string, error) {
	p.created[o.ID] = o.Amount
	return "https://qr.example.test/fee", nil
}
func (p *feePaymentFake) Query(_ context.Context, o payments.Order) (payments.Result, error) {
	p.queried[o.ID] = o.Amount
	return payments.Result{OrderID: o.ID, Amount: o.Amount, Status: "paid", TradeNo: "trade-" + o.ID}, nil
}

func TestDomainFeeUsesFiveYuanAndPreservesOldReceipts(t *testing.T) {
	owner, app := fixture(t, true)
	ctx := context.Background()
	execSQL(t, owner, `INSERT INTO partners(id,slug,name,email,active) VALUES('agent','FEE123','Agent','fee@example.test',true); UPDATE payment_settings SET enabled_providers=ARRAY['gatewayb-alipay']; INSERT INTO partner_domains(id,partner_id,hostname,verification,free) VALUES('new','agent','new.example.com','v',false),('legacy-ui','agent','old.example.com','v',false),('legacy-worker','agent','worker.example.com','v',false); INSERT INTO domain_charges(id,domain_id,partner_id,hostname,period,source,provider,account,amount_cents) VALUES('legacy-ui','legacy-ui','agent','old.example.com',0,'gatewayb','gatewayb-alipay','original',2000),('legacy-worker','legacy-worker','agent','worker.example.com',0,'gatewayb','gatewayb-alipay','original',2000)`)
	fake := &feePaymentFake{domainPaymentFake: domainPaymentFake{paymentFake: paymentFake{account: "original"}}, created: map[string]int64{}, queried: map[string]int64{}}
	billing := domainbilling.Service{MonthlyFeeCents: 500, Store: app, Payments: payments.Service{Store: app, Provider: fake, Providers: map[string]payments.Provider{fake.ID(): fake}, Checkout: true, NotifyOrigin: "https://example.test"}}
	fresh, e := billing.Payment(ctx, "agent", "new", "create", 0)
	if e != nil {
		t.Fatal(e)
	}
	receipt := fresh["payment"].(map[string]any)
	id := receipt["id"].(string)
	if receipt["amount_cents"] != int64(500) || fake.created[id] != 500 {
		t.Fatal("new cashier price not five yuan", receipt)
	}
	if e = billing.Apply(ctx, fake, payments.Result{OrderID: id, Amount: 2000, Status: "paid", TradeNo: "wrong-new"}); e == nil {
		t.Fatal("new receipt accepted old fee")
	}
	old, e := billing.Payment(ctx, "agent", "legacy-ui", "create", 0)
	if e != nil {
		t.Fatal(e)
	}
	if old["payment"].(map[string]any)["amount_cents"] != int64(2000) || fake.created["legacy-ui"] != 2000 {
		t.Fatal("legacy cashier amount rewritten")
	}
	if e = billing.Apply(ctx, fake, payments.Result{OrderID: "legacy-ui", Amount: 500, Status: "paid", TradeNo: "wrong-old"}); e == nil {
		t.Fatal("old receipt accepted underpayment")
	}
	if _, e = billing.Payment(ctx, "agent", "legacy-ui", "query"); e != nil || fake.queried["legacy-ui"] != 2000 {
		t.Fatal("legacy query rejected", e)
	}
	if e = billing.Reconcile(ctx); e != nil {
		t.Fatal(e)
	}
	if fake.queried[id] != 500 || fake.queried["legacy-worker"] != 2000 {
		t.Fatal("worker ignored receipt prices", fake.queried)
	}
	if e = billing.Apply(ctx, fake, payments.Result{OrderID: "legacy-ui", Amount: 2000, Status: "paid", TradeNo: "trade-legacy-ui"}); e != nil {
		t.Fatal("old receipt replay rejected", e)
	}
	var valid bool
	if e = owner.DB.QueryRow(ctx, `SELECT bool_and(billing_version=1 AND expires_at>now()+interval '27 days') FROM partner_domains`).Scan(&valid); e != nil || !valid {
		t.Fatal("duplicate or missing activation", e)
	}
}
