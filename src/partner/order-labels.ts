type ProfitOrder = { mode: string; status: string; payment_status: string; price_cents: number; cost_cents: number; profit_cents?: number; payout_status: string | null; refund_hold?: number; refunded_cents?: number }

export function profitLabel(o: ProfitOrder) {
  if (o.refund_hold) return '售后冻结'
  if ((o.refunded_cents ?? 0) >= o.price_cents) return '全额退款 · 无收益'
  if (o.mode === 'demo') return '不计入结算'
  if (o.payout_status === 'paid') return '已结算'
  if (o.payout_status === 'pending') return '结算处理中'
  if (o.status === 'cancelled') return '已关闭 · 不结算'
  if (o.payment_status !== 'paid') return '未到账 · 不结算'
  if (o.status !== 'success') return '待完成'
  return (o.profit_cents ?? o.price_cents - o.cost_cents) > 0 ? '可结算' : '无差价'
}
