package providers

import (
	"context"
	"gptibo/platform/internal/commerce"
	"regexp"
	"strings"
)

var planCode = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,79}$`)

func normalizeGPTPlans(data Object) ([]commerce.UpstreamPlan, error) {
	registry, registryOK := data["registry"].([]any)
	configured, configuredOK := data["plans"].(map[string]any)
	if !registryOK || !configuredOK {
		return nil, &UpstreamError{"invalid_plan_catalog", false}
	}
	rows := []commerce.UpstreamPlan{}
	seen := map[string]bool{}
	for _, item := range registry {
		row := obj(item)
		if str(row["product"]) != "gpt" || truth(row["is_credit"]) || str(row["flow"]) != "" && str(row["flow"]) != "direct" {
			continue
		}
		code := str(row["key"])
		if !planCode.MatchString(code) || seen[code] {
			return nil, &UpstreamError{"invalid_plan_catalog", false}
		}
		seen[code] = true
		configKey := str(row["acc_plan_key"])
		if configKey == "" {
			configKey = code
		}
		plan := obj(configured[configKey])
		name := code
		for _, value := range []any{row["name"], row["label"], plan["name"]} {
			if label := strings.TrimSpace(str(value)); label != "" && len(label) <= 200 {
				name = label
				break
			}
		}
		rows = append(rows, commerce.UpstreamPlan{Code: code, Name: name, Enabled: truth(plan["enabled"]), Purchasable: truth(row["purchasable"])})
	}
	return rows, nil
}

func readGPTPlans(ctx context.Context, provider commerce.ProviderConfig) (Object, []commerce.UpstreamPlan, error) {
	response, err := cardAPI(ctx, provider, "GET", "/gpt-direct/plans?product=gpt", nil, "")
	if err != nil {
		return nil, nil, err
	}
	data := obj(response["data"])
	rows, err := normalizeGPTPlans(data)
	return data, rows, err
}

// GPTPlans reads the live catalog without creating cards or recharge orders.
func (e *Engine) GPTPlans(ctx context.Context) ([]commerce.UpstreamPlan, error) {
	provider, err := (&commerce.Service{Store: e.Store}).Provider(ctx)
	if err != nil {
		return nil, err
	}
	_, rows, err := readGPTPlans(ctx, provider)
	if err != nil {
		return nil, commerce.Fail("plan_query_failed", "上游套餐读取失败，请检查卡台连接后重试", 502)
	}
	return rows, nil
}
