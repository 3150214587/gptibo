package store

import (
	"context"
	"github.com/jackc/pgx/v5"
)

// An explicit allowlist avoids turning routine polling and queue waits into alerts.
func ManualReason(code string) string {
	switch code {
	case "payment_action_required", "payment_manual_action":
		return "付款遇到验证码或支付验证，请进入后台继续原单付款"
	case "payment_manual_review":
		return "原支付验证已失效，需要人工核对并继续原单付款"
	case "card_pair_declined":
		return "主卡和备用卡均拒付，需要人工检查"
	case "card_declined", "card_replacement_limit":
		return "卡片拒付或已达到换卡上限，需要人工检查"
	case "upstream_charge_requires_review", "execution_requires_reconciliation":
		return "上游执行结果需要人工核对，请先确认原单结果，避免重复付款"
	case "configuration_unavailable", "credential_unavailable", "checkpoint_unavailable", "channel_unavailable", "business_driver_missing":
		return "订单配置或执行凭证异常，需要管理员检查"
	}
	return ""
}

func EnqueueManualAlert(ctx context.Context, tx pgx.Tx, orderID, reason string) error {
	if ManualReason(reason) == "" {
		return nil
	}
	// One durable alert per order, also across reason changes and process restarts.
	_, err := tx.Exec(ctx, "INSERT INTO manual_alerts(order_id,reason) VALUES($1,$2) ON CONFLICT(order_id) DO NOTHING", orderID, reason)
	return err
}
