package providers

import (
	"context"
	"gptibo/platform/internal/commerce"
)

// Inventory returns only fields explicitly needed by administrators.
func (e *Engine) Inventory(ctx context.Context, kind string) (any, error) {
	p, err := (&commerce.Service{Store: e.Store}).Provider(ctx)
	if err != nil {
		return nil, err
	}
	path := ""
	fields := []string{}
	switch kind {
	case "products":
		path = "/products"
		fields = []string{"product_code", "product_name", "network", "issuing_area", "min_amount", "max_amount", "open_fee", "recharge_fee"}
	case "cards":
		path = "/cards?page=1&page_size=100&sync=0"
		fields = []string{"id", "product_code", "product_name", "status", "available_amount"}
	case "balance":
		path = "/balance"
		fields = []string{"balance", "spendable_balance", "currency"}
	default:
		return nil, commerce.Fail("invalid_resource", "资源类型无效", 400)
	}
	resp, err := cardAPI(ctx, p, "GET", path, nil, "")
	if err != nil {
		return nil, commerce.Fail("card_query_failed", "卡台查询未确认，请稍后重试", 502)
	}
	safe := func(row Object) Object {
		out := Object{}
		if kind == "cards" {
			if pan := str(row["card_number"]); len(pan) >= 4 {
				out["last_four"] = pan[len(pan)-4:]
			}
		}
		for _, f := range fields {
			switch v := row[f].(type) {
			case string:
				out[f] = v
			case float64:
				out[f] = v
			case bool:
				out[f] = v
			}
		}
		return out
	}
	if kind == "balance" {
		return safe(obj(resp["data"])), nil
	}
	var rows []any
	if kind == "cards" {
		rows, _ = obj(resp["data"])["list"].([]any)
	} else {
		rows, _ = resp["data"].([]any)
	}
	out := []Object{}
	for _, row := range rows {
		out = append(out, safe(obj(row)))
	}
	return out, nil
}
