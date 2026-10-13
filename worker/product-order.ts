import { DEFAULT_PRODUCT_ORDER, normalizeProductOrder, type ProductOrderSettings } from '../shared/product-order.ts'
import { ApiError, body, json, type Env } from './platform.ts'
import type { Partner } from './partner-auth.ts'

export async function productOrderSettings(env: Env, partnerId: string): Promise<ProductOrderSettings> {
  const row = await env.INTEREST_DB.prepare('SELECT product_ids,hidden_product_ids,version FROM partner_product_order WHERE partner_id=?').bind(partnerId).first<{ product_ids: string; hidden_product_ids: string; version: number }>()
  return { productIds: normalizeProductOrder(row ? JSON.parse(row.product_ids) : []), version: row?.version ?? 0, hiddenProductIds: row ? JSON.parse(row.hidden_product_ids) : [] }
}

export async function productOrderRoute(request: Request, env: Env, partner: Partner) {
  if (request.method === 'GET') return json(await productOrderSettings(env, partner.id))
  if (request.method !== 'POST') throw new ApiError('请通过商品排序表单保存。', 405)
  const data = await body(request)
  if (!Array.isArray(data.productIds) || data.productIds.length !== DEFAULT_PRODUCT_ORDER.length ||
      new Set(data.productIds).size !== DEFAULT_PRODUCT_ORDER.length ||
      !data.productIds.every(id => DEFAULT_PRODUCT_ORDER.some(known => known === id))) {
    throw new ApiError('商品列表无效，请刷新后重新排序。')
  }
  if (!Number.isSafeInteger(data.version) || Number(data.version) < 0) throw new ApiError('排序版本无效，请刷新重试。')
  if (data.hiddenProductIds != null && (!Array.isArray(data.hiddenProductIds) || new Set(data.hiddenProductIds).size !== data.hiddenProductIds.length || !data.hiddenProductIds.every(id => DEFAULT_PRODUCT_ORDER.some(known => known === id)))) throw new ApiError('下架商品列表无效或重复。')
  const id = crypto.randomUUID(); const now = Date.now(); const db = env.INTEREST_DB
  await db.batch([
    db.prepare(`INSERT INTO partner_product_order(partner_id,product_ids,hidden_product_ids,version,change_id,updated_at)
      SELECT id,?,COALESCE(?,(SELECT hidden_product_ids FROM partner_product_order WHERE partner_id=?),'[]'),1,?,? FROM partners WHERE id=? AND active=1 AND auth_version=?
      AND COALESCE((SELECT version FROM partner_product_order WHERE partner_id=?),0)=?
      ON CONFLICT(partner_id) DO UPDATE SET product_ids=excluded.product_ids,hidden_product_ids=excluded.hidden_product_ids,version=partner_product_order.version+1,change_id=excluded.change_id,updated_at=excluded.updated_at`)
      .bind(JSON.stringify(data.productIds), data.hiddenProductIds == null ? null : JSON.stringify(data.hiddenProductIds), partner.id, id, now, partner.id, partner.auth_version, partner.id, Number(data.version)),
    db.prepare("INSERT INTO admin_audit(id,target_id,action,note,created_at) SELECT ?,partner_id,'partner-product-order','代理调整商品顺序及上下架',? FROM partner_product_order WHERE partner_id=? AND change_id=?")
      .bind(id, now, partner.id, id),
  ])
  if (!await db.prepare('SELECT id FROM admin_audit WHERE id=?').bind(id).first()) throw new ApiError('账号或商品排序已更新，请刷新后重试。', 409)
  return json(await productOrderSettings(env, partner.id))
}
