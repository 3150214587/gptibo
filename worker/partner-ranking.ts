import { ApiError, type Env } from './platform.ts'

const DAY_MS = 86400000
const CHINA_OFFSET = 8 * 3600000

export async function partnerRanking(env: Env, url: URL) {
  const daysParam = url.searchParams.get('days') ?? '1'
  if (!['1', '7', '30'].includes(daysParam)) throw new ApiError('仅支持今天、近 7 天或近 30 天。')
  const days = Number(daysParam)
  const page = Number(url.searchParams.get('page') ?? 1)
  if (!Number.isInteger(page) || page < 1 || page > 10000) throw new ApiError('页码无效。')
  const now = Date.now()
  const today = Math.floor((now + CHINA_OFFSET) / DAY_MS) * DAY_MS - CHINA_OFFSET
  const since = today - (days - 1) * DAY_MS
  const offset = (page - 1) * 20
  // Count actual sales, including partial refunds, without demo/payment test orders.
  // Use creation time consistently with the admin order list; future records never count.
  const sales = `WITH sales AS (
    SELECT partner_id,COUNT(*) order_count FROM orders
    WHERE partner_id IS NOT NULL AND mode='live' AND is_test=0
      AND payment_status='paid' AND status='success' AND refunded_cents<price_cents
      AND created_at>=? AND created_at<=?
    GROUP BY partner_id
  )`
  const rows = (await env.INTEREST_DB.prepare(`${sales}
    SELECT p.id,p.name,p.email,p.active,COALESCE(c.wechat,'') wechat,
      COALESCE(s.order_count,0) orderCount
    FROM partners p LEFT JOIN sales s ON s.partner_id=p.id
    LEFT JOIN support_contacts c ON c.owner=p.id
    ORDER BY orderCount DESC,p.created_at ASC,p.id ASC LIMIT 20 OFFSET ?`)
    .bind(since, now, offset).all<{ id: string; name: string; email: string; active: number; wechat: string; orderCount: number }>()).results
  const totals = await env.INTEREST_DB.prepare(`${sales}
    SELECT COUNT(*) total,COALESCE(SUM(s.order_count),0) orderCount,
      COUNT(s.partner_id) sellingPartners
    FROM partners p LEFT JOIN sales s ON s.partner_id=p.id`)
    .bind(since, now).first<{ total: number; orderCount: number; sellingPartners: number }>()
  return {
    days, page, since, until: now,
    total: totals?.total ?? 0,
    orderCount: totals?.orderCount ?? 0,
    sellingPartners: totals?.sellingPartners ?? 0,
    rows: rows.map((row, index) => ({ ...row, rank: offset + index + 1 })),
  }
}
