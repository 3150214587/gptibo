package rechargecopy

import (
	"strings"
	"testing"
)

func TestPublicMessageBoundary(t *testing.T) {
	reason := "账号尚未完成验证，请完成账号验证后再提交。"
	if PublicMessage("请重新获取 Session 后再提交。") == "" {
		t.Fatal("safe credential guidance discarded")
	}
	if DisplayMessage(reason, "precheck_rejected", "account") != reason {
		t.Fatal("specific public reason discarded")
	}
	for _, raw := range []string{"https://private.example/error", "user@example.com", "<script>private</script>", `{"accessToken":"private"}`, "Bearer private-token", "secret=private", strings.Repeat("字", 501), "raw\nresponse", "card 1234567890123456"} {
		if PublicMessage(raw) != "" || DisplayMessage(raw, "precheck_rejected", "account") != FailureMessages["precheck_rejected"] {
			t.Fatal("unsafe message accepted", raw)
		}
	}
	if DisplayMessage("", "new_code", "confirmation") != CategoryMessages["confirmation"] {
		t.Fatal("category fallback missing")
	}
}

func TestPublicMessageRejectsSupplierDomains(t *testing.T) {
	for _, raw := range []string{
		"请前往 recharge.example.invalid 处理。",
		"请前往 //recharge.example.invalid/pay 处理。",
		"请联系 RECHARGE.INK 客服。",
		"请前往 checkout.recharge.example.invalid:443/pay 处理。",
		"充值失败（recharge.example.invalid），请稍后再试。",
		"请前往 other-supplier.example/path 处理。",
		"请前往 recharge。ink 处理。",
		"请前往 recharge．ink 处理。",
		"请前往 recharge｡ink 处理。",
		"请前往 up\u200bstream.example.invalid 处理。",
	} {
		t.Run(raw, func(t *testing.T) {
			if PublicMessage(raw) != "" {
				t.Fatal("supplier domain accepted")
			}
			if got := DisplayMessage(raw, "precheck_rejected", "account"); got != FailureMessages["precheck_rejected"] {
				t.Fatalf("unsafe reason did not fall back: %q", got)
			}
			if got := DisplayMessage(raw, "new_code", "service"); got != CategoryMessages["service"] {
				t.Fatalf("unsafe reason did not use safe category: %q", got)
			}
		})
	}
	for _, safe := range []string{
		"请重新获取 Session 后再提交。",
		"账号尚未完成验证，请完成账号验证后再提交。",
		"GPT-5.2 暂时不可用，请稍后再试。",
		"版本 1.26.0 暂不支持。",
		"Please retry. Contact support if the problem persists.",
	} {
		if got := PublicMessage(safe); got != safe {
			t.Fatalf("safe guidance changed: %q", got)
		}
	}
}
