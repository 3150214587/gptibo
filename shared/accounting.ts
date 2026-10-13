export type AccountingTotals = {
  order_count: number; adjustment_count: number; sale_cents: number; cost_cents: number; refund_cents: number; profit_cents: number; missing_cost_count: number
}
export type AccountingSettlement = Omit<AccountingTotals, 'missing_cost_count'> & { id: string; base_version: number; previous_id: string | null; from_at: number; closed_at: number }
export type AccountingGroup = AccountingTotals & { plan: string; plan_name: string }
export type AccountingCost = { plan: string; name: string; cost_cents: number | null }
export type AccountingSummary = {
  version: number; fromAt: number | null; asOf: number; costs: AccountingCost[]; totals: AccountingTotals; groups: AccountingGroup[]; heldCount: number; history: AccountingSettlement[]; historyTotal: number; page: number
}
export type AccountingItem = { order_id: string; plan_name: string; kind: 'order' | 'refund'; sale_cents: number; cost_cents: number; refund_cents: number; created_at: number }
export type AccountingDetail = { settlement: AccountingSettlement; groups: AccountingGroup[]; items: AccountingItem[]; total: number; page: number }

export type AccountingReportTotals = {
  order_count: number; paid_count: number; completed_count: number; paid_cents: number; refund_cents: number
  completed_paid_cents: number; completed_refund_cents: number; cost_cents: number; commission_cents: number; profit_cents: number | null
  missing_cost_count: number; missing_commission_count: number; held_count: number
}
export type AccountingReportOrder = {
  id: string; plan_name: string; status: string; payment_status: string; partner_name: string | null; created_at: number
  paid_cents: number; refund_cents: number; cost_cents: number | null; commission_cents: number | null; profit_cents: number | null; held: number
}
export type AccountingReport = {
  totals: AccountingReportTotals; groups: (AccountingReportTotals & { plan: string; plan_name: string })[]
  orders: AccountingReportOrder[]; page: number; asOf: number; start: number | null; end: number | null; status: string
}
