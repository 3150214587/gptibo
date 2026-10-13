import { categoryMessages, failureMessages, publicCode, resultStages } from './errors.ts'
import { credentialExpiryProblem } from '../../shared/credential-expiry.ts'
import { ApiError, body, hash, json, type Env, type Statement } from '../platform.ts'
import { change, getOrder, type Order } from '../order-state.ts'
import { amountText } from '../payments/types.ts'
import { PAYMENT_WINDOW_MS, paymentDeadline } from '../../shared/payment-window.ts'
import { publicAlipayQr } from '../../shared/public-payment.ts'
import { rechargeStage, type RechargeStage } from '../../shared/recharge-progress.ts'
import { cashierLink, checkoutEnabled, config, notifyUrl, paymentLink, paymentQr, productMap, readRaw, request, hmac, marginCents, moneyCents as amountCents, RechargeError, type Product, type RemoteOrder, type Redemption, type RemoteRefund } from './client.ts'

type SupplierOrder = { order_id: string; account: string; product_code: string; collection_mode: string; create_body: string; remote_id: string | null; payment_status: string; payment_url: string | null; payment_qr: string | null; expires_at: number | null; attempted_at: number | null; updated_at: number; lease_token: string | null; lease_until: number; supply_cents: number | null; merchant_margin_cents: number | null; refunded_cents: number; financial_hold: number; notify_version: number; checked_notify_version: number }
type Attempt = { id: string; order_id: string; attempt_no: number; request_hash: string; credential_mode: 'session' | 'access_token'; remote_id: string | null; status: string; fallback: number; failure_code: string | null; error_category: string | null; result_code: string | null; account_email_masked: string | null; finished_at: number | null; created_at: number; progress_stage: RechargeStage | null; progress_version: number; progress_updated_at: number | null }
export const findRechargeOrder = (env: Env, id: string) => env.INTEREST_DB.prepare('SELECT * FROM recharge_orders WHERE order_id=?').bind(id).first<SupplierOrder>()
export async function expireRechargeOrder(env: Env, order: Order) {
  if (order.mode !== 'live' || order.is_test || order.status !== 'pending' || order.payment_status !== 'unpaid') return order
  const row = await findRechargeOrder(env, order.id)
  if (!row || row.payment_status !== 'pending' || paymentDeadline(order.created_at, row.expires_at) > Date.now()) return order
  try { order = await change(env, order, { status: 'cancelled' }, 'system', 'payment-expired', '订单付款有效期已结束，停止收款入口，继续核对已发生的付款') }
  catch (error) { if (!(error instanceof ApiError && error.status === 409)) throw error; order = await getOrder(env, order.id) }
  await env.INTEREST_DB.prepare("UPDATE recharge_orders SET payment_qr=NULL WHERE order_id=? AND EXISTS(SELECT 1 FROM orders WHERE id=recharge_orders.order_id AND status='cancelled' AND payment_status='unpaid')").bind(order.id).run()
  return order
}
const latestAttempt = (env: Env, id: string) => env.INTEREST_DB.prepare('SELECT * FROM recharge_redemptions WHERE order_id=? ORDER BY attempt_no DESC LIMIT 1').bind(id).first<Attempt>()
const terminal = (status: string) => ['succeeded', 'failed', 'cancelled', 'rejected'].includes(status)
const unknownExpired = (attempt: Attempt) => attempt.status === 'unknown' && Date.now() - attempt.created_at >= 7 * 86400000
function accountFor(env: Env, row: SupplierOrder) {
  if (row.account !== config(env).account) throw new ApiError('充值服务账号已变化，请恢复订单原账号后处理。', 503)
}
export async function supplierProducts(env: Env) {
  const data = await request<Product[]>(env, '/products')
  if (!Array.isArray(data)) throw new Error('Invalid products')
  return data
}
function eligible(product: Product | undefined, price: number): product is Product {
  return !!product && product.available === true && product.currency === 'CNY' && product.max_quantity >= 1 && Array.isArray(product.delivery_modes) && product.delivery_modes.includes('auto_recharge') && price >= amountCents(product.supply_price) && price <= amountCents(product.max_sale_price)
}
export async function supplierCatalog<T extends { id: string; price_cents: number; enabled: number }>(env: Env, plans: T[], partnerId?: string) {
  const products = await supplierProducts(env); const map = productMap(env)
  const costs = partnerId ? (await env.INTEREST_DB.prepare('SELECT plan,cost_cents FROM partner_prices WHERE partner_id=?').bind(partnerId).all<{ plan: string; cost_cents: number }>()).results : []
  return plans.map(plan => {
    const product = products.find(p => p.product_code === map[plan.id]); const cost = costs.find(c => c.plan === plan.id)?.cost_cents
    return { ...plan, enabled: plan.enabled && eligible(product, plan.price_cents) && (!partnerId || (cost !== undefined && cost >= amountCents(product.supply_price))) ? 1 : 0 }
  })
}
// Only authorized, live orders get this immutable snapshot. No network write here.
export async function supplierSnapshot(env: Env, plan: string, price: number, partnerCost?: number) {
  if (!checkoutEnabled(env)) throw new ApiError('真实购买尚未开放。', 409)
  if (env.RECHARGE_COLLECTION_MODE && env.RECHARGE_COLLECTION_MODE !== 'platform_collect') throw new ApiError('当前接入仅支持平台代收。', 503)
  const product = (await supplierProducts(env)).find(p => p.product_code === productMap(env)[plan])
  if (!eligible(product, price)) throw new ApiError('该套餐暂不可售或价格已更新，请刷新商品。', 409)
  if (partnerCost !== undefined && partnerCost < amountCents(product.supply_price)) throw new ApiError('该代理套餐供货价需重新核对，请联系平台支持。', 409)
  const methods = await request<{ code: string }[]>(env, '/payment-methods')
  if (!Array.isArray(methods) || !methods.some(m => m.code === 'alipay')) throw new ApiError('支付宝通道暂不可用，请稍后重试。', 409)
  return { account: config(env).account, product: product.product_code, notify: notifyUrl(env) }
}
export function snapshotStatement(env: Env, id: string, snapshot: Awaited<ReturnType<typeof supplierSnapshot>>, price: number): Statement {
  const raw = JSON.stringify({ merchant_order_no: id, product_code: snapshot.product, quantity: 1, sale_amount: amountText(price), collection_mode: 'platform_collect', delivery_mode: 'auto_recharge', payment_channel: 'alipay', notify_url: snapshot.notify })
  return env.INTEREST_DB.prepare("INSERT INTO recharge_orders(order_id,account,product_code,collection_mode,create_body,updated_at) SELECT id,?,?,'platform_collect',?,? FROM orders WHERE id=? AND mode='live' ON CONFLICT(order_id) DO NOTHING").bind(snapshot.account, snapshot.product, raw, Date.now(), id)
}
async function locked<T>(env: Env, row: SupplierOrder, run: (fresh: SupplierOrder) => Promise<T>) {
  accountFor(env, row)
  const token = crypto.randomUUID(); const now = Date.now()
  await env.INTEREST_DB.prepare('UPDATE recharge_orders SET lease_token=?,lease_until=? WHERE order_id=? AND lease_until<=?').bind(token, now + 45000, row.order_id, now).run()
  const fresh = await findRechargeOrder(env, row.order_id)
  if (fresh?.lease_token !== token) throw new ApiError('订单正在同步，请稍后刷新。', 409)
  try { return await run(fresh) }
  finally { await env.INTEREST_DB.prepare('UPDATE recharge_orders SET lease_token=NULL,lease_until=0,updated_at=CASE WHEN notify_version>checked_notify_version THEN 0 ELSE ? END WHERE order_id=? AND lease_token=?').bind(Date.now(), row.order_id, token).run() }
}
function validateOrder(row: SupplierOrder, order: Order, remote: RemoteOrder) {
  if (!remote || typeof remote.order_id !== 'string' || !/^[A-Za-z0-9_-]{1,100}$/.test(remote.order_id) || (row.remote_id && row.remote_id !== remote.order_id) || remote.merchant_order_no !== order.id || remote.product_code !== row.product_code || remote.quantity !== 1 || remote.currency !== 'CNY' || remote.collection_mode !== 'platform_collect' || remote.payment_scope !== 'retail' || remote.delivery_mode !== 'auto_recharge' || amountCents(remote.sale_amount) !== order.price_cents || !['pending', 'paid', 'expired', 'closed', 'partially_refunded', 'refunded'].includes(remote.payment_status)) throw new Error('Supplier order mismatch')
  if (JSON.parse(row.create_body).payment_channel !== 'alipay' || (remote.payment_channel !== undefined && remote.payment_channel !== 'alipay')) throw new Error('Supplier payment channel mismatch')
}
async function applyRemoteOrder(env: Env, row: SupplierOrder, remote: RemoteOrder, refreshCashier = true) {
  const order = await getOrder(env, row.order_id); validateOrder(row, order, remote)
  const expires = Date.parse(remote.expires_at)
  if (!Number.isFinite(expires)) throw new Error('Invalid expiry')
  let paymentUrl: string | null = null; let linkError: unknown
  try { if (remote.payment_status === 'pending') { const link = paymentLink(env, remote.qr_payload); cashierLink(env, link, remote.order_id); paymentUrl = link } }
  catch (error) { linkError = error }
  const supply = amountCents(remote.supply_amount); const margin = marginCents(remote.merchant_margin); const refunded = amountCents(remote.refunded_amount ?? '0.00')
  if (supply > order.price_cents || margin < -supply || margin > order.price_cents - refunded || refunded > order.price_cents || (['pending','closed','expired','paid'].includes(remote.payment_status) && refunded > 0) || (remote.payment_status === 'refunded' && refunded !== order.price_cents) || (remote.payment_status === 'partially_refunded' && (refunded === 0 || refunded === order.price_cents))) throw new Error('Supplier financial mismatch')
  const paid = ['paid', 'partially_refunded', 'refunded'].includes(remote.payment_status)
  const closed = ['closed', 'expired'].includes(remote.payment_status)
  const deadline = paymentDeadline(order.created_at, expires)
  const payable = remote.payment_status === 'pending' && deadline > Date.now() && order.status === 'pending' && order.payment_status === 'unpaid' && !order.redemption_blocked && !order.refund_hold && !order.refunded_cents
  const event = crypto.randomUUID(); const now = Date.now()
  await env.INTEREST_DB.batch([
    env.INTEREST_DB.prepare("UPDATE recharge_orders SET remote_id=?,payment_status=?,payment_qr=CASE WHEN ?=1 AND payment_url=? AND expires_at=? THEN payment_qr ELSE NULL END,payment_url=?,expires_at=? WHERE order_id=? AND (remote_id IS NULL OR remote_id=?) AND (payment_status='pending' OR (payment_status IN ('closed','expired') AND ? IN ('paid','partially_refunded','refunded')) OR (payment_status='paid' AND ? IN ('partially_refunded','refunded')) OR (payment_status='partially_refunded' AND ?='refunded') OR payment_status=?)")
      .bind(remote.order_id, remote.payment_status, Number(payable), paymentUrl, expires, remote.payment_status === 'pending' ? paymentUrl : null, expires, row.order_id, remote.order_id, remote.payment_status, remote.payment_status, remote.payment_status, remote.payment_status),
    env.INTEREST_DB.prepare("UPDATE recharge_orders SET supply_cents=?,merchant_margin_cents=?,reported_margin_cents=?,refunded_cents=?,verified_at=?,financial_hold=CASE WHEN ?=(SELECT refunded_cents FROM orders WHERE id=?) THEN 0 ELSE 1 END WHERE order_id=? AND refunded_cents<=? AND payment_status=?")
      .bind(supply, Math.max(0, margin), margin, refunded, now, refunded, order.id, order.id, refunded, remote.payment_status),
    env.INTEREST_DB.prepare("UPDATE orders SET payment_status=CASE WHEN ?=1 THEN 'paid' ELSE payment_status END,status=CASE WHEN ?=1 AND status IN ('pending','cancelled') THEN 'session' WHEN ?=1 AND status='pending' THEN 'cancelled' ELSE status END,updated_at=?,version=version+1,last_event_id=? WHERE id=? AND mode='live' AND payment_status='unpaid' AND ?=1 AND EXISTS(SELECT 1 FROM recharge_orders WHERE order_id=orders.id AND remote_id=?)")
      .bind(Number(paid), Number(paid), Number(closed), now, event, row.order_id, Number(paid || closed), remote.order_id),
    env.INTEREST_DB.prepare("INSERT INTO order_events(id,order_id,actor,action,from_status,to_status,note,order_version,created_at) SELECT ?,id,'system',?,?,status,?,version,? FROM orders WHERE id=? AND last_event_id=?")
      .bind(event, paid ? 'payment-confirmed' : 'payment-closed', order.status, paid ? '支付渠道已核验收款，等待提交充值资料' : '支付渠道已确认订单关闭', now, row.order_id, event),
  ])
  if (linkError) throw linkError
  // Manual upstream fulfillment can complete the order while its historical
  // automatic attempts remain failed. Keep those attempts and the no-resubmit
  // marker intact; only the validated original order can confirm this success.
  if (remote.fulfillment_status === 'succeeded' && remote.payment_status === 'paid') {
    const current = await getOrder(env, row.order_id)
    const completion = crypto.randomUUID()
    await env.INTEREST_DB.batch([
      env.INTEREST_DB.prepare("UPDATE orders SET status='success',updated_at=?,version=version+1,last_event_id=? WHERE id=? AND version=? AND mode='live' AND is_test=0 AND payment_status='paid' AND status<>'success' AND refund_hold=0 AND refunded_cents=0 AND EXISTS(SELECT 1 FROM recharge_orders t WHERE t.order_id=orders.id AND t.remote_id=? AND t.payment_status='paid' AND t.financial_hold=0 AND t.refunded_cents=0)")
        .bind(now, completion, current.id, current.version, remote.order_id),
      env.INTEREST_DB.prepare("INSERT INTO order_events(id,order_id,actor,action,from_status,to_status,note,order_version,created_at) SELECT ?,id,'system','platform-fulfillment-completed',?,'success','平台原订单已核验充值完成',version,? FROM orders WHERE id=? AND last_event_id=?")
        .bind(completion, current.status, now, current.id, completion),
    ])
  }
  // Commit the signed order identity before the optional cashier bridge. A QR
  // timeout must never force a second supplier create or erase verified payment.
  if (payable && refreshCashier) {
    let qr: string | null
    try { qr = await paymentQr(env, paymentUrl, remote, deadline) }
    catch (error) {
      if (!(error instanceof RechargeError) || error.code === 'invalid_payment_response') await env.INTEREST_DB.prepare('UPDATE recharge_orders SET payment_qr=NULL WHERE order_id=? AND remote_id=?').bind(row.order_id, remote.order_id).run()
      throw error
    }
    await env.INTEREST_DB.prepare("UPDATE recharge_orders SET payment_qr=? WHERE order_id=? AND remote_id=? AND payment_status='pending' AND expires_at>? AND EXISTS(SELECT 1 FROM orders WHERE id=recharge_orders.order_id AND status='pending' AND payment_status='unpaid' AND redemption_blocked=0 AND refund_hold=0 AND refunded_cents=0 AND created_at>?)").bind(qr, row.order_id, remote.order_id, Date.now(), Date.now() - PAYMENT_WINDOW_MS).run()
  }
}
async function ensureRemote(env: Env, row: SupplierOrder) {
  if (row.remote_id) return row
  const order = await expireRechargeOrder(env, await getOrder(env, row.order_id))
  // Recover only an already attempted idempotent write after timeout; no new payment entrance.
  const recovering = !!row.attempted_at && order.status === 'cancelled' && paymentDeadline(order.created_at, row.expires_at) <= Date.now()
  if (order.mode !== 'live' || order.redemption_blocked || order.refund_hold || order.refunded_cents || (order.status !== 'pending' && !recovering)) throw new ApiError('当前订单不能创建支付。', 409)
  // Existing attempted writes may be recovered after the new-sales switch is disabled.
  if (!row.attempted_at && !checkoutEnabled(env)) throw new ApiError('真实购买尚未开放。', 409)
  await env.INTEREST_DB.prepare('UPDATE recharge_orders SET attempted_at=COALESCE(attempted_at,?) WHERE order_id=?').bind(Date.now(), row.order_id).run()
  const remote = await request<RemoteOrder>(env, '/orders', row.create_body, 'order:' + row.order_id)
  await applyRemoteOrder(env, row, remote)
  return (await findRechargeOrder(env, row.order_id))!
}
function failureMessage(failure: string | null, category: string | null) {
  return failure && failure !== 'other' && Object.hasOwn(failureMessages, failure) ? failureMessages[failure] : category && Object.hasOwn(categoryMessages, category) ? categoryMessages[category] : failureMessages.other
}
async function applyRedemption(env: Env, row: SupplierOrder, attempt: Attempt, remote: Redemption) {
  if (!remote || !/^[A-Za-z0-9_-]{1,128}$/.test(remote.redemption_id) || remote.order_id !== row.remote_id || (attempt.remote_id && attempt.remote_id !== remote.redemption_id) || !['queued', 'running', 'succeeded', 'failed', 'cancelled'].includes(remote.status) || typeof remote.fallback_recharge_available !== 'boolean') throw new Error('Redemption mismatch')
  if (remote.attempt_no !== undefined && remote.attempt_no !== attempt.attempt_no) throw new Error('Redemption attempt mismatch')
  const version = remote.progress_version ?? 0
  if (!Number.isSafeInteger(version) || version < 0) throw new Error('Invalid progress version')
  // Ignore older snapshots of this attempt, including their status and recovery flags.
  if (version < attempt.progress_version) return
  const resultCode = publicCode(remote.result_code, resultStages) as keyof typeof resultStages | null
  const resultStage = resultCode ? resultStages[resultCode] : null
  // Contradictory terminal claims must not release a new attempt or mark success.
  if (resultStage && ((remote.status === 'succeeded' && resultStage !== 'completed') || (['failed', 'cancelled'].includes(remote.status) && !['failed', 'cancelled'].includes(resultStage)))) throw new Error('Redemption result mismatch')
  const stage = rechargeStage(remote.progress_stage) ?? (['queued', 'running'].includes(remote.status) && resultStage && ['completed', 'failed', 'cancelled'].includes(resultStage) ? null : resultStage)
  const category = publicCode(remote.error_category, categoryMessages)
  const progressTime = remote.progress_updated_at == null ? null : Date.parse(remote.progress_updated_at)
  if (progressTime !== null && !Number.isFinite(progressTime)) throw new Error('Invalid progress time')
  const acceptProgress = version > attempt.progress_version || (version === 0 && (attempt.progress_stage === null || remote.progress_version === undefined))
  const publicStage = acceptProgress ? stage : attempt.progress_stage
  const publicTime = acceptProgress ? progressTime : attempt.progress_updated_at
  const event = crypto.randomUUID(); const now = Date.now(); const order = await getOrder(env, row.order_id)
  const status = remote.status === 'succeeded' ? 'success' : ['failed', 'cancelled'].includes(remote.status) ? 'failed' : 'processing'
  const finished = remote.finished_at === null ? null : Date.parse(remote.finished_at)
  if (finished !== null && !Number.isFinite(finished)) throw new Error('Invalid completion time')
  const failure = publicCode(remote.failure_code, failureMessages) ?? (remote.failure_code == null ? null : 'other')
  const masked = typeof remote.account_email_masked === 'string' && /^[^@\s]*\*[^@\s]*@[^@\s]+$/.test(remote.account_email_masked) && remote.account_email_masked.length <= 320 ? remote.account_email_masked : null
  await env.INTEREST_DB.batch([
    env.INTEREST_DB.prepare("UPDATE recharge_redemptions SET remote_id=?,status=?,fallback=?,failure_code=?,error_category=?,result_code=?,account_email_masked=?,finished_at=?,progress_stage=?,progress_version=?,progress_updated_at=? WHERE id=? AND progress_version<=? AND (status IN ('unknown','queued','running') OR status=? OR (status IN ('failed','cancelled') AND ?='succeeded' AND progress_version<?))")
      .bind(remote.redemption_id, remote.status, Number(remote.fallback_recharge_available && remote.retry_allowed === true && remote.next_action === 'resubmit' && remote.recovery_action !== 'refund' && ['failed', 'cancelled'].includes(remote.status)), failure, category, resultCode, masked, finished, publicStage, version, publicTime, attempt.id, version, remote.status, remote.status, version),
    env.INTEREST_DB.prepare("UPDATE orders SET status=?,started_at=COALESCE(started_at,?),updated_at=?,version=version+1,last_event_id=? WHERE id=? AND mode='live' AND payment_status='paid' AND redemption_blocked=0 AND refund_hold=0 AND refunded_cents<price_cents AND status<>? AND status<>'success' AND EXISTS(SELECT 1 FROM recharge_redemptions a WHERE a.id=? AND a.status=? AND a.attempt_no=(SELECT MAX(attempt_no) FROM recharge_redemptions WHERE order_id=orders.id))")
      .bind(status, now, now, event, row.order_id, status, attempt.id, remote.status),
    env.INTEREST_DB.prepare("INSERT INTO order_events(id,order_id,actor,action,from_status,to_status,note,order_version,created_at) SELECT ?,id,'system','recharge-status',?,status,?,version,? FROM orders WHERE id=? AND last_event_id=?")
      .bind(event, order.status, status === 'success' ? '充值服务已确认完成' : status === 'failed' ? failureMessage(failure, category) : '充值任务已受理，等待完成', now, row.order_id, event),
  ])
}
async function syncUnlocked(env: Env, row: SupplierOrder) {
  const current = await expireRechargeOrder(env, await getOrder(env, row.order_id))
  if (!row.remote_id && !row.attempted_at && current.status === 'cancelled') return current
  const ready = await ensureRemote(env, row)
  const attempt = await latestAttempt(env, row.order_id)
  const order = await getOrder(env, row.order_id)
  const needsTerminal = attempt && terminal(attempt.status) && order.status !== (attempt.status === 'succeeded' ? 'success' : 'failed') && !order.redemption_blocked && !order.refund_hold && order.refunded_cents < order.price_cents
  const [remoteOrder, redemption] = await Promise.all([
    row.remote_id ? request<RemoteOrder>(env, '/orders/' + ready.remote_id) : Promise.resolve(null),
    attempt?.remote_id && (!terminal(attempt.status) || needsTerminal || ['failed', 'cancelled'].includes(attempt.status) || row.notify_version > row.checked_notify_version) ? request<Redemption>(env, '/redemptions/' + attempt.remote_id) : Promise.resolve(null),
  ])
  if (remoteOrder) await applyRemoteOrder(env, ready, remoteOrder)
  if (attempt && redemption) await applyRedemption(env, ready, attempt, redemption)
  await env.INTEREST_DB.prepare('UPDATE recharge_orders SET checked_notify_version=MAX(checked_notify_version,?) WHERE order_id=? AND lease_token=?').bind(row.notify_version, row.order_id, row.lease_token).run()
  return expireRechargeOrder(env, await getOrder(env, row.order_id))
}
export async function syncRechargeOrder(env: Env, order: Order, force = false) {
  if (order.mode !== 'live' || order.is_test) return order
  order = await expireRechargeOrder(env, order)
  const row = await findRechargeOrder(env, order.id)
  if (!row || (!force && (row.lease_until > Date.now() || Date.now() - row.updated_at < 3000))) return order
  // Idle input steps have no new task to poll. Avoid competing with a customer's
  // credential submission for the same lease; manual refresh/Cron still query.
  if (!force && row.remote_id && row.updated_at !== 0 && order.payment_status === 'paid') {
    if (['session', 'failed'].includes(order.status)) return order
    if (order.status === 'processing' && !(await latestAttempt(env, order.id))?.remote_id) return order
  }
  return locked(env, row, fresh => syncUnlocked(env, fresh))
}
// Operator queries only an existing original order. Never create a payment,
// start a cashier, submit credentials or execute a refund from this endpoint.
export async function queryPlatformOrder(env: Env, order: Order) {
  const row = await findRechargeOrder(env, order.id)
  if (order.mode !== 'live' || order.is_test || !row?.remote_id) throw new ApiError('此订单没有可查询的平台订单。', 409)
  await locked(env, row, async fresh => {
    const remote = await request<RemoteOrder>(env, '/orders/' + fresh.remote_id)
    await applyRemoteOrder(env, fresh, remote, false)
    const attempt = await latestAttempt(env, order.id)
    if (remote.fulfillment_status !== 'succeeded' && attempt?.remote_id) {
      await applyRedemption(env, fresh, attempt, await request<Redemption>(env, '/redemptions/' + attempt.remote_id))
    }
    // Leave notifications due: they may also require a recharge-task query.
  })
  return getOrder(env, order.id)
}
export async function platformOrderStatus(env: Env, order: Order) {
  if (order.mode !== 'live' || order.is_test) return null
  const row = await env.INTEREST_DB.prepare('SELECT remote_id,payment_status,refunded_cents,financial_hold,verified_at FROM recharge_orders WHERE order_id=?').bind(order.id).first<{ remote_id: string | null; payment_status: string; refunded_cents: number; financial_hold: number; verified_at: number | null }>()
  if (!row?.remote_id) return null
  return { remoteId: row.remote_id, paymentStatus: row.payment_status, refundedCents: row.refunded_cents, checkedAt: row.verified_at, financialHold: !!row.financial_hold }
}
export async function rechargeSummary(env: Env, order: Order) {
  if (order.mode !== 'live' || order.is_test) return undefined
  const row = await findRechargeOrder(env, order.id); if (!row) return undefined
  const historicalAttempt = await latestAttempt(env, order.id)
  // Do not expose a failed historical automatic task as the result of a
  // subsequently completed manual fulfillment, or invent its completion time.
  const completed = order.status === 'success'
  const attempt = completed && historicalAttempt?.status !== 'succeeded' ? null : historicalAttempt
  const available = !order.redemption_blocked && !order.refund_hold && !order.refunded_cents && !row.financial_hold && row.payment_status === 'paid' && order.payment_status === 'paid'
  const expiresAt = paymentDeadline(order.created_at, row.expires_at)
  const paymentExpired = order.payment_status === 'unpaid' && (row.payment_status === 'expired' || (row.payment_status === 'pending' && expiresAt <= Date.now()))
  const payable = order.status === 'pending' && order.payment_status === 'unpaid' && row.payment_status === 'pending' && !order.redemption_blocked && !order.refund_hold && !order.refunded_cents && expiresAt > Date.now()
  // Never publish the supplier cashier URL/token, including legacy link orders.
  const paymentKind = payable ? 'qr' : null
  const publicQr = payable ? publicAlipayQr(row.payment_qr) : null
  return { financialHold: !!row.financial_hold && !!row.remote_id && order.payment_status === 'paid', paymentUrl: null, paymentQr: publicQr, paymentKind, expiresAt, paymentExpired, rechargeStatus: completed ? 'succeeded' : attempt?.status ?? null, failureCode: attempt?.failure_code ?? null, errorCategory: attempt?.error_category ?? null, resultCode: attempt?.result_code ?? null, progressStage: completed ? 'completed' : attempt?.progress_stage ?? null, progressVersion: attempt?.progress_version ?? 0, progressUpdatedAt: attempt?.progress_updated_at ?? null, attemptNo: attempt?.attempt_no ?? null, retryKey: attempt?.status === 'unknown' ? attempt.id : null, retryMode: attempt?.status === 'unknown' ? attempt.credential_mode : null, canSubmit: available && order.status !== 'success' && (!attempt || (attempt.status === 'unknown' && !unknownExpired(attempt)) || attempt.status === 'rejected' || !!attempt.fallback), message: row.financial_hold && order.payment_status === 'paid' ? '平台退款金额正在核对，充值与代理收益暂时冻结，请联系客服。' : attempt && unknownExpired(attempt) ? '提交结果长期未确认，请联系客服核对原任务，不要重新付款或提交新任务。' : attempt && (attempt.failure_code || (terminal(attempt.status) && attempt.status !== 'succeeded')) ? failureMessage(attempt.failure_code, attempt.error_category) : null, accountEmailMasked: attempt?.account_email_masked ?? null, finishedAt: attempt?.finished_at ?? null }
}
export async function rechargeAction(requestMessage: Request, env: Env, order: Order) {
  if (requestMessage.method !== 'POST') throw new ApiError('请求方式不支持。', 405)
  const data = await body(requestMessage, 40000)
  const row = await findRechargeOrder(env, order.id)
  if (order.mode !== 'live' || !row) throw new ApiError('此订单没有充值通道。', 409)
  if (!(await env.INTEREST_LIMITER.limit({ key: 'recharge:' + order.owner_hash })).success) throw new ApiError('操作较频繁，请稍后重试。', 429)
  if (data.action === 'query') { await locked(env, row, fresh => syncUnlocked(env, fresh)); return }
  if (data.action !== 'submit' || typeof data.key !== 'string' || !/^[a-f0-9-]{36}$/.test(data.key) || data.customerConfirmedEmail !== true) throw new ApiError('请确认账号并重新提交。')
  const credential = data.credential as Record<string, unknown> | undefined
  let normalized: { mode: string; session?: string; access_token?: string }
  if (credential?.mode === 'session' && typeof credential.session === 'string' && credential.session.length > 0 && credential.session.length <= 32000) {
    let session: { user?: { email?: unknown }; accessToken?: unknown }
    try { session = JSON.parse(credential.session) } catch { throw new ApiError('请粘贴完整的 Session JSON。') }
    if (!session || typeof session.user?.email !== 'string' || typeof session.accessToken !== 'string' || !session.accessToken) throw new ApiError('Session 中缺少账号邮箱或 Access Token。')
    normalized = { mode: 'session', session: credential.session }
  } else if (credential?.mode === 'access_token' && typeof credential.access_token === 'string' && credential.access_token.trim().length > 0 && credential.access_token.length <= 32000) normalized = { mode: 'access_token', access_token: credential.access_token }
  else throw new ApiError('请选择 Session 或 Access Token，并填写有效凭据。')
  await locked(env, row, async () => {
    const current = await getOrder(env, order.id); const ready = await findRechargeOrder(env, order.id)
    if (current.redemption_blocked) throw new ApiError('本订单已禁止再次兑换，请联系支持。', 409)
    if (!ready?.remote_id || ready.financial_hold || ready.payment_status !== 'paid' || current.payment_status !== 'paid' || current.refund_hold || current.refunded_cents || current.status === 'success') throw new ApiError('当前订单不能提交充值，请先刷新付款和售后状态。', 409)
    const raw = JSON.stringify({ mode: 'auto_recharge', order_id: ready.remote_id, credential: normalized, customer_confirmed_email: true })
    const digest = await hash(raw); let attempt = await latestAttempt(env, order.id)
    if (attempt && attempt.id === data.key) {
      if (unknownExpired(attempt)) throw new ApiError('原提交已超过幂等保护期，请联系客服核对，不能再次提交。', 409)
      if (attempt.request_hash !== digest) throw new ApiError('该提交编号已用于其他资料。结果未知时请重交原资料。', 409)
      if (attempt.remote_id || terminal(attempt.status)) return
    } else {
      if (attempt && attempt.status !== 'rejected' && !attempt.fallback) throw new ApiError('原充值结果尚未确认，请查询原任务，勿重复提交。', 409)
      const sessionInput = normalized.mode === 'session' ? JSON.parse(normalized.session!) : null
      const expiryProblem = credentialExpiryProblem(sessionInput?.accessToken ?? normalized.access_token!, sessionInput?.expires)
      if (expiryProblem) throw new ApiError(expiryProblem)
      const now = Date.now(); const event = crypto.randomUUID()
      await env.INTEREST_DB.batch([
        env.INTEREST_DB.prepare("INSERT INTO recharge_redemptions(id,order_id,attempt_no,request_hash,credential_mode,created_at) SELECT ?,id,?,?,?,? FROM orders WHERE id=? AND version=? AND redemption_blocked=0 AND refund_hold=0 AND refunded_cents=0 AND payment_status='paid'").bind(String(data.key), (attempt?.attempt_no ?? 0) + 1, digest, normalized.mode, now, order.id, current.version),
        env.INTEREST_DB.prepare("UPDATE orders SET status='processing',started_at=?,updated_at=?,version=version+1,last_event_id=? WHERE id=? AND EXISTS(SELECT 1 FROM recharge_redemptions WHERE id=? AND order_id=?)").bind(now, now, event, order.id, String(data.key), order.id),
        env.INTEREST_DB.prepare("INSERT INTO order_events(id,order_id,actor,action,from_status,to_status,note,order_version,created_at) SELECT ?,id,'customer','recharge-submit',?,'processing','客户已授权提交充值资料',version,? FROM orders WHERE id=? AND last_event_id=?").bind(event, current.status, now, order.id, event),
      ])
      attempt = await latestAttempt(env, order.id)
      if (!attempt || attempt.id !== data.key) throw new ApiError('订单已更新，请刷新重试。', 409)
    }
    if (!attempt) throw new ApiError('未找到充值提交记录。', 409)
    try { await applyRedemption(env, ready, attempt, await request<Redemption>(env, '/redemptions', raw, 'recharge:' + attempt.id)) }
    catch (e) {
      // A definitive API rejection permits a new attempt. A transport error,
      // conflict, or malformed reply remains unknown and retains the original key.
      if (e instanceof RechargeError && !e.retryable && ['invalid_request', 'session_invalid', 'invalid_credential'].includes(e.code)) {
        await env.INTEREST_DB.prepare("UPDATE recharge_redemptions SET status='rejected',failure_code='request_rejected' WHERE id=? AND status='unknown'").bind(attempt.id).run()
        const rejectedOrder = await getOrder(env, order.id)
        if (!rejectedOrder.refund_hold && !rejectedOrder.refunded_cents && rejectedOrder.status === 'processing') await change(env, rejectedOrder, { status: 'failed' }, 'system', 'recharge-rejected', failureMessages.request_rejected)
      }
      throw e
    }
  })
}
export async function reconcileRecharge(env: Env) {
  // Completed orders remain in the financial rotation: refunds can arrive after delivery,
  // and a missing callback must not leave the original commission available forever.
  const rows = (await env.INTEREST_DB.prepare("SELECT t.* FROM recharge_orders t JOIN orders o ON o.id=t.order_id WHERE t.lease_until<=? AND (t.notify_version>t.checked_notify_version OR (o.refunded_cents<o.price_cents AND o.status IN ('pending','session','processing','failed','success')) OR (o.status='cancelled' AND (t.expires_at>=? OR (t.remote_id IS NULL AND t.attempted_at>=?))) OR EXISTS(SELECT 1 FROM refund_cases r WHERE r.order_id=o.id AND r.status='approved')) ORDER BY t.updated_at LIMIT 10").bind(Date.now(), Date.now() - 86400000, Date.now() - 86400000).all<SupplierOrder>()).results
  let checked = 0; let failed = 0
  await Promise.all(rows.map(async row => { try { await locked(env, row, fresh => syncUnlocked(env, fresh)); checked++ } catch { failed++; await env.INTEREST_DB.prepare('UPDATE recharge_orders SET updated_at=? WHERE order_id=? AND lease_until<=?').bind(Date.now(), row.order_id, Date.now()).run() } }))
  return { checked, failed }
}
export async function rechargeWebhook(message: Request, env: Env) {
  try {
    if (message.method !== 'POST' || !env.RECHARGE_WEBHOOK_SECRET || !message.headers.get('Content-Type')?.toLowerCase().startsWith('application/json')) throw new Error()
    const timestamp = message.headers.get('X-Quefa-Timestamp') ?? ''; const signature = message.headers.get('X-Quefa-Signature') ?? ''
    const event = message.headers.get('X-Quefa-Event-Id') ?? ''; const type = message.headers.get('X-Quefa-Event') ?? ''; const delivery = message.headers.get('X-Quefa-Delivery') ?? ''
    if (!/^\d{10}$/.test(timestamp) || Math.abs(Date.now() / 1000 - Number(timestamp)) > 300 || !/^[A-Za-z0-9_-]{1,128}$/.test(event) || !/^[A-Za-z0-9_-]{1,128}$/.test(delivery) || !/^[a-z.]{1,64}$/.test(type)) throw new Error()
    const match = /^t=(\d{10}),v1=([a-f0-9]{64})$/.exec(signature)
    if (!match || match[1] !== timestamp) throw new Error()
    const raw = await readRaw(message, 64000)
    const expected = await hmac(env.RECHARGE_WEBHOOK_SECRET, timestamp + '.' + raw)
    let diff = 0; for (let i = 0; i < 64; i++) diff |= expected.charCodeAt(i) ^ match[2].charCodeAt(i)
    if (diff) throw new Error()
    const payload = JSON.parse(raw)
    if (!payload || typeof payload !== 'object' || Array.isArray(payload) || (payload.event_id !== undefined && payload.event_id !== event) || (payload.type !== undefined && payload.type !== type) || (payload.event !== undefined && payload.event !== type)) throw new Error()
    const account = config(env).account; const digest = await hash(raw)
    const previous = await env.INTEREST_DB.prepare('SELECT body_hash FROM recharge_webhooks WHERE account=? AND event_id=?').bind(account, event).first<{ body_hash: string }>()
    if (previous && previous.body_hash !== digest) throw new Error()
    // Notifications only schedule authoritative GET reconciliation. Their data
    // can never directly mark payment, recharge, or refund as successful.
    if (!previous) await env.INTEREST_DB.batch([
      env.INTEREST_DB.prepare('INSERT INTO recharge_webhooks(account,event_id,event_type,body_hash,received_at) VALUES (?,?,?,?,?) ON CONFLICT(account,event_id) DO NOTHING').bind(account, event, type, digest, Date.now()),
      env.INTEREST_DB.prepare('UPDATE recharge_orders SET updated_at=0,notify_version=notify_version+1 WHERE account=?').bind(account),
    ])
    const stored = await env.INTEREST_DB.prepare('SELECT body_hash FROM recharge_webhooks WHERE account=? AND event_id=?').bind(account, event).first<{ body_hash: string }>()
    if (stored?.body_hash !== digest) throw new Error()
    return json({ ok: true })
  } catch { return json({ error: '通知未通过验证。' }, 400) }
}
// Explicit reconciliation only reads the original upstream order; never creates a refund.
export async function reconcilePlatformRefund(env: Env, order: Order, amount: number) {
  const row = await findRechargeOrder(env, order.id)
  if (order.mode !== 'live' || order.is_test || !row?.remote_id || order.payment_status !== 'paid' || !order.refund_hold) throw new ApiError('付款或售后状态不正确。', 409)
  return locked(env, row, async fresh => {
    const remote = await request<RemoteOrder>(env, '/orders/' + fresh.remote_id)
    await applyRemoteOrder(env, fresh, remote, false)
    const verified = await findRechargeOrder(env, order.id)
    const total = order.refunded_cents + amount
    if (!['partially_refunded', 'refunded'].includes(remote.payment_status) || amountCents(remote.refunded_amount ?? '0.00') !== total || verified?.refunded_cents !== total) throw new ApiError('平台已退金额与本次核准金额不一致，请核对后再处理。', 409)
    return 'recharge-order:' + fresh.remote_id + ':' + total
  })
}
export async function refundThroughRecharge(env: Env, order: Order, caseId: string, amount: number) {
  const row = await findRechargeOrder(env, order.id); if (!row) return null
  if (!row.remote_id || row.collection_mode !== 'platform_collect' || order.payment_status !== 'paid' || !order.refund_hold) throw new ApiError('付款或售后状态不正确。', 409)
  return locked(env, row, async fresh => {
    const db = env.INTEREST_DB
    let record = await db.prepare('SELECT * FROM recharge_refunds WHERE case_id=?').bind(caseId).first<{ create_body: string; remote_id: string | null; status: string }>()
    if (!record?.remote_id && fresh.refunded_cents > order.refunded_cents) throw new ApiError('平台原订单已确认退款，请使用“核对上游已退款”完成记账。', 409)
    if (!record) {
      const raw = JSON.stringify({ merchant_refund_no: caseId, type: amount === order.price_cents - order.refunded_cents ? 'full' : 'partial', amount: amountText(amount), reason: '本站售后审核通过' })
      await db.prepare('INSERT INTO recharge_refunds(case_id,order_id,create_body) VALUES (?,?,?) ON CONFLICT(case_id) DO NOTHING').bind(caseId, order.id, raw).run()
      record = (await db.prepare('SELECT * FROM recharge_refunds WHERE case_id=?').bind(caseId).first<typeof record>())!
    }
    const remote = record.remote_id ? await request<RemoteRefund>(env, '/refunds/' + record.remote_id) : await request<RemoteRefund>(env, '/orders/' + fresh.remote_id + '/refunds', record.create_body, 'refund:' + caseId)
    if (!remote || !/^[A-Za-z0-9_-]{1,128}$/.test(remote.refund_id) || (record.remote_id && remote.refund_id !== record.remote_id) || remote.order_id !== fresh.remote_id || remote.merchant_refund_no !== caseId || amountCents(remote.amount) !== amount || !['requested','approved','processing','succeeded','failed','rejected','cancelled'].includes(remote.status)) throw new Error('Supplier refund mismatch')
    await db.prepare('UPDATE recharge_refunds SET remote_id=?,status=?,updated_at=? WHERE case_id=?').bind(remote.refund_id, remote.status, Date.now(), caseId).run()
    if (!['succeeded','rejected','cancelled'].includes(remote.status)) throw new ApiError(remote.status === 'failed' ? '平台退款尚未确认，请联系客服核对原退款单。' : '退款已提交平台审核，尚未确认退回；系统会继续核对同一退款单。', 409)
    const latest = await request<RemoteOrder>(env, '/orders/' + fresh.remote_id)
    await applyRemoteOrder(env, fresh, latest)
    if (['rejected','cancelled'].includes(remote.status)) throw new RechargeRefundRejected()
    if (amountCents(latest.refunded_amount ?? '0.00') < order.refunded_cents + amount) throw new ApiError('退款金额尚未核对一致，请稍后重试。', 409)
    return 'recharge:' + caseId
  })
}
export class RechargeRefundRejected extends ApiError {
  constructor() { super('平台已明确拒绝或取消本次退款，本次申请已结束。', 409) }
}
