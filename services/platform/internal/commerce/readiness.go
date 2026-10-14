package commerce

import "regexp"

// Local execution prerequisites do not establish an upstream business result.
func ExecutionReady(s Snapshot) error {
	if !s.Provider.WritesEnabled {
		return Fail("execution_paused", "自动充值已暂停", 409)
	}
	if s.Provider.Environment != "production" && s.Provider.Environment != "sandbox" || s.Provider.APIKey == "" {
		return Fail("provider_missing", "卡台连接未配置", 409)
	}
	if err := ValidateCardBase(s.Provider.APIBase); err != nil {
		return Fail("provider_missing", "卡台连接未配置", 409)
	}
	if !s.Rule.Enabled {
		return Fail("card_rule_disabled", "商品卡规则未启用", 409)
	}
	if s.Rule.CardID <= 0 && !s.Rule.AutoOpen {
		return Fail("card_missing", "请指定卡片或允许自动开卡", 409)
	}
	if s.Rule.AutoOpen && s.Rule.CardID == 0 && (s.Provider.Billing.FirstName == "" || s.Provider.Billing.LastName == "") {
		return Fail("billing_profile_missing", "自动开卡持卡人资料未配置", 409)
	}
	if s.Business == "gpt" {
		if s.Plan == "" {
			return Fail("plan_missing", "上游套餐未配置", 409)
		}
		if s.Provider.PaymentCountry == "" || s.Provider.PaymentCurrency == "" {
			return Fail("payment_region_missing", "GPT 支付地区未配置", 409)
		}
	}
	if s.Business == "x" {
		if err := ValidateXProduct(s.ProductCode, s.ProductConfig); err != nil {
			return err
		}
		b := s.Provider.Billing
		if s.Provider.Environment != "production" || !regexp.MustCompile(`^pk_live_[A-Za-z0-9]+$`).MatchString(s.Provider.StripeKey) || s.Provider.XBearer == "" {
			return Fail("x_configuration_missing", "X 正式支付连接未配置", 409)
		}
		if b.FirstName == "" || b.LastName == "" || b.Email == "" || b.Country == "" || b.Line1 == "" || b.City == "" || b.Postal == "" {
			return Fail("billing_profile_missing", "X 账单资料未配置", 409)
		}
	}
	return nil
}
