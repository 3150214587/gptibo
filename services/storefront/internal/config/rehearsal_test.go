package config

import (
	"testing"
)

func TestRehearsalDisablesEveryLiveIntegration(t *testing.T) {
	t.Setenv("MIGRATION_REHEARSAL", "true")
	for _, name := range []string{"ALIPAY_ENABLED", "GATEWAYA_ENABLED", "GATEWAYB_ENABLED", "PAYMENT_CHECKOUT_ENABLED", "RECHARGE_ENABLED", "DELIVERY_SUPPLIER_ENABLED", "ACCOUNT_SUPPLIER_ENABLED", "EMAIL_PRODUCTS_ENABLED", "XGIFT_ENABLED", "XGIFT_PLATFORM_ENABLED", "TELEGRAM_ENABLED", "FEISHU_PAYOUT_ENABLED", "PAYMENT_TEST_ENABLED", "ORDER_TESTING_ENABLED", "MAIL_ENABLED"} {
		t.Setenv(name, "true")
	}
	p, e := LoadPayments()
	if e != nil || p.Provider != nil || p.Checkout {
		t.Fatal("payments not isolated", e)
	}
	tb, e := LoadRecharge()
	if e != nil || tb != nil {
		t.Fatal("Recharge not isolated", e)
	}
	pc, e := LoadProcurement()
	if e != nil || pc.DeliverySupplier != nil || pc.AccountSupplier != nil || pc.EmailEnabled {
		t.Fatal("procurement not isolated", e)
	}
	xg, e := LoadXGift()
	if e != nil || len(xg.Clients) > 0 || xg.Checkout {
		t.Fatal("X not isolated", e)
	}
	tg, e := LoadTelegram()
	if e != nil || tg != nil {
		t.Fatal("telegram not isolated", e)
	}
	f, e := LoadNotifications("https://staging.example.test")
	if e != nil || f != nil {
		t.Fatal("notifications not isolated", e)
	}
	h, e := LoadHTTP()
	if e != nil || h.Auth.Sender != nil || h.PaymentTesting || h.OrderTesting || !h.Rehearsal {
		t.Fatal("HTTP not isolated", e)
	}
}
