package providers

import (
	"context"
	"gptibo/platform/internal/commerce"
	"math"
	"strings"
)

func xQuote(ctx context.Context, s commerce.Snapshot, account Object) error {
	data, err := xQuery(ctx, s, account, "useSubscriptionProductDetailsByRestIdQuery", "Se1Bp6zcNnuXYXRecV2qLA", Object{"stripeId": str(s.ProductConfig["stripe_product"])})
	if err != nil {
		return err
	}
	return guardXQuote(data, s.ProductConfig)
}
func guardXQuote(data, cfg Object) error {
	product := obj(data["web_subscription_product_details_by_rest_id"])
	prices, ok := product["prices"].([]any)
	if str(product["rest_id"]) != str(cfg["stripe_product"]) || !ok || len(prices) != 1 {
		return &UpstreamError{"x_quote_unconfirmed", true}
	}
	price := obj(prices[0])
	micro, ok := price["amount_local_micro"].(float64)
	if !ok || math.IsNaN(micro) || math.IsInf(micro, 0) || micro <= 0 || math.Trunc(micro) != micro || micro > 9007199254740991 || str(price["price_type"]) != "OneTime" || strings.ToLower(str(price["currency_code"])) != str(cfg["currency"]) || micro != number(cfg["amount_minor"])*10000 {
		return &UpstreamError{"x_price_changed", true}
	}
	return nil
}
