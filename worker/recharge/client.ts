import { ApiError, hash, type Env } from '../platform.ts'
import { publicCode, requestMessages } from './errors.ts'

const encoder = new TextEncoder()
const hex = (bytes: ArrayBuffer) => Array.from(new Uint8Array(bytes), b => b.toString(16).padStart(2, '0')).join('')
const encode = (s: string) => encodeURIComponent(s).replace(/[!'()*]/g, c => '%' + c.charCodeAt(0).toString(16).toUpperCase())
export function canonicalQuery(params: URLSearchParams) {
  return Array.from(params, ([k, v]) => [encode(k), encode(v)]).sort(([ak, av], [bk, bv]) => ak < bk ? -1 : ak > bk ? 1 : av < bv ? -1 : av > bv ? 1 : 0).map(([k, v]) => `${k}=${v}`).join('&')
}
export async function hmac(secret: string, text: string) {
  const key = await crypto.subtle.importKey('raw', encoder.encode(secret), { name: 'HMAC', hash: 'SHA-256' }, false, ['sign'])
  return hex(await crypto.subtle.sign('HMAC', key, encoder.encode(text)))
}
export async function signature(secret: string, method: string, url: URL, timestamp: string, nonce: string, keyId: string, idempotency: string, rawBody: string) {
  return hmac(secret, [method, url.pathname, canonicalQuery(url.searchParams), timestamp, nonce, keyId, idempotency, await hash(rawBody)].join('\n'))
}
export function config(env: Env) {
  const base = new URL(env.RECHARGE_API_BASE ?? 'https://recharge.example.invalid/v1')
  if (base.protocol !== 'https:' || base.username || base.password || base.search || base.hash || base.pathname.replace(/\/$/, '') !== '/v1') throw new ApiError('充值服务地址配置无效。', 503)
  if (!env.RECHARGE_PARTNER_ID || !env.RECHARGE_KEY_ID || !env.RECHARGE_CLIENT_SECRET) throw new ApiError('充值服务尚未配置。', 503)
  // Key rotation within a tenant is allowed; tenant/environment switching is not.
  return { base: base.origin + '/v1', account: base.origin + ':' + env.RECHARGE_PARTNER_ID, partner: env.RECHARGE_PARTNER_ID, key: env.RECHARGE_KEY_ID, secret: env.RECHARGE_CLIENT_SECRET }
}
export function configured(env: Env) { try { config(env); return true } catch { return false } }
export function productMap(env: Env): Record<string, string> {
  try {
    const map = JSON.parse(env.RECHARGE_PRODUCT_MAP ?? '{}')
    if (!map || typeof map !== 'object' || Array.isArray(map) || Object.entries(map).some(([k, v]) => !['plus', 'pro-5x', 'pro-20x', 'pro-25x'].includes(k) || typeof v !== 'string' || !/^[A-Za-z0-9_-]{1,64}$/.test(v))) throw new Error()
    return map
  } catch { throw new ApiError('充值商品映射配置无效。', 503) }
}
export function collectionMode(env: Env) {
  const mode = env.RECHARGE_COLLECTION_MODE ?? 'platform_collect'
  if (mode !== 'platform_collect' && mode !== 'agent_collect') throw new ApiError('收款方式配置无效。', 503)
  return mode
}
export function notifyUrl(env: Env) {
  let url: URL
  try { url = new URL(env.RECHARGE_NOTIFY_URL ?? '') } catch { throw new ApiError('充值回调地址尚未配置。', 503) }
  if (url.protocol !== 'https:' || url.username || url.password || url.search || url.hash || url.pathname !== '/api/recharge/webhook') throw new ApiError('充值回调地址配置无效。', 503)
  return url.href
}
export function checkoutEnabled(env: Env) {
  try {
    return env.ORDER_TESTING_ENABLED !== 'true' && env.RECHARGE_CHECKOUT_ENABLED === 'true' && configured(env) && !!env.RECHARGE_WEBHOOK_SECRET && !!notifyUrl(env) && Object.keys(productMap(env)).length > 0 && collectionMode(env) === 'platform_collect'
  } catch { return false }
}
export class RechargeError extends ApiError {
  code: string
  retryable: boolean
  requestId: string | null
  retryAfter: number | null
  upstreamStatus: number | null
  constructor(code: string, retryable: boolean, details: { requestId?: string | null; retryAfter?: number | null; upstreamStatus?: number } = {}) {
    super(publicCode(code, requestMessages) ? requestMessages[code] : '充值服务暂未完成请求，请刷新订单核对后重试。', 503)
    this.code = code; this.retryable = retryable
    this.requestId = details.requestId ?? null; this.retryAfter = details.retryAfter ?? null; this.upstreamStatus = details.upstreamStatus ?? null
  }
}
function retryDelay(value: string | null) {
  if (!value) return null
  const delay = /^\d+$/.test(value) ? Number(value) : Math.ceil((Date.parse(value) - Date.now()) / 1000)
  return Number.isSafeInteger(delay) && delay >= 0 ? delay : null
}
function requestId(value: unknown) {
  return typeof value === 'string' && /^req_[A-Za-z0-9_-]{1,96}$/.test(value) ? value : null
}
export async function request<T>(env: Env, path: string, rawBody?: string, idempotency = ''): Promise<T> {
  const c = config(env)
  if (!/^\/[a-z0-9][A-Za-z0-9_/?=&%.-]*$/.test(path)) throw new Error('Invalid Recharge path')
  const url = new URL(c.base + path)
  const method = rawBody === undefined ? 'GET' : 'POST'
  if (method === 'POST' && (idempotency.length < 8 || idempotency.length > 128)) throw new Error('Invalid idempotency key')
  const timestamp = String(Math.floor(Date.now() / 1000)); const nonce = crypto.randomUUID()
  const headers: Record<string, string> = { 'X-Partner-Id': c.partner, 'X-Key-Id': c.key, 'X-Timestamp': timestamp, 'X-Nonce': nonce, 'X-Signature': await signature(c.secret, method, url, timestamp, nonce, c.key, idempotency, rawBody ?? ''), Accept: 'application/json' }
  if (rawBody !== undefined) { headers['Content-Type'] = 'application/json'; headers['Idempotency-Key'] = idempotency }
  let response: Response
  try { response = await fetch(url.href, { method, headers, body: rawBody, redirect: 'manual', signal: AbortSignal.timeout(10000) }) }
  catch { throw new RechargeError('transport_unknown', true) }
  if (response.status >= 300 && response.status < 400) throw new RechargeError('redirect_rejected', true)
  const details = { requestId: requestId(response.headers.get('X-Request-Id')), retryAfter: retryDelay(response.headers.get('Retry-After')), upstreamStatus: response.status }
  let result: { data?: T; error?: { code?: unknown; retryable?: unknown; request_id?: unknown } }
  try { result = JSON.parse(await readRaw(response, 256000)) } catch { throw new RechargeError('invalid_response', true, details) }
  if (!response.ok) throw new RechargeError(publicCode(result?.error?.code, requestMessages) ?? 'request_rejected', result?.error?.retryable === true || [429, 502, 503, 504].includes(response.status), { ...details, requestId: details.requestId ?? requestId(result?.error?.request_id) })
  if (!result || result.error || result.data == null) throw new RechargeError('invalid_response', true, details)
  return result.data
}
export async function readRaw(message: Request | Response, limit: number) {
  const reader = message.body?.getReader(); if (!reader) return ''
  const decoder = new TextDecoder('utf-8', { fatal: true }); let size = 0; let text = ''
  while (true) {
    const { done, value } = await reader.read(); if (done) break
    size += value.byteLength
    if (size > limit) { await reader.cancel(); throw new ApiError('请求或响应过长。', 413) }
    text += decoder.decode(value, { stream: true })
  }
  return text + decoder.decode()
}
export function paymentLink(env: Env, value: unknown) {
  if (value === null || value === undefined) return null
  if (typeof value !== 'string') throw new Error('Invalid payment link')
  const url = new URL(value)
  if (url.protocol !== 'https:' || url.origin !== new URL(config(env).base).origin || url.username || url.password || url.hash) throw new Error('Invalid payment link')
  return url.href
}
export function alipayQr(value: unknown) {
  if (value === null || value === undefined) return null
  if (typeof value !== 'string' || value.length > 512) throw new Error('Invalid Alipay QR')
  const url = new URL(value)
  if (url.origin !== 'https://qr.alipay.com' || url.username || url.password || url.search || url.hash || !/^\/[A-Za-z0-9]{8,128}$/.test(url.pathname)) throw new Error('Invalid Alipay QR')
  return url.href
}
type CashierStatus = { status: string; amount: string; expires_at: string; expired: boolean; can_start: boolean; qr_code: string | null; delivery_mode: string }
export function cashierLink(env: Env, link: string | null, orderId: string) {
  if (!link) return null
  const url = new URL(link)
  if (!url.pathname.startsWith('/payments')) return null
  if (url.pathname !== '/payments/' + orderId || url.origin !== new URL(config(env).base).origin || url.hash || url.username || url.password || url.searchParams.size !== 1 || url.searchParams.getAll('token').length !== 1 || !/^[A-Za-z0-9_-]{16,256}$/.test(url.searchParams.get('token') ?? '')) throw new Error('Supplier cashier mismatch')
  return url
}
// The platform's own /payments/:id page uses these endpoints for 当面付.
// Its signed order still returns the cashier URL, never the Alipay QR payload.
export async function paymentQr(env: Env, link: string | null, remote: RemoteOrder, deadline = Date.parse(remote.expires_at)) {
  // Keep historical payment-page contracts intact. Never turn a cashier URL into a QR.
  const url = cashierLink(env, link, remote.order_id)
  if (!url || deadline <= Date.now()) return null
  const read = async (action: 'status' | 'start') => {
    const endpoint = new URL(url); endpoint.pathname += '/' + action
    let response: Response
    try { response = await fetch(endpoint.href, { method: action === 'start' ? 'POST' : 'GET', headers: { Accept: 'application/json' }, redirect: 'manual', signal: AbortSignal.timeout(10000) }) }
    catch { throw new RechargeError('payment_transport_unknown', true) }
    if (!response.ok) throw new RechargeError('payment_request_rejected', response.status >= 500 || response.status === 429)
    let data: CashierStatus
    try { data = JSON.parse(await readRaw(response, 16000)) } catch { throw new RechargeError('invalid_payment_response', true) }
    if (!data || !['pending', 'paid', 'expired', 'closed', 'partially_refunded', 'refunded'].includes(data.status) || moneyCents(data.amount) !== moneyCents(remote.sale_amount) || data.delivery_mode !== remote.delivery_mode || typeof data.expired !== 'boolean' || typeof data.can_start !== 'boolean' || !Number.isFinite(Date.parse(data.expires_at)) || Date.parse(data.expires_at) !== Date.parse(remote.expires_at)) throw new Error('Supplier cashier quote mismatch')
    return data
  }
  // Recover a lost /start response by querying this same payment first.
  let data = await read('status')
  if (data.status !== 'pending' || data.expired || Math.min(deadline, Date.parse(data.expires_at)) <= Date.now()) return null
  if (!data.qr_code && data.can_start) data = await read('start')
  if (data.status !== 'pending' || data.expired || Math.min(deadline, Date.parse(data.expires_at)) <= Date.now()) return null
  return alipayQr(data.qr_code)
}
export type Product = { product_code: string; supply_price: string; max_sale_price: string; currency: string; max_quantity: number; available: boolean; delivery_modes: string[] }
export type RemoteOrder = { order_id: string; merchant_order_no: string; product_code: string; quantity: number; currency: string; collection_mode: string; payment_scope: string; delivery_mode: string; payment_channel?: string; sale_amount: string; supply_amount: string; merchant_margin: string; refunded_amount?: string; payment_status: string; fulfillment_status?: 'queued' | 'running' | 'succeeded' | 'failed' | 'cancelled' | null; qr_payload: string | null; expires_at: string }
export type Redemption = { redemption_id: string; order_id: string; status: 'queued' | 'running' | 'succeeded' | 'failed' | 'cancelled'; failure_code: string | null; error_category?: string | null; result_code?: string | null; fallback_recharge_available: boolean; account_email_masked: string | null; finished_at: string | null; progress_stage?: string | null; progress_version?: number; progress_updated_at?: string | null; attempt_no?: number; retry_allowed?: boolean; next_action?: 'wait' | 'resubmit' | 'none'; recovery_action?: 'retry' | 'refund' | null }
export type RemoteRefund = { refund_id: string; order_id: string; merchant_refund_no: string; amount: string; status: string }
export function moneyCents(value: unknown) {
  if (typeof value !== 'string' || !/^(0|[1-9]\d{0,7})\.\d{2}$/.test(value)) throw new Error('Invalid supplier money')
  const [whole, fraction] = value.split('.')
  return Number(whole) * 100 + Number(fraction)
}
// Only merchant margin may be negative after a platform refund.
export function marginCents(value: unknown) {
  if (typeof value === 'string' && value.startsWith('-')) return -moneyCents(value.slice(1))
  return moneyCents(value)
}
