import { signature } from '../../shared/xgift-signature.ts'
import { ApiError, type Env } from '../platform.ts'
import { boundedResponseText } from '../bounded-response.ts'
export interface XGiftOrder {
  id: string
  merchant_order_no: string
  product_code: string
  recipient: string
  points: number
  currency: string
  amount_minor: number
  status: 'queued' | 'running' | 'unknown' | 'succeeded' | 'failed'
  receipt: string | null
  failure_code: string | null
  created_at: number
  updated_at: number
}
export type XGiftBackend = 'legacy' | 'platform'
export function xgiftBackend(env: Env): XGiftBackend {
  if (env.XGIFT_BACKEND && !['legacy', 'platform'].includes(env.XGIFT_BACKEND))
    throw new ApiError('X 充值供应商配置无效。', 503)
  return env.XGIFT_BACKEND === 'platform' ? 'platform' : 'legacy'
}
export function configured(env: Env, backend: XGiftBackend = xgiftBackend(env)) {
  return backend === 'platform'
    ? !!env.XGIFT_PLATFORM_KEY_ID && !!env.XGIFT_PLATFORM_CLIENT_SECRET
    : !!env.XGIFT_USER_ID && !!env.XGIFT_KEY_ID && !!env.XGIFT_CLIENT_SECRET
}
export class XGiftError extends ApiError {
  code: string
  constructor(code: string, status: number) {
    super('X 赠送服务结果待核对，请继续原订单。', status)
    this.code = code
  }
}
export async function xgiftRequest<T>(
  env: Env,
  path: string,
  body?: Record<string, unknown>,
  idempotency = '',
  backend: XGiftBackend = 'legacy',
): Promise<T> {
  if (!configured(env, backend)) throw new ApiError('X 充值供应商尚未配置。', 503)
  const base = new URL(backend === 'platform'
    ? env.XGIFT_PLATFORM_API_BASE ?? 'https://api.shop.example.invalid/v1'
    : env.XGIFT_API_BASE ?? 'https://x-api.shop.example.invalid/v1')
  const keyId = backend === 'platform' ? env.XGIFT_PLATFORM_KEY_ID! : env.XGIFT_KEY_ID!
  const secret = backend === 'platform' ? env.XGIFT_PLATFORM_CLIENT_SECRET! : env.XGIFT_CLIENT_SECRET!
  if (
    base.protocol !== 'https:' ||
    base.username ||
    base.password ||
    base.search ||
    base.hash ||
    base.pathname.replace(/\/$/, '') !== '/v1' ||
    !/^\/[a-z][A-Za-z0-9_/?=&%.-]*$/.test(path)
  )
    throw new ApiError('X 充值供应商地址无效。', 503)
  const url = new URL(base.origin + '/v1' + path),
    method = body ? 'POST' : 'GET',
    raw = body ? JSON.stringify(body) : '',
    timestamp = String(Math.floor(Date.now() / 1000)),
    nonce = crypto.randomUUID()
  if (body && !/^[A-Za-z0-9_.:-]{8,128}$/.test(idempotency))
    throw new ApiError('充值订单需要稳定的幂等键。')
  try {
    const response = await fetch(url, {
      method,
      body: body ? raw : undefined,
      headers: {
        'Content-Type': 'application/json',
        ...(backend === 'legacy' ? { 'X-Partner-Id': env.XGIFT_USER_ID! } : {}),
        'X-Key-Id': keyId,
        'X-Timestamp': timestamp,
        'X-Nonce': nonce,
        'X-Signature': await signature(
          secret,
          method,
          url,
          timestamp,
          nonce,
          keyId,
          idempotency,
          raw,
        ),
        ...(body ? { 'Idempotency-Key': idempotency } : {}),
      },
      redirect: 'manual',
      signal: AbortSignal.timeout(30000),
    })
    const value = JSON.parse(await boundedResponseText(response))
    if (!response.ok && typeof value.error?.code === 'string' && /^[a-z0-9_]{1,64}$/.test(value.error.code))
      throw new XGiftError(value.error.code, response.status)
    if (!response.ok || value.data == null)
      throw new Error('Unknown supplier response')
    return value.data as T
  } catch (error) {
    if (error instanceof XGiftError) throw error
    throw new ApiError(
      'X 充值服务结果暂未确认，请按原商户订单号查询，勿重复下单。',
      503,
    )
  }
}
const platformCodes: Record<string, string> = { 'x-premium-3m': 'x-3m', 'x-premium-6m': 'x-6m' }
function platformCode(code: string) {
  if (!platformCodes[code]) throw new ApiError('X 套餐无效。')
  return platformCodes[code]
}
function money(value: unknown): value is number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0
}
export async function xgiftProducts(env: Env, backend: XGiftBackend = xgiftBackend(env)) {
  if (backend === 'legacy') return xgiftRequest<Array<{ code: string; name: string; points: number; enabled: number }>>(env, '/products')
  const rows = await xgiftRequest<Array<{ code: string; name: string; business: string; supply_minor: number; enabled: boolean; available: boolean }>>(env, '/products', undefined, '', backend)
  if (!Array.isArray(rows)) throw new ApiError('供货商品暂未确认。', 503)
  return Object.entries(platformCodes).map(([code, upstream]) => {
    const matches = rows.filter(r => r.code === upstream && r.business === 'x')
    if (matches.length !== 1 || !money(matches[0].supply_minor)) throw new ApiError('X 供货商品暂未确认。', 503)
    const p = matches[0]
    return { code, name: p.name, points: p.supply_minor, enabled: Number(p.enabled === true && p.available === true) }
  })
}
export async function xgiftBalance(env: Env, backend: XGiftBackend = xgiftBackend(env)) {
  if (backend === 'legacy') return xgiftRequest<{ available: number; frozen: number }>(env, '/balance')
  const b = await xgiftRequest<{ currency: string; available_minor: number; frozen_minor: number }>(env, '/balance', undefined, '', backend)
  if (b.currency !== 'CNY' || !money(b.available_minor) || !money(b.frozen_minor)) throw new ApiError('人民币采购余额暂未确认。', 503)
  return { available: b.available_minor, frozen: b.frozen_minor }
}
interface PlatformOrder {
  id: string; merchant_order_no: string; product_code: string; business: string; supply_minor: number;
  recipient: string; status: XGiftOrder['status']; receipt: string; failure_code: string;
  created_at: string; updated_at: string
}
function platformOrder(p: PlatformOrder): XGiftOrder {
  const product = Object.keys(platformCodes).find(k => platformCodes[k] === p.product_code)
  if (p.business !== 'x' || !product || !money(p.supply_minor) || p.supply_minor <= 0 || !/^ord_[a-f0-9]{32}$/.test(p.id)
    || !/^[a-z0-9_]{1,15}$/.test(p.recipient) || !['queued','running','unknown','succeeded','failed'].includes(p.status)
    || !Number.isFinite(Date.parse(p.created_at)) || !Number.isFinite(Date.parse(p.updated_at)))
    throw new ApiError('供货订单结果待核对。', 503)
  // CNY is the platform procurement amount; Stripe's BDT quote is guarded by the provider.
  return { id: p.id, merchant_order_no: p.merchant_order_no, product_code: product, recipient: p.recipient,
    points: p.supply_minor, currency: 'CNY', amount_minor: p.supply_minor, status: p.status,
    receipt: p.receipt || null, failure_code: p.failure_code || null,
    created_at: Date.parse(p.created_at), updated_at: Date.parse(p.updated_at) }
}
export const createXGiftOrder = (
  env: Env,
  merchantOrder: string,
  product: string,
  recipient: string,
  recipientId?: string,
  expectedPoints?: number,
  backend: XGiftBackend = xgiftBackend(env),
) =>
  backend === 'platform' ? xgiftRequest<PlatformOrder>(env, '/orders', {
    merchant_order_no: merchantOrder, product_code: platformCode(product), recipient,
    confirmed_recipient_id: recipientId, expected_supply_minor: expectedPoints,
  }, 'gptibo:' + merchantOrder, backend).then(platformOrder) : xgiftRequest<XGiftOrder>(
    env,
    '/orders',
    { merchant_order_no: merchantOrder, product_code: product, recipient, recipient_id: recipientId, expected_points: expectedPoints },
    'gptibo:' + merchantOrder,
  )
