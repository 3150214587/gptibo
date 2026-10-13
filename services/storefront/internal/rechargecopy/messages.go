// Public API messages are supplementary display text, never state or retry authority.
package rechargecopy

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Only the documented, sanitized redemption message enters this boundary.
// Reject obvious credentials, raw responses, addresses and markup as a defense in depth.
var privateMessage = regexp.MustCompile(`(?i)(https?://|www\.|@|[<>{}]|(?:access[_ -]?token|refresh[_ -]?token|session|api[_ -]?key|password|secret)\s*[:=]|bearer\s|eyJ[A-Za-z0-9_-]{12}|[A-Za-z0-9_/-]{40,}|[0-9]{12,}|\b(?:[0-9]{1,3}\.){3}[0-9]{1,3}\b)`)

// A provider address can appear without a URL scheme, including in old stored
// messages. Reject domain-shaped text and DNS dot variants at both boundaries.
var privateDomain = regexp.MustCompile(`(?i)[a-z0-9-]+(?:[.。．｡][a-z0-9-]+)*[.。．｡][a-z]{2,63}`)

func PublicMessage(message string) string {
	message = strings.TrimSpace(message)
	if !utf8.ValidString(message) || utf8.RuneCountInString(message) > 500 || strings.ContainsFunc(message, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) || privateMessage.MatchString(message) || privateDomain.MatchString(message) {
		return ""
	}
	return message
}
func DisplayMessage(message, failure, category string) string {
	if safe := PublicMessage(message); safe != "" {
		return safe
	}
	return FailureMessage(failure, category)
}

var FailureMessages = map[string]string{
	"session_invalid":          "登录凭据已失效，请重新登录后提交。",
	"mailbox_login_failed":     "邮箱登录失败，请核对登录资料，或在允许重新提交后改用 Session。",
	"account_has_subscription": "账号已有订阅，请核对账号或联系客服。",
	"subscription_required":    "该套餐要求账号已有有效订阅，请核对续费资格。",
	"precheck_rejected":        "账号未通过预检，请完成账号验证或联系客服。",
	"account_unavailable":      "当前账号暂不可用，请核对账号或联系客服。",
	"product_unavailable":      "该套餐暂不可用，请联系客服。",
	"order_rejected":           "此次充值未被受理，请联系客服核对。",
	"region_unsupported":       "账号地区暂不支持，请联系客服。",
	"payment_blocked":          "充值付款受限，请联系客服核查，勿重复提交相同资料。",
	"payment_declined":         "充值付款未通过，请查看当前可用的处理方式或联系客服。",
	"precharge_failed":         "充值前校验未通过，请核对账号。",
	"agent_cancelled":          "此次充值已取消，请查看原订单的可用处理方式。",
	"cancelled":                "此次充值已取消。",
	"verification_timeout":     "充值结果尚未确认，请联系客服核对原任务，勿重复提交。",
	"service_unavailable":      "充值服务暂不可用，平台正在重试，请继续查询原任务。",
	"other":                    "充值未完成，请联系客服。",
	"request_rejected":         "提交未被受理，请核对资料后重新提交。",
}
var CategoryMessages = map[string]string{
	"credential":   "登录资料未通过验证，请核对资料或联系客服。",
	"account":      "账号状态需要核对，请联系客服。",
	"product":      "套餐暂不可用，请联系客服。",
	"resource":     "充值资源暂不可用，请继续查询原任务或联系客服。",
	"service":      "充值服务暂不可用，请继续查询原任务。",
	"confirmation": "充值结果正在核对，请勿重复提交。",
	"unknown":      "充值结果需要进一步核对，请联系客服。",
}

// FailureMessage prefers a specific reason, then a safe category, then a fallback.
func FailureMessage(failure, category string) string {
	if message, ok := FailureMessages[failure]; ok && failure != "other" {
		return message
	}
	if message, ok := CategoryMessages[category]; ok {
		return message
	}
	return FailureMessages["other"]
}
