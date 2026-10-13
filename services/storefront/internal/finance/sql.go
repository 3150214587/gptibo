package finance

// SQL fragments accept only aliases chosen in source, never request parameters.
func Profit(alias string) string {
	return "partner_order_profit(" + alias + ".id," + alias + ".partner_id)"
}
func Eligible(alias string) string {
	p := alias + "."
	return p + "mode='live' AND " + p + "is_test=0 AND " + p + "payment_status='paid' AND " + p + "status='success' AND " + p + "refund_hold=0 AND " + p + "cost_cents IS NOT NULL AND NOT EXISTS(SELECT 1 FROM recharge_orders t WHERE t.order_id=" + p + "id AND (t.financial_hold=1 OR t.merchant_margin_cents IS NULL)) AND (" + Profit(alias) + ")>0"
}
