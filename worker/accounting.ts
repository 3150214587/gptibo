import { ApiError, body, json, type Env } from './platform.ts'
import type { AccountingCost, AccountingGroup, AccountingSettlement, AccountingTotals } from '../shared/accounting.ts'
import { accountingReport } from './accounting-report.ts'

const totalsSql = `COALESCE(SUM(CASE WHEN kind='order' THEN 1 ELSE 0 END),0) order_count,
  COALESCE(SUM(CASE WHEN kind='refund' THEN 1 ELSE 0 END),0) adjustment_count,
  COALESCE(SUM(sale_cents),0) sale_cents,COALESCE(SUM(cost_cents),0) cost_cents,
  COALESCE(SUM(refund_cents),0) refund_cents,COALESCE(SUM(sale_cents-cost_cents-refund_cents),0) profit_cents,
  COALESCE(SUM(CASE WHEN cost_cents IS NULL THEN 1 ELSE 0 END),0) missing_cost_count`
type State = { version: number; last_settlement_id: string | null; closed_at: number | null }
function pageParam(url: URL) {
  const page = Number(url.searchParams.get('page') ?? 1)
  if (!Number.isInteger(page) || page < 1 || page > 10000) throw new ApiError('页码无效。')
  return page
}
export async function accountingRoutes(request: Request, env: Env, url: URL) {
  const db = env.INTEREST_DB; const path = url.pathname
  if (path === '/api/admin/accounting/report' && request.method === 'GET') return accountingReport(env,url)
  if (path === '/api/admin/accounting' && request.method === 'GET') {
    const page = pageParam(url)
    // One D1 batch provides a consistent preview even during another admin's close.
    const results = await db.batch([
      db.prepare('SELECT s.version,s.last_settlement_id,a.closed_at FROM accounting_state s LEFT JOIN accounting_settlements a ON a.id=s.last_settlement_id WHERE s.id=1'),
      db.prepare(`SELECT ${totalsSql},MIN(created_at) first_at FROM accounting_candidates`),
      db.prepare(`SELECT plan,MAX(plan_name) plan_name,${totalsSql} FROM accounting_candidates GROUP BY plan ORDER BY plan`),
      db.prepare('SELECT p.id plan,p.name,c.cost_cents FROM catalog_plans p LEFT JOIN accounting_costs c ON c.plan=p.id ORDER BY p.rowid'),
      db.prepare('SELECT * FROM accounting_settlements ORDER BY closed_at DESC,base_version DESC LIMIT 10 OFFSET ?').bind((page-1)*10),
      db.prepare('SELECT COUNT(*) total FROM accounting_settlements'),
      db.prepare(`SELECT COUNT(*) total FROM orders o LEFT JOIN recharge_orders t ON t.order_id=o.id WHERE o.mode='live' AND o.is_test=0 AND o.payment_status='paid' AND (o.refund_hold=1 OR COALESCE(t.financial_hold,0)=1) AND (o.status='success' OR EXISTS(SELECT 1 FROM accounting_items i WHERE i.order_id=o.id))`),
    ]) as { results: unknown[] }[]
    const state = results[0].results[0] as State
    const totals = results[1].results[0] as AccountingTotals & { first_at: number | null }
    return json({ version: state.version, fromAt: state.closed_at ?? totals.first_at, asOf: Date.now(), totals, groups: results[2].results, costs: results[3].results, history: results[4].results, historyTotal: (results[5].results[0] as { total: number }).total, heldCount: (results[6].results[0] as { total: number }).total, page })
  }
  if (path === '/api/admin/accounting/costs' && request.method === 'POST') {
    const data = await body(request)
    if (!Number.isSafeInteger(data.version) || !Array.isArray(data.costs)) throw new ApiError('成本设置无效。')
    const plans = (await db.prepare('SELECT id plan FROM catalog_plans').all<{ plan: string }>()).results
    const costs = data.costs as AccountingCost[]
    if (costs.length !== plans.length || new Set(costs.map(c => c?.plan)).size !== plans.length || costs.some(c => !c || !plans.some(p => p.plan === c.plan) || (c.cost_cents !== null && (!Number.isSafeInteger(c.cost_cents) || c.cost_cents < 0 || c.cost_cents > 10000000)))) throw new ApiError('请填写有效采购成本，最多两位小数；未知成本可以留空。')
    const token = crypto.randomUUID(); const now = Date.now()
    await db.batch([
      db.prepare("INSERT INTO admin_audit(id,action,target_id,note,created_at) SELECT ?,'accounting-costs','accounting',?,? FROM accounting_state WHERE id=1 AND version=?").bind(token, JSON.stringify(costs.map(c => ({ plan: c.plan, costCents: c.cost_cents }))), now, data.version as number),
      ...costs.map(c => db.prepare('INSERT INTO accounting_costs(plan,cost_cents) SELECT ?,? WHERE EXISTS(SELECT 1 FROM admin_audit WHERE id=?) ON CONFLICT(plan) DO UPDATE SET cost_cents=excluded.cost_cents').bind(c.plan, c.cost_cents, token)),
      db.prepare('UPDATE accounting_state SET version=version+1 WHERE id=1 AND EXISTS(SELECT 1 FROM admin_audit WHERE id=?)').bind(token),
    ])
    if (!await db.prepare('SELECT id FROM admin_audit WHERE id=?').bind(token).first()) throw new ApiError('清账或成本设置已更新，请刷新后重试。', 409)
    return json({ ok: true })
  }
  if (path === '/api/admin/accounting/close' && request.method === 'POST') {
    const data = await body(request)
    if (!Number.isSafeInteger(data.version) || typeof data.key !== 'string' || !/^[a-f0-9-]{36}$/.test(data.key)) throw new ApiError('清账请求无效。')
    const existing = await db.prepare('SELECT * FROM accounting_settlements WHERE id=?').bind(data.key).first<AccountingSettlement>()
    if (existing) {
      if (existing.base_version !== data.version) throw new ApiError('请求编号已被使用。', 409)
      return json({ settlement: existing })
    }
    const now = Date.now(); const key = data.key; const version = data.version as number
    // Header, all order snapshots, totals, cursor and audit share one transaction.
    // A version guard makes repeated or concurrent clicks unable to double count.
    await db.batch([
      db.prepare(`INSERT INTO accounting_settlements(id,base_version,previous_id,from_at,closed_at)
        SELECT ?,s.version,s.last_settlement_id,COALESCE(a.closed_at,(SELECT MIN(created_at) FROM accounting_candidates)),?
        FROM accounting_state s LEFT JOIN accounting_settlements a ON a.id=s.last_settlement_id
        WHERE s.id=1 AND s.version=? AND COALESCE(a.closed_at,0)<=?
          AND EXISTS(SELECT 1 FROM accounting_candidates) AND NOT EXISTS(SELECT 1 FROM accounting_candidates WHERE cost_cents IS NULL)
        ON CONFLICT DO NOTHING`).bind(key, now, version, now),
      db.prepare(`INSERT INTO accounting_items(settlement_id,order_id,plan,plan_name,kind,sale_cents,cost_cents,refund_cents,created_at)
        SELECT ?,order_id,plan,plan_name,kind,sale_cents,cost_cents,refund_cents,created_at FROM accounting_candidates
        WHERE EXISTS(SELECT 1 FROM accounting_settlements a JOIN accounting_state s ON s.version=a.base_version WHERE a.id=? AND s.id=1)
        ON CONFLICT DO NOTHING`).bind(key, key),
      db.prepare(`UPDATE accounting_settlements SET
        order_count=(SELECT COUNT(*) FROM accounting_items WHERE settlement_id=? AND kind='order'),
        adjustment_count=(SELECT COUNT(*) FROM accounting_items WHERE settlement_id=? AND kind='refund'),
        sale_cents=(SELECT COALESCE(SUM(sale_cents),0) FROM accounting_items WHERE settlement_id=?),
        cost_cents=(SELECT COALESCE(SUM(cost_cents),0) FROM accounting_items WHERE settlement_id=?),
        refund_cents=(SELECT COALESCE(SUM(refund_cents),0) FROM accounting_items WHERE settlement_id=?),
        profit_cents=(SELECT COALESCE(SUM(sale_cents-cost_cents-refund_cents),0) FROM accounting_items WHERE settlement_id=?)
        WHERE id=? AND base_version=(SELECT version FROM accounting_state WHERE id=1)`).bind(key,key,key,key,key,key,key),
      db.prepare("INSERT INTO admin_audit(id,action,target_id,note,created_at) SELECT id,'accounting-close',id,'记录清账统计，付款金额减采购成本和退款',closed_at FROM accounting_settlements WHERE id=? AND base_version=(SELECT version FROM accounting_state WHERE id=1) ON CONFLICT DO NOTHING").bind(key),
      db.prepare('UPDATE accounting_state SET version=version+1,last_settlement_id=? WHERE id=1 AND version=? AND EXISTS(SELECT 1 FROM accounting_settlements WHERE id=? AND base_version=?)').bind(key,version,key,version),
    ])
    const settlement = await db.prepare('SELECT * FROM accounting_settlements WHERE id=?').bind(key).first<AccountingSettlement>()
    if (!settlement) throw new ApiError('暂无可清账订单，或数据与成本已更新、存在未填写成本。请刷新后核对。', 409)
    return json({ settlement }, 201)
  }
  const match = path.match(/^\/api\/admin\/accounting\/([a-f0-9-]{36})$/)
  if (match && request.method === 'GET') {
    const page = pageParam(url); const id = match[1]
    const settlement = await db.prepare('SELECT * FROM accounting_settlements WHERE id=?').bind(id).first<AccountingSettlement>()
    if (!settlement) throw new ApiError('未找到清账记录。', 404)
    const groups = (await db.prepare(`SELECT plan,MAX(plan_name) plan_name,${totalsSql} FROM accounting_items WHERE settlement_id=? GROUP BY plan ORDER BY plan`).bind(id).all<AccountingGroup>()).results
    const items = (await db.prepare('SELECT * FROM accounting_items WHERE settlement_id=? ORDER BY created_at,order_id LIMIT 50 OFFSET ?').bind(id,(page-1)*50).all()).results
    return json({ settlement, groups, items, total: settlement.order_count+settlement.adjustment_count, page })
  }
  throw new ApiError('未找到清账接口。', 404)
}
