import { ApiError, hash, type Env } from '../platform.ts'
import { boundedResponseText } from '../bounded-response.ts'

export const mappings = {
  'gemini-pro-18m': { id: 'DEMO_PRODUCT_A', maxUsdCents: 60 },
  'duolingo-12m': { id: 'DEMO_PRODUCT_B', maxUsdCents: 38 },
} as const
export const isDeliverySupplierPlan = (plan: string): plan is keyof typeof mappings => Object.hasOwn(mappings, plan)
export type Product = { service_id: string; name: string; price: number; stock: number; availability_state?: string }
export type SupplierOrder = { order_id?: string; order_status?: string; service_id?: string; requested_quantity?: number; delivered_quantity?: number; charged_amount?: number; refunded_amount?: number; net_charged_amount?: number; partial_delivery?: boolean; delivered_products?: string[]; products?: string[]; retry_after_seconds?: number }
export const USD_MICROS_PER_CENT = 10_000
export function usdMicros(value: unknown) {
  // Compare supplier prices, balances and receipts in integer micro-USDT.
  // Never round to cents before checking a ceiling, refund or net charge.
  if (typeof value !== 'number' || !Number.isFinite(value) || value < 0 || !Number.isSafeInteger(Math.round(value * 1_000_000)) || Math.abs(value * 1_000_000 - Math.round(value * 1_000_000)) > 0.000001) throw new ApiError('供应商金额格式异常。', 502)
  return Math.round(value * 1_000_000)
}
export function rate(env: Env) {
  const value = Number(env.DELIVERY_SUPPLIER_CNY_PER_USD)
  if (!Number.isFinite(value) || value <= 0 || value > 100) throw new ApiError('采购记账汇率未配置。', 503)
  return value
}
export const credentialHash = (env: Env) => hash(env.DELIVERY_SUPPLIER_API_KEY ?? '')
export function purchaseReceipt(id: string) {
  if (!/^[A-Za-z0-9_-]{1,124}$/.test(id)) throw new ApiError('采购编号格式无效。', 409)
  return 'ymx_' + id
}
export class DeliverySupplierReceiptRejected extends ApiError {
  constructor() { super('供应商拒绝了采购编号，采购未发生，请核对后恢复。', 409) }
}
export async function call<T>(env: Env, path: string, receipt?: string, payload?: string): Promise<T> {
  if (!env.DELIVERY_SUPPLIER_API_KEY) throw new ApiError('自动发货供应商尚未配置。', 503)
  try {
    const response = await fetch('https://delivery.example.invalid/api/v1' + path, {
      method: payload ? 'POST' : 'GET', redirect: 'manual',
      headers: { 'X-API-Key': env.DELIVERY_SUPPLIER_API_KEY, Accept: 'application/json', ...(payload ? { 'Content-Type': 'application/json', 'Idempotency-Key': receipt! } : {}) },
      body: payload, signal: AbortSignal.timeout(15000),
    })
    if (response.status >= 300 && response.status < 400) throw new Error('Redirect refused')
    const data = JSON.parse(await boundedResponseText(response)) as Record<string, unknown>
    if (!data || typeof data !== 'object' || Array.isArray(data)) throw new Error('Invalid supplier response')
    // Only this explicit validation failure proves that no purchase was accepted.
    // A response containing an order identity still belongs to the original purchase.
    if (path === '/order' && payload && response.status === 400 && data.error_code === 'INVALID_IDEMPOTENCY_KEY' && data.order_id === undefined && data.order === undefined) throw new DeliverySupplierReceiptRejected()
    // A 503/409 may contain a paid pending/refunded order; preserve its identity.
    if (!response.ok && !(typeof data.order_id === 'string' && typeof data.order_status === 'string')) throw new Error('Supplier unavailable')
    return data as T
  } catch (error) {
    if (error instanceof DeliverySupplierReceiptRejected) throw error
    throw new ApiError('供应商结果暂未确认，系统会继续核对原采购单。', 503)
  }
}
export async function products(env: Env) {
  const data = await call<{ services: Product[] }>(env, '/products')
  if (!Array.isArray(data.services)) throw new ApiError('供应商商品列表异常。', 502)
  return data.services
}
export function validateProduct(list: Product[], serviceId: string, ceiling: number) {
  const p = list.find(p => p.service_id === serviceId)
  if (!p || typeof p.name !== 'string' || !Number.isInteger(p.stock) || p.stock < 1 || (p.availability_state && p.availability_state !== 'available') || usdMicros(p.price) <= 0 || usdMicros(p.price) > ceiling * USD_MICROS_PER_CENT) throw new ApiError('商品暂时缺货或采购价格已变化，请稍后再试。', 409)
  return p
}
