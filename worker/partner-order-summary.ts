import { ApiError, type Env } from './platform.ts'
import { eligibleSql, profitSql } from './finance.ts'
import type { PartnerOrderPeriod, PartnerOrderSummary, PartnerProductSummary } from '../shared/partner-order-summary.ts'

export async function partnerOrderSummary(env: Env, partnerId: string, url: URL): Promise<PartnerOrderSummary> {
  const period = url.searchParams.get('period') ?? 'all'
  if (!['all', 'today', '7', '30'].includes(period)) throw new ApiError('请选择全部时间、今天、近 7 天或近 30 天。')
  const asOf = Date.now()
  const day = 86400000
  const today = Math.floor((asOf + 8 * 3600000) / day) * day - 8 * 3600000
  const since = period === 'all' ? null : today - ((period === 'today' ? 1 : Number(period)) - 1) * day
  // Aggregate all of this partner's real orders, independently of list pagination.
  // Reuse payout commission rules, including partial refunds and supplier caps.
  const products = (await env.INTEREST_DB.prepare(`SELECT COALESCE(c.product_id,'other') productId,
    COUNT(*) orderCount,
    SUM(CASE WHEN o.payment_status='paid' AND o.status='success' THEN 1 ELSE 0 END) completedCount,
    SUM(CASE WHEN ${eligibleSql('o')} THEN ${profitSql('o')} ELSE 0 END) profitCents,
    SUM(CASE WHEN o.payment_status='paid' AND o.status='success' AND
      (o.refund_hold=1 OR o.cost_cents IS NULL OR EXISTS(SELECT 1 FROM recharge_orders t WHERE t.order_id=o.id AND (t.financial_hold=1 OR t.merchant_margin_cents IS NULL)))
      THEN 1 ELSE 0 END) heldCount
    FROM orders o LEFT JOIN catalog_plans c ON c.id=o.plan
    WHERE o.partner_id=? AND o.mode='live' AND o.is_test=0 AND o.created_at>=? AND o.created_at<=?
    GROUP BY COALESCE(c.product_id,'other') ORDER BY profitCents DESC,orderCount DESC,productId ASC`)
    .bind(partnerId, since ?? 0, asOf).all<PartnerProductSummary>()).results
  const totals = products.reduce((sum, p) => ({
    orderCount: sum.orderCount + p.orderCount,
    completedCount: sum.completedCount + p.completedCount,
    profitCents: sum.profitCents + p.profitCents,
    heldCount: sum.heldCount + p.heldCount,
  }), { orderCount: 0, completedCount: 0, profitCents: 0, heldCount: 0 })
  return { period: period as PartnerOrderPeriod, since, asOf, totals, products }
}
