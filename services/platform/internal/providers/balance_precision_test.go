package providers

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strings"
	"testing"
)

func TestBalancePrecisionDoesNotRelaxChargeAmounts(t *testing.T) {
	for _, tc := range []struct {
		value any
		minor int64
		valid bool
	}{
		{49.26028000000001, 4926, true}, {"49.26028000000001", 4926, true},
		{6.999999, 699, true}, {7.000001, 700, true}, {20.49999, 2049, true},
		{nil, 0, false}, {"", 0, false}, {-0.001, 0, false}, {math.NaN(), 0, false}, {math.Inf(1), 0, false},
	} {
		got, ok := usdBalanceMinor(tc.value)
		if got != tc.minor || ok != tc.valid {
			t.Fatalf("balance %v => %d/%v", tc.value, got, ok)
		}
	}
	if _, ok := usdMinor(49.26028); ok {
		t.Fatal("fractional-cent charge amount allowed")
	}
}

func TestProductionFractionalWalletCanOpenCard(t *testing.T) {
	_, _, s := orderFixture(t)
	s.Rule.FundingMinor = 2000
	s.Rule.MaxOpenFeeMinor = 50
	s.Provider.Billing.FirstName = "Test"
	s.Provider.Billing.LastName = "Owner"
	old := client
	defer func() { client = old }()
	wallet := `{"code":0,"data":{"account_reserve_amount":20,"balance":69.26028000000001,"currency":"USD","spendable_balance":49.26028000000001}}`
	client = &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" {
			t.Fatal("precheck charged funds")
		}
		if strings.HasSuffix(r.URL.Path, "/products") {
			return response(`{"code":0,"data":[{"product_code":"TEST-BIN","min_amount":20,"open_fee":0.5,"recharge_fee":0}]}`), nil
		}
		return response(wallet), nil
	})}
	fee, err := cardFundingPrecheck(context.Background(), s, 0)
	if err != nil || fee != 50 {
		t.Fatalf("real balance shape rejected: %v", err)
	}
	wallet = `{"code":0,"data":{"currency":"USD","balance":69.26028,"spendable_balance":20.49999}}`
	_, err = cardFundingPrecheck(context.Background(), s, 0)
	var upstream *UpstreamError
	if !errors.As(err, &upstream) || upstream.Code != "card_wallet_insufficient" {
		t.Fatal("rounded up unavailable funds", err)
	}
	wallet = `{"code":0,"data":{"currency":"USD","balance":69.26028}}`
	_, err = cardFundingPrecheck(context.Background(), s, 0)
	if !errors.As(err, &upstream) || upstream.Definitive || upstream.Code != "card_wallet_unconfirmed" {
		t.Fatal("missing balance treated as definitive failure", err)
	}
}