export async function xgiftEligibility(env: Env, username: string, product = 'x-premium-3m', backend: XGiftBackend = xgiftBackend(env)) {
  if (backend === 'legacy') return xgiftRequest<{ username: string; recipient_id?: string; eligible: boolean; reason: string | null }>(env, '/eligibility', { username }, 'check:' + crypto.randomUUID())
  const check = await xgiftRequest<{ recipient: string; recipient_id: string; eligible: boolean }>(env, '/preflight', { product_code: platformCode(product), recipient: username }, 'check:' + crypto.randomUUID(), backend)
  return { username: check.recipient, recipient_id: check.recipient_id, eligible: check.eligible, reason: check.eligible === false ? 'not_eligible' : null }
}
export async function xgiftCapabilities(env: Env, backend: XGiftBackend = xgiftBackend(env)) {
  if (backend === 'legacy') return xgiftRequest<{ execution_ready: boolean; accepts_orders: boolean }>(env, '/capabilities')
  const products = await xgiftProducts(env, backend)
  const ready = products.some(p => p.enabled === 1 && p.points > 0)
  return { execution_ready: ready, accepts_orders: ready }
}
export const queryXGiftOrder = (env: Env, merchantOrder: string, backend: XGiftBackend = xgiftBackend(env)) =>
  backend === 'platform' ? xgiftRequest<PlatformOrder>(env, '/orders/by-merchant/' + encodeURIComponent(merchantOrder), undefined, '', backend).then(platformOrder) : xgiftRequest<XGiftOrder>(
    env,
    '/orders?merchant_order_no=' + encodeURIComponent(merchantOrder),
  )
