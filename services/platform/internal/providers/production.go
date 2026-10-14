package providers

import (
	"context"
	"encoding/json"
	"gptibo/platform/internal/commerce"
	"strings"
)

func safeStage(stage string) string {
	switch stage {
	case "queued", "preflight", "precheck", "processing", "running", "submitting", "submitted", "paying", "paid", "confirming", "completed", "succeeded", "failed", "declined", "cancelled", "review", "requires_action", "done":
		return stage
	}
	return "processing"
}
func maskEmail(email string) string {
	if email == "" {
		return ""
	}
	parts := strings.SplitN(email, "@", 2)
	if len(parts) != 2 {
		return ""
	}
	r := []rune(parts[0])
	if len(r) == 0 {
		return ""
	}
	return string(r[0]) + "***@" + parts[1]
}
func (e *Engine) dispatchAllowed(ctx context.Context, id, token string) error {
	var status, cipher string
	err := e.Store.DB.QueryRow(ctx, "SELECT u.status,s.payload_cipher FROM orders o JOIN users u ON u.id=o.user_id JOIN settings s ON s.id='provider' WHERE o.id=$1 AND o.lease_token=$2 AND o.lease_until>now()", id, token).Scan(&status, &cipher)
	if err != nil {
		return &UpstreamError{"lease_lost", false}
	}
	if status != "active" {
		return &UpstreamError{"channel_disabled", false}
	}
	data, err := e.Store.Vault.Open("setting:provider", cipher)
	if err != nil {
		return err
	}
	var p commerce.ProviderConfig
	if json.Unmarshal(data, &p) != nil || !p.WritesEnabled {
		return &UpstreamError{"execution_paused", false}
	}
	return nil
}
func cardFundingPrecheck(ctx context.Context, s commerce.Snapshot, id int64) (int64, error) {
	catalog, err := cardAPI(ctx, s.Provider, "GET", "/products", nil, "")
	if err != nil {
		return 0, err
	}
	rows, ok := catalog["data"].([]any)
	if !ok {
		return 0, &UpstreamError{"card_product_unconfirmed", true}
	}
	var product Object
	for _, v := range rows {
		row := obj(v)
		if str(row["product_code"]) == s.Rule.CardProduct {
			product = row
			break
		}
	}
	if product == nil {
		return 0, &UpstreamError{"card_product_unconfirmed", true}
	}
	min, ok := usdMinor(product["min_amount"])
	if id == 0 && (!ok || s.Rule.FundingMinor < min) {
		return 0, &UpstreamError{"card_funding_minimum", true}
	}
	if id > 0 && product["recharge_min_amount"] != nil {
		minimum, valid := usdMinor(product["recharge_min_amount"])
		if !valid || s.Rule.FundingMinor < minimum {
			return 0, &UpstreamError{"card_funding_minimum", true}
		}
	}
	// The reference API does not define a unit for nonzero recharge_fee. Do not guess it.
	if n, ok := usdMinor(product["recharge_fee"]); !ok || n != 0 {
		return 0, &UpstreamError{"card_recharge_fee_unconfirmed", true}
	}
	fee := int64(0)
	if id == 0 {
		fee, ok = usdMinor(product["open_fee"])
		if !ok || fee > s.Rule.MaxOpenFeeMinor {
			return 0, &UpstreamError{"card_open_fee_changed", true}
		}
	}
	balance, err := cardAPI(ctx, s.Provider, "GET", "/balance", nil, "")
	if err != nil {
		return 0, err
	}
	row := obj(balance["data"])
	available, ok := usdBalanceMinor(row["spendable_balance"])
	if str(row["currency"]) != "USD" || !ok {
		return 0, &UpstreamError{"card_wallet_unconfirmed", false}
	}
	if available < s.Rule.FundingMinor+fee {
		return 0, &UpstreamError{"card_wallet_insufficient", true}
	}
	if id == 0 && (s.Provider.Billing.FirstName == "" || s.Provider.Billing.LastName == "") {
		return 0, &UpstreamError{"billing_profile_missing", true}
	}
	return fee, nil
}

func checkedCardID(row Object, id int64) error {
	if !matchesID(row["id"], id) {
		return &UpstreamError{"card_evidence_mismatch", false}
	}
	return nil
}
