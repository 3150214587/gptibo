package commerce

import (
	"math"
	"regexp"
)

// X charge identity is explicitly configured, then frozen in every order snapshot.
func ValidateXProduct(code string, cfg map[string]any) error {
	months, _ := cfg["months"].(float64)
	amount, _ := cfg["amount_minor"].(float64)
	currency, _ := cfg["currency"].(string)
	product, _ := cfg["stripe_product"].(string)
	merchant, _ := cfg["stripe_merchant"].(string)
	want := map[string]float64{"x-3m": 3, "x-6m": 6}[code]
	if want == 0 || months != want || amount <= 0 || amount > 100000000 || math.IsNaN(amount) || math.IsInf(amount, 0) || math.Trunc(amount) != amount || !regexp.MustCompile(`^[a-z]{3}$`).MatchString(currency) || !regexp.MustCompile(`^prod_[A-Za-z0-9]{1,100}$`).MatchString(product) || !regexp.MustCompile(`^acct_[A-Za-z0-9]{1,100}$`).MatchString(merchant) {
		return Fail("x_product_missing", "请核对 X 商品、收款方、币种和最小单位金额后再开放", 409)
	}
	return nil
}
