import { ApiError, hash, type Env } from '../platform.ts'
import { type Order } from '../order-state.ts'
import { isXGiftPlan, xUsername, xgiftPlans } from '../../shared/xgift-products.ts'
import { configured, xgiftBackend, xgiftEligibility, xgiftProducts, xgiftBalance, xgiftCapabilities, createXGiftOrder, queryXGiftOrder, XGiftError, type XGiftOrder, type XGiftBackend } from './client.ts'
export interface Preflight { token: string; owner_hash: string; username: string; recipient_id: string; product_code: string; points: number; expires_at: number; supplier_backend: XGiftBackend }
interface Purchase { order_id: string; username: string; recipient_id: string; product_code: string; points: number; credential_hash: string; state: string; supplier_order: string | null; receipt: string | null; failure_code: string | null; lease_token: string | null; lease_until: number; attempted_at: number | null; next_check: number; supplier_backend: XGiftBackend; supplier_merchant_order_no: string | null; recovery_attempt: number }
export const xgiftCheckoutEnabled = (env: Env, backend: XGiftBackend = xgiftBackend(env)) => env.XGIFT_CHECKOUT_ENABLED === 'true' && configured(env, backend)
const credentials = (env: Env, backend: XGiftBackend) => hash(backend === 'platform'
  ? ['platform', env.XGIFT_PLATFORM_API_BASE ?? 'https://api.shop.example.invalid/v1', env.XGIFT_PLATFORM_KEY_ID, env.XGIFT_PLATFORM_CLIENT_SECRET].join('|')
  : [env.XGIFT_API_BASE ?? 'https://x-api.shop.example.invalid/v1', env.XGIFT_USER_ID, env.XGIFT_KEY_ID, env.XGIFT_CLIENT_SECRET].join('|'))
