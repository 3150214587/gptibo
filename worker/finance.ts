export const MIN_PAYOUT_CENTS = 1000
export const PAYOUT_COOLDOWN_MS = 24 * 60 * 60 * 1000
export const profitSql = (alias = '') => {
  const p = alias ? alias + '.' : 'orders.'
  const agreed = `(${p}price_cents-${p}cost_cents-CAST((${p}price_cents-${p}cost_cents)*${p}refunded_cents/${p}price_cents AS INTEGER))`
  return `CASE WHEN ${p}plan='duolingo-12m' AND ${p}refunded_cents>0 THEN 0 ELSE MAX(0,MIN(${agreed},COALESCE((SELECT t.merchant_margin_cents FROM recharge_orders t WHERE t.order_id=${p}id),${agreed}))) END`
}
export const eligibleSql = (alias = '') => {
  const p = alias ? alias + '.' : 'orders.'
  return `${p}mode='live' AND ${p}payment_status='paid' AND ${p}status='success' AND ${p}refund_hold=0 AND ${p}cost_cents IS NOT NULL AND NOT EXISTS(SELECT 1 FROM recharge_orders t WHERE t.order_id=${p}id AND (t.financial_hold=1 OR t.merchant_margin_cents IS NULL)) AND ${profitSql(alias)}>0`
}
