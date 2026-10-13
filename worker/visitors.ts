import { ApiError, body, hash, json, type Env } from './platform.ts'

const localDay = (time: number) => new Date(time + 8 * 3600000).toISOString().slice(0, 10)
const daysAgo = (days: number) => localDay(Date.now() - (days - 1) * 86400000)

function period(url: URL) {
  const value = url.searchParams.get('days') ?? '7'
  if (value !== '7' && value !== '30' && value !== '90') throw new ApiError('仅支持最近 7 天、30 天或 90 天。')
  return Number(value)
}

export async function recordVisitor(request: Request, env: Env) {
  if (request.method !== 'POST') throw new ApiError('请使用 POST 提交。', 405)
  const data = await body(request, 512)
  if (typeof data.visitorId !== 'string' || !/^[a-f0-9-]{36}$/.test(data.visitorId)) throw new ApiError('访客标识无效。')
  if (typeof data.path !== 'string' || data.path.length > 120) throw new ApiError('页面路径无效。')
  const path = data.path.replace(/\/$/, '') || '/'
  const match = path.match(/^\/(?:r|s)\/([^/]+)(?:\/(?:orders|product\/[a-z0-9-]+))?$/)
  if (!match && path !== '/' && path !== '/orders' && !/^\/product\/[a-z0-9-]+$/.test(path)) throw new ApiError('页面路径无效。')
  let shopId = 'platform'
  if (match) {
    const partner = await env.INTEREST_DB.prepare('SELECT id FROM partners WHERE slug=? AND active=1').bind(match[1]).first<{ id: string }>()
    if (!partner) throw new ApiError('代理商店不可用。', 404)
    shopId = partner.id
  }
  await env.INTEREST_DB.prepare('INSERT INTO visitor_days(day,shop_id,visitor_hash,page_views) VALUES (?,?,?,1) ON CONFLICT(day,shop_id,visitor_hash) DO UPDATE SET page_views=page_views+1')
    .bind(localDay(Date.now()), shopId, await hash(data.visitorId)).run()
  return json({ ok: true })
}

export async function visitorSummary(env: Env, url: URL, shopId?: string) {
  const days = period(url)
  const since = daysAgo(days)
  const filter = shopId ? 'day>=? AND shop_id=?' : 'day>=?'
  const args = shopId ? [since, shopId] : [since]
  const rows = (await env.INTEREST_DB.prepare(`SELECT day,SUM(page_views) pageViews,COUNT(DISTINCT visitor_hash) visitors FROM visitor_days WHERE ${filter} GROUP BY day ORDER BY day`).bind(...args).all<{ day: string; pageViews: number; visitors: number }>()).results
  const total = await env.INTEREST_DB.prepare(`SELECT COALESCE(SUM(page_views),0) pageViews,COUNT(DISTINCT visitor_hash) visitors FROM visitor_days WHERE ${filter}`).bind(...args).first<{ pageViews: number; visitors: number }>()
  const byDay = new Map(rows.map(row => [row.day, row]))
  const trend = Array.from({ length: days }, (_, index) => {
    const day = localDay(Date.now() - (days - 1 - index) * 86400000)
    return byDay.get(day) ?? { day, pageViews: 0, visitors: 0 }
  })
  return { days, pageViews: total?.pageViews ?? 0, visitors: total?.visitors ?? 0, trend }
}

export async function adminVisitorSummary(env: Env, url: URL) {
  const summary = await visitorSummary(env, url)
  const since = daysAgo(summary.days)
  const platform = await env.INTEREST_DB.prepare("SELECT COALESCE(SUM(page_views),0) pageViews,COUNT(DISTINCT visitor_hash) visitors FROM visitor_days WHERE day>=? AND shop_id='platform'").bind(since).first<{ pageViews: number; visitors: number }>()
  const partners = (await env.INTEREST_DB.prepare(`SELECT p.id,p.name,p.slug,p.active,COALESCE(SUM(v.page_views),0) pageViews,COUNT(DISTINCT v.visitor_hash) visitors
    FROM partners p LEFT JOIN visitor_days v ON v.shop_id=p.id AND v.day>=? GROUP BY p.id ORDER BY visitors DESC,pageViews DESC,p.created_at DESC`).bind(since).all<{ id: string; name: string; slug: string; active: number; pageViews: number; visitors: number }>()).results
  return { ...summary, platform: platform ?? { pageViews: 0, visitors: 0 }, partners }
}