const find = (env: Env, id: string) => env.INTEREST_DB.prepare('SELECT * FROM xgift_purchases WHERE order_id=?').bind(id).first<Purchase>()
const eligibleOrder = "mode='live' AND is_test=0 AND payment_status='paid' AND refund_hold=0 AND refunded_cents=0 AND redemption_blocked=0 AND status IN ('session','processing')"
export async function preflightXGift(env: Env, owner: string, data: Record<string, unknown>) {
  const username = xUsername(data.username)
  if (!username || typeof data.plan !== 'string' || !isXGiftPlan(data.plan)) throw new ApiError('请填写正确的 X 用户名并选择套餐。')
  if (!xgiftCheckoutEnabled(env)) throw new ApiError('X 赠送暂未开放下单。', 503)
  const backend = xgiftBackend(env)
  const check = await xgiftEligibility(env, username, data.plan, backend)
  if (check.username !== username || typeof check.eligible !== 'boolean') throw new ApiError('赠送资格暂未确认，请稍后重试。', 503)
  if (!check.eligible) return { eligible: false, username, message: check.reason === 'user_not_found' ? '未找到该 X 账号，请检查用户名。' : '72小时内改过资料或头像不可充值' }
  if (!/^\d{1,25}$/.test(check.recipient_id ?? '')) throw new ApiError('无法确认接收账号身份。', 503)
  const [list, balance, capacity] = await Promise.all([xgiftProducts(env, backend), xgiftBalance(env, backend), xgiftCapabilities(env, backend)])
  const product = list.find(p => p.code === data.plan)
  if (!product?.enabled || !Number.isSafeInteger(product.points) || product.points !== xgiftPlans[data.plan].points || balance.available < product.points || capacity.accepts_orders !== true) throw new ApiError('赠送服务暂不可下单，请稍后再试。', 503)
  const token = crypto.randomUUID(), expires = Date.now() + 5 * 60000
  await env.INTEREST_DB.batch([
    env.INTEREST_DB.prepare('INSERT INTO xgift_preflights(token,owner_hash,username,recipient_id,product_code,points,expires_at) VALUES(?,?,?,?,?,?,?)').bind(token, owner, username, check.recipient_id!, product.code, product.points, expires),
    env.INTEREST_DB.prepare('INSERT INTO xgift_preflight_backends(token,supplier_backend) VALUES(?,?)').bind(token, backend),
  ])
  return { eligible: true, username, token, expires_at: expires, message: '当前可接收赠送，请核对用户名后继续付款。' }
}
export async function xgiftSnapshot(env: Env, owner: string, data: Record<string, unknown>): Promise<Preflight> {
  if (!xgiftCheckoutEnabled(env)) throw new ApiError('X 赠送暂未开放下单。', 503)
  const username = xUsername(data.username)
  const p = await env.INTEREST_DB.prepare("SELECT p.*,COALESCE(b.supplier_backend,'legacy') supplier_backend FROM xgift_preflights p LEFT JOIN xgift_preflight_backends b ON b.token=p.token WHERE p.token=? AND p.owner_hash=? AND p.username=? AND p.product_code=? AND p.expires_at>?")
    .bind(typeof data.preflightToken === 'string' ? data.preflightToken : '', owner, username, String(data.plan), Date.now()).first<Preflight>()
  if (!p || p.supplier_backend !== xgiftBackend(env)) throw new ApiError('请重新检测赠送资格；检测有效期为 5 分钟。', 409)
  return p
}
export async function xgiftSnapshotStatement(env: Env, id: string, p: Preflight) {
  return env.INTEREST_DB.prepare('INSERT INTO xgift_purchases(order_id,username,recipient_id,product_code,points,credential_hash,updated_at,supplier_backend) SELECT id,?,?,?,?,?,?,? FROM orders WHERE id=? ON CONFLICT DO NOTHING')
    .bind(p.username, p.recipient_id, p.product_code, p.points, await credentials(env, p.supplier_backend), Date.now(), p.supplier_backend, id)
}
export async function xgiftSummary(env: Env, order: Order) {
  if (!isXGiftPlan(order.plan)) return null
  const row = await find(env, order.id)
  return row ? { username: row.username, state: row.state, receipt: row.receipt, failureCode: row.failure_code, message: row.state === 'succeeded' ? 'X 赠送付款已完成，请前往 X 核对 Premium 到账。' : row.state === 'failed' ? '本次赠送未完成，请通过原订单处理售后。' : row.state === 'unknown' ? '结果正在核对，请勿重复下单。' : order.payment_status === 'paid' ? '赠送正在处理，请稍候。' : '付款确认后自动开始赠送。' } : null
}
function verifyResult(result: XGiftOrder, row: Purchase) {
  const expectedCurrency = row.supplier_backend === 'platform' ? 'CNY' : 'bdt'
  const expectedAmount = row.supplier_backend === 'platform' ? row.points : row.product_code === 'x-premium-3m' ? 30000 : 60000
  if (!result || result.merchant_order_no !== (row.supplier_merchant_order_no ?? row.order_id) || result.recipient !== row.username || result.product_code !== row.product_code || result.points !== row.points || result.currency !== expectedCurrency || result.amount_minor !== expectedAmount || !/^ord_[a-f0-9]{32}$/.test(result.id) || (row.supplier_order && row.supplier_order !== result.id) || !['queued','running','unknown','succeeded','failed'].includes(result.status)) throw new ApiError('赠送订单结果待核对。', 503)
  const receiptPattern = row.supplier_backend === 'platform' ? /^rcpt_[a-f0-9]{24}$/ : /^cs_live_[A-Za-z0-9]+$/
  if (result.status === 'succeeded' && !receiptPattern.test(result.receipt ?? '')) throw new ApiError('赠送付款凭证待核对。', 503)
}
export async function fulfillXGift(env: Env, id: string) {
  let row = await find(env, id)
  if (!row || ['failed','succeeded'].includes(row.state) || row.next_check > Date.now()) return
  if (!row.attempted_at && !xgiftCheckoutEnabled(env, row.supplier_backend)) return
  if (row.credential_hash !== await credentials(env, row.supplier_backend)) throw new ApiError('赠送接口凭据已变化，请核对原单。', 409)
  const lease = crypto.randomUUID(), now = Date.now()
  await env.INTEREST_DB.prepare(`UPDATE xgift_purchases SET lease_token=?,lease_until=? WHERE order_id=? AND lease_until<=? AND EXISTS(SELECT 1 FROM orders WHERE id=xgift_purchases.order_id AND ${eligibleOrder})`).bind(lease, now + 90000, id, now).run()
  row = (await find(env, id))!
  if (row.lease_token !== lease) return
  try {
    let result: XGiftOrder
    if (row.attempted_at === null) {
      const check = await xgiftEligibility(env, row.username, row.product_code, row.supplier_backend)
      if (!check.eligible || check.recipient_id !== row.recipient_id) {
        await finish(env, row, lease, 'failed', null, null, 'recipient_not_eligible'); return
      }
      await env.INTEREST_DB.prepare("UPDATE xgift_purchases SET state='creating',attempted_at=?,updated_at=? WHERE order_id=? AND lease_token=? AND attempted_at IS NULL").bind(now, now, id, lease).run()
      // This owned supplier persists idempotency for the lifetime of the order.
      result = await createXGiftOrder(env, row.supplier_merchant_order_no ?? id, row.product_code, row.username, row.recipient_id, row.points, row.supplier_backend)
    } else {
      try { result = await queryXGiftOrder(env, row.supplier_merchant_order_no ?? id, row.supplier_backend) }
      catch (error) {
        // Recover a request that never reached X API using the SAME immutable body and key.
        // Any other query failure is ambiguous and must not submit another request.
        if (row.supplier_order || !(error instanceof XGiftError) || error.status !== 404 || error.code !== (row.supplier_backend === 'platform' ? 'order_missing' : 'not_found') || !xgiftCheckoutEnabled(env, row.supplier_backend)) throw error
        result = await createXGiftOrder(env, row.supplier_merchant_order_no ?? id, row.product_code, row.username, row.recipient_id, row.points, row.supplier_backend)
      }
    }
    verifyResult(result, row)
    await finish(env, row, lease, result.status === 'succeeded' ? 'succeeded' : result.status === 'failed' ? 'failed' : result.status === 'unknown' ? 'unknown' : 'pending', result.id, result.receipt, result.failure_code)
  } catch (error) {
    // A signed business rejection only proves no order was accepted before an ID is known.
    if (!row.supplier_order && error instanceof XGiftError && ['not_eligible','recipient_changed','product_unavailable','insufficient_points','execution_disabled','product_disabled','price_changed','insufficient_balance','execution_paused','card_rule_disabled','card_rule_missing','card_missing','x_configuration_missing','billing_profile_missing','provider_missing','worker_unavailable'].includes(error.code)) {
      await finish(env, row, lease, 'failed', null, null, error.code)
      return
    }
    await env.INTEREST_DB.prepare("UPDATE xgift_purchases SET state=CASE WHEN attempted_at IS NOT NULL THEN 'unknown' ELSE state END,next_check=?,updated_at=? WHERE order_id=? AND lease_token=?").bind(Date.now() + 15000, Date.now(), id, lease).run()
  } finally { await env.INTEREST_DB.prepare('UPDATE xgift_purchases SET lease_until=0 WHERE order_id=? AND lease_token=?').bind(id, lease).run() }
}
async function finish(env: Env, row: Purchase, lease: string, state: string, supplier: string | null, receipt: string | null, failure: string | null) {
  const event = crypto.randomUUID(), now = Date.now(), status = state === 'succeeded' ? 'success' : state === 'failed' ? 'failed' : 'processing'
  await env.INTEREST_DB.batch([
    env.INTEREST_DB.prepare('UPDATE xgift_purchases SET state=?,supplier_order=COALESCE(supplier_order,?),receipt=?,failure_code=?,next_check=?,updated_at=? WHERE order_id=? AND lease_token=?').bind(state, supplier, receipt, failure, now + 5000, now, row.order_id, lease),
    env.INTEREST_DB.prepare(`UPDATE orders SET status=?,cost_cents=CASE WHEN partner_id IS NOT NULL THEN cost_cents ELSE ? END,updated_at=?,started_at=COALESCE(started_at,?),version=version+1,last_event_id=? WHERE id=? AND ${eligibleOrder} AND EXISTS(SELECT 1 FROM xgift_purchases x WHERE x.order_id=orders.id AND x.lease_token=? AND x.state=?) AND status<>?`).bind(status, state === 'succeeded' ? row.points : null, now, now, event, row.order_id, lease, state, status),
    env.INTEREST_DB.prepare("INSERT INTO order_events(id,order_id,actor,action,from_status,to_status,note,order_version,created_at) SELECT ?,id,'system','xgift-progress',NULL,status,?,version,? FROM orders WHERE id=? AND last_event_id=?").bind(event, state === 'succeeded' ? 'X 赠送结账付款已核验完成，请核对到账' : state === 'failed' ? '赠送未完成，请处理原订单售后' : '已收款，赠送处理中', now, row.order_id, event),
  ])
}
export async function reconcileXGift(env: Env) {
  await env.INTEREST_DB.prepare('DELETE FROM xgift_preflights WHERE expires_at<?').bind(Date.now()).run()
  const rows = (await env.INTEREST_DB.prepare("SELECT x.order_id FROM xgift_purchases x JOIN orders o ON o.id=x.order_id WHERE x.state NOT IN ('failed','succeeded') AND x.next_check<=? AND x.lease_until<=? AND o.payment_status='paid' ORDER BY x.updated_at LIMIT 5").bind(Date.now(), Date.now()).all<{ order_id: string }>()).results
  for (const row of rows) { try { await fulfillXGift(env, row.order_id) } catch { /* Isolate configuration failures per order. */ } }
}

