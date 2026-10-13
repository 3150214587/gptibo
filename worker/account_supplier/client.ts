import { createHash } from 'node:crypto'
import { ApiError, type Env } from '../platform.ts'
import { boundedResponseText } from '../bounded-response.ts'

export type Goods = { goodsId: number; name: string; salesPrice: number; goodsType: number; status: number; skuType: number; stockCount?: number; minQuantity?: number; maxQuantity?: number; multiple?: number; rechargeTemplates?: unknown[]; buyNotice?: string }
export type Card = { cardId: string; cardNo: string; cardPwd: string; status: number }
export type SupplierOrder = { orderNumber: string; outerNumber?: string; status?: number; count?: number; money?: number; retMoney?: number; cards?: Card[] }
export class AccountSupplierRejected extends ApiError { constructor() { super('供应商拒绝本次请求，请在后台核对商品、余额或订单。', 502) } }
export function configuration(env: Env) {
  let base: URL
  try { base = new URL(env.ACCOUNT_SUPPLIER_API_BASE ?? '') } catch { throw new ApiError('Gmail 供应商尚未配置。', 503) }
  if (base.protocol !== 'https:' || base.username || base.password || base.pathname !== '/' || base.search || base.hash || !/^\d{1,20}$/.test(env.ACCOUNT_SUPPLIER_APP_ID ?? '') || !env.ACCOUNT_SUPPLIER_APP_SECRET) throw new ApiError('Gmail 供应商配置无效。', 503)
  return { base: base.origin, appId: env.ACCOUNT_SUPPLIER_APP_ID!, secret: env.ACCOUNT_SUPPLIER_APP_SECRET! }
}
export function configured(env: Env) { try { configuration(env); return true } catch { return false } }
// Protocol-mandated MD5 is used solely for this supplier's legacy signature.
export function signature(appId: string, secret: string, timestamp: string, payload: string) {
  return createHash('md5').update(appId + secret + '3.0' + timestamp + payload, 'utf8').digest('hex')
}
export async function call<T>(env: Env, path: '/goods/getList' | '/goods/getDetail' | '/order/create' | '/order/getDetail', data: Record<string, unknown>): Promise<T> {
  const config = configuration(env), payload = JSON.stringify(data), timestamp = Math.floor(Date.now() / 1000).toString()
  let response: Response
  try {
    response = await fetch(config.base + '/api/v3' + path, { method: 'POST', redirect: 'manual', signal: AbortSignal.timeout(10000), headers: { 'Content-Type': 'application/json; charset=utf-8', 'Accept': 'application/json', 'User-Agent': 'YMX-Gmail-Integration/1.0 (+https://shop.example.invalid)', 'X-App-Id': config.appId, 'X-Version': '3.0', 'X-Timestamp': timestamp, 'X-Signature': signature(config.appId, config.secret, timestamp, payload) }, body: payload })
  } catch { throw new ApiError('供应商响应尚未确认，请稍后核对原订单。', 503) }
  if (!response.ok) throw new ApiError('供应商暂不可用，请稍后核对原订单。', 503)
  let result: { code?: unknown; data?: T }
  try { result = JSON.parse(await boundedResponseText(response)) } catch { throw new ApiError('供应商返回格式异常，请核对原订单。', 502) }
  if (!result || typeof result !== 'object' || Array.isArray(result)) throw new ApiError('供应商返回格式异常，请核对原订单。', 502)
  // Never expose arbitrary supplier messages: they may contain credentials.
  if (result.code !== 1000) throw new AccountSupplierRejected()
  if (!result.data || typeof result.data !== 'object') throw new ApiError('供应商返回资料不完整，请核对原订单。', 502)
  return result.data
}
export function moneyCents(value: unknown) {
  if (typeof value !== 'number' || !Number.isFinite(value) || value < 0 || value > 100000 || Math.abs(value * 100 - Math.round(value * 100)) > 0.00001) throw new ApiError('供应商金额无效。', 502)
  return Math.round(value * 100)
}
export async function goodsDetail(env: Env, id: number) {
  if (!Number.isSafeInteger(id) || id <= 0) throw new ApiError('商品编号无效。')
  const goods = await call<Goods>(env, '/goods/getDetail', { goodsId: id })
  if (goods.goodsId !== id || typeof goods.name !== 'string' || goods.name.length > 2000) throw new ApiError('供应商商品不匹配。', 502)
  moneyCents(goods.salesPrice)
  return goods
}
export function validateGoods(goods: Goods, ceiling: number) {
  if (!/gmail|谷歌/i.test(goods.name) || goods.goodsType !== 1 || goods.status !== 1 || goods.skuType !== 0 || (goods.rechargeTemplates?.length ?? 0) > 0 || (goods.minQuantity ?? 1) > 1 || (goods.maxQuantity ?? 1) < 1 || (goods.multiple ?? 1) > 1 || (goods.stockCount ?? 0) < 1) throw new ApiError('当前 Gmail 商品不支持单账号自动发货，或暂时缺货。', 409)
  if (moneyCents(goods.salesPrice) > ceiling) throw new ApiError('Gmail 进价已上涨，购买暂时关闭，请联系支持。', 409)
}
