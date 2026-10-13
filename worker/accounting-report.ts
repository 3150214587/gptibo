import { ApiError, json, type Env, type Value } from './platform.ts'
import { profitSql } from './finance.ts'
import type { AccountingReport, AccountingReportTotals } from '../shared/accounting.ts'

const statuses = ['all','pending','session','processing','success','failed','cancelled','refunded']
function timestamp(url: URL, name: string) {
  const raw = url.searchParams.get(name)
  if (raw === null || raw === '') return null
  const value = Number(raw)
  if (!/^\d{1,14}$/.test(raw) || !Number.isSafeInteger(value) || value < 0 || value > 8640000000000000) throw new ApiError('时间范围无效。')
  return value
}
const sums = `COUNT(*) order_count,
  COALESCE(SUM(paid),0) paid_count,COALESCE(SUM(completed),0) completed_count,
  COALESCE(SUM(paid_cents),0) paid_cents,COALESCE(SUM(refund_cents),0) refund_cents,
  COALESCE(SUM(CASE WHEN completed=1 THEN paid_cents ELSE 0 END),0) completed_paid_cents,
  COALESCE(SUM(CASE WHEN completed=1 THEN refund_cents ELSE 0 END),0) completed_refund_cents,
  COALESCE(SUM(cost_cents),0) cost_cents,COALESCE(SUM(commission_cents),0) commission_cents,
  CASE WHEN COUNT(*)=COUNT(profit_cents) THEN COALESCE(SUM(profit_cents),0) ELSE NULL END profit_cents,
  COALESCE(SUM(CASE WHEN completed=1 AND cost_cents IS NULL THEN 1 ELSE 0 END),0) missing_cost_count,
  COALESCE(SUM(CASE WHEN completed=1 AND commission_cents IS NULL THEN 1 ELSE 0 END),0) missing_commission_count,
  COALESCE(SUM(CASE WHEN completed=1 THEN held ELSE 0 END),0) held_count`

export async function accountingReport(env: Env, url: URL) {
  const start = timestamp(url,'start'); const end = timestamp(url,'end')
  const status = url.searchParams.get('status') ?? 'success'; const page = Number(url.searchParams.get('page') ?? 1)
  if (!statuses.includes(status)) throw new ApiError('订单状态无效。')
  if (start !== null && end !== null && start >= end) throw new ApiError('结束时间必须晚于开始时间。')
  if (!Number.isInteger(page) || page < 1 || page > 10000) throw new ApiError('页码无效。')
  const clauses = ["o.mode='live'",'o.is_test=0']; const values: Value[] = []
  if (start !== null) { clauses.push('o.created_at>=?'); values.push(start) }
  if (end !== null) { clauses.push('o.created_at<?'); values.push(end) }
  if (status === 'refunded') clauses.push('MAX(o.refunded_cents,COALESCE(t.refunded_cents,0))>0')
  else if (status !== 'all') { clauses.push('o.status=?'); values.push(status) }
  // The customer payment and the agent's supply-price snapshot are different
  // amounts. The procurement setting (or saved close snapshot) is our cost;
  // profitSql is the same earned commission used by the payout system, whether
  // that commission has already been withdrawn or is still owed to the agent.
  const source = `SELECT o.id,o.plan,o.plan_name,o.status,o.payment_status,o.partner_name,o.created_at,
    CASE WHEN o.payment_status='paid' THEN 1 ELSE 0 END paid,
    CASE WHEN o.payment_status='paid' AND o.status='success' THEN 1 ELSE 0 END completed,
    CASE WHEN o.payment_status='paid' THEN o.price_cents ELSE 0 END paid_cents,
    CASE WHEN o.payment_status='paid' THEN MAX(o.refunded_cents,COALESCE(t.refunded_cents,0)) ELSE 0 END refund_cents,
    CASE WHEN o.payment_status='paid' AND o.status='success' THEN COALESCE(i.cost_cents,CASE WHEN o.plan='gmail-account' THEN k.actual_cost_cents WHEN n.order_id IS NOT NULL THEN n.actual_cost_cents ELSE c.cost_cents END) ELSE 0 END cost_cents,
    CASE WHEN o.payment_status<>'paid' OR o.status<>'success' OR o.partner_id IS NULL THEN 0
      WHEN o.cost_cents IS NULL OR (t.order_id IS NOT NULL AND t.merchant_margin_cents IS NULL) THEN NULL
      ELSE ${profitSql('o')} END commission_cents,
    MAX(o.refund_hold,COALESCE(t.financial_hold,0)) held
    FROM orders o LEFT JOIN recharge_orders t ON t.order_id=o.id
    LEFT JOIN account_supplier_orders k ON k.order_id=o.id
    LEFT JOIN delivery_supplier_orders n ON n.order_id=o.id
    LEFT JOIN accounting_items i ON i.order_id=o.id AND i.kind='order'
    LEFT JOIN accounting_costs c ON c.plan=o.plan WHERE ${clauses.join(' AND ')}`
  const rows = `SELECT q.*,CASE WHEN completed=0 THEN 0 WHEN held=1 THEN NULL ELSE paid_cents-refund_cents-cost_cents-commission_cents END profit_cents FROM (${source}) q`
  const db = env.INTEREST_DB
  const results = await db.batch([
    db.prepare(`SELECT ${sums} FROM (${rows})`).bind(...values),
    db.prepare(`SELECT plan,MAX(plan_name) plan_name,${sums} FROM (${rows}) GROUP BY plan ORDER BY plan`).bind(...values),
    db.prepare(`SELECT * FROM (${rows}) ORDER BY created_at DESC,id DESC LIMIT 20 OFFSET ?`).bind(...values,(page-1)*20),
  ]) as { results: unknown[] }[]
  return json({ totals: results[0].results[0] as AccountingReportTotals, groups: results[1].results, orders: results[2].results,
    page, asOf:Date.now(), start, end, status } as AccountingReport)
}