// A new supplier attempt is allowed only for a definitive pre-payment rejection.
// Preserve the old supplier order and ledger; never reopen its terminal state.
export async function recoverXGift(env: Env, order: Order, note: string) {
  const row = await find(env, order.id)
  if (!row || row.supplier_backend !== 'platform' || row.state !== 'failed' || row.failure_code !== 'card_wallet_insufficient' || !row.supplier_order || row.receipt || row.lease_until > Date.now()
    || order.status !== 'failed' || order.mode !== 'live' || order.is_test || order.payment_status !== 'paid' || order.refunded_cents || order.refund_hold || order.redemption_blocked)
    throw new ApiError('该订单不能安全补发，请先核对原单付款和售后状态。', 409)
  if (!note.trim() || note.trim().length > 500) throw new ApiError('请填写 1–500 字的处理说明。')
  if (!xgiftCheckoutEnabled(env, row.supplier_backend) || row.credential_hash !== await credentials(env, row.supplier_backend)) throw new ApiError('赠送接口配置待核对。', 409)
  const merchant = row.supplier_merchant_order_no ?? row.order_id
  const original = await queryXGiftOrder(env, merchant, row.supplier_backend)
  verifyResult(original, row)
  if (original.status !== 'failed' || original.failure_code !== 'card_wallet_insufficient' || original.receipt) throw new ApiError('原供货订单尚未确认未付款，禁止补发。', 409)
  const attempt = row.recovery_attempt + 1, nextMerchant = `${row.order_id}:retry:${attempt}`
  try {
    await env.INTEREST_DB.prepare('INSERT INTO xgift_recovery_attempts(id,order_id,attempt,merchant_order_no,previous_merchant_order_no,previous_supplier_order,previous_failure_code,expected_order_version,actor,note,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)')
      .bind(crypto.randomUUID(), row.order_id, attempt, nextMerchant, merchant, row.supplier_order, row.failure_code, order.version, 'admin:' + env.ADMIN_USERNAME, note.trim(), Date.now()).run()
  } catch (error) {
    if (String(error).includes('xgift_recovery_conflict') || String(error).includes('UNIQUE constraint')) throw new ApiError('订单已更新，请刷新后重试。', 409)
    throw error
  }
  return { merchantOrderNo: nextMerchant, attempt }
}
