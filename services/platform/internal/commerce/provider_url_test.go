package commerce

import "testing"

func TestCardCredentialsOnlyGoToExplicitHTTPSHost(t *testing.T) {
	t.Setenv("CARD_API_ALLOWED_HOSTS", "approved.example.com")
	for _, base := range []string{"http://approved.example.com/openapi/v1", "https://approved.example.com.evil.test/openapi/v1", "https://127.0.0.1/openapi/v1", "https://user:password@approved.example.com/openapi/v1", "https://approved.example.com:8443/openapi/v1", "https://approved.example.com/openapi/v1?token=x", "https://approved.example.com/openapi/v1#x", "https://approved.example.com/other", ""} {
		if ValidateCardBase(base) == nil {
			t.Fatalf("unsafe endpoint accepted: %s", base)
		}
	}
	if err := ValidateCardBase("https://approved.example.com/openapi/v1"); err != nil {
		t.Fatal(err)
	}
}

func TestXProductCannotEnableWithoutExplicitPaymentIdentity(t *testing.T) {
	cfg := map[string]any{"months": float64(3), "amount_minor": float64(123), "currency": "usd", "stripe_product": "prod_fixture", "stripe_merchant": "acct_fixture"}
	if err := ValidateXProduct("x-3m", cfg); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"months", "amount_minor", "currency", "stripe_product", "stripe_merchant"} {
		old := cfg[field]
		delete(cfg, field)
		if ValidateXProduct("x-3m", cfg) == nil {
			t.Fatal("missing guard accepted", field)
		}
		cfg[field] = old
	}
	if ValidateXProduct("x-6m", cfg) == nil {
		t.Fatal("wrong duration accepted")
	}
}
