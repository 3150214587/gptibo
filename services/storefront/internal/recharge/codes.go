package recharge

import "gptibo/storefront/internal/rechargecopy"

var failureMessages = rechargecopy.FailureMessages
var categoryMessages = rechargecopy.CategoryMessages

var resultStages = map[string]string{
	"queued":           "queued",
	"awaiting_card":    "awaiting_card",
	"funding_pending":  "preparing_funds",
	"dispatching":      "dispatching",
	"running":          "processing",
	"requires_action":  "requires_action",
	"pending":          "confirming",
	"plus_paid":        "upgrading",
	"review":           "review",
	"completed":        "completed",
	"declined":         "failed",
	"failed_precharge": "failed",
	"cancelled":        "cancelled",
}
