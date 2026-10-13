import { ApiError, body, hash, json, type Env } from '../platform.ts'
import { getOrder, type Order } from '../order-state.ts'
import { emailValue } from '../partner-auth.ts'
import { adminRefunds, customerRefunds } from '../refunds.ts'
import { findPayment, paymentAction } from './service.ts'
import { alipayConfigured } from './providers.ts'

// All callers must pass requireAdmin in commerce.ts. No public test route exists.
async function view(env: Env, o: Order) {
  const p = await findPayment(env, o.id)
  const refund = await env.INTEREST_DB.prepare('SELECT id,status,version FROM refund_cases WHERE order_id=? ORDER BY created_at DESC LIMIT 1').bind(o.id).first()
  const events = (await env.INTEREST_DB.prepare('SELECT action,note,created_at FROM order_events WHERE order_id=? ORDER BY order_version').bind(o.id).all()).results
  return { id: o.id, email: o.customer_email, amountCents: o.price_cents, paymentStatus: o.payment_status, status: o.status, refundedCents: o.refunded_cents, refundHold: Boolean(o.refund_hold), createdAt: o.created_at, payment: p ? { provider: p.provider, status: p.state, qr: p.state === 'pending' && p.expires_at > Date.now() ? p.qr : null, expiresAt: p.expires_at, notifiedAt: p.notified_at, tradeNo: p.trade_no } : null, refund, events }
}
function internal(request: Request, path: string, data: unknown) {
  const origin = new URL(request.url).origin
  return new Request(origin + path, { method: 'POST', headers: { Origin: origin, 'Content-Type': 'application/json' }, body: JSON.stringify(data) })
}
async function testRefund(request: Request, env: Env, order: Order) {
  if (order.refunded_cents === 1) return
  if (order.payment_status !== 'paid') throw new ApiError('付款确认后才能原路退款。', 409)
  let c = await env.INTEREST_DB.prepare("SELECT id,status,version FROM refund_cases WHERE order_id=? AND status IN ('pending','approved','refunded') ORDER BY created_at DESC LIMIT 1").bind(order.id).first<{ id: string; status: string; version: number }>()
  if (!c) {
    // Use the same reviewed refund workflow as ordinary orders. The only
    // shortcut is that this administrator-owned, fixed one-cent test is approved.
    await customerRefunds(internal(request, '/api/orders/' + order.id + '/refunds', { key: crypto.randomUUID(), reason: '支付联调测试原路退回 ¥0.01', version: order.version }), env, order)
    c = await env.INTEREST_DB.prepare("SELECT id,status,version FROM refund_cases WHERE order_id=? AND status='pending'").bind(order.id).first()
  }
  if (!c) throw new ApiError('退款状态已更新，请刷新。', 409)
  const path = '/api/admin/refunds/' + c.id
  if (c.status === 'pending') {
    await adminRefunds(internal(request, path, { action: 'approve', version: c.version, amountCents: 1, note: '管理员确认退回支付联调测试款' }), env, new URL(path, request.url))
    c = await env.INTEREST_DB.prepare('SELECT id,status,version FROM refund_cases WHERE id=?').bind(c.id).first()
  }
  if (c?.status === 'approved') await adminRefunds(internal(request, path, { action: 'execute-refund', version: c.version, note: '支付联调测试原路退款' }), env, new URL(path, request.url))
}
export async function paymentTesting(request: Request, env: Env, url: URL) {
  const db = env.INTEREST_DB
  if (url.pathname === '/api/admin/payments/test') {
    if (request.method === 'GET') {
      const rows = (await db.prepare('SELECT * FROM orders WHERE is_test=1 ORDER BY created_at DESC LIMIT 10').all<Order>()).results
      return json({ enabled: env.PAYMENT_TEST_ENABLED === 'true' && alipayConfigured(env), orders: await Promise.all(rows.map(o => view(env, o))) })
    }
    if (request.method !== 'POST') throw new ApiError('请求方法不支持。', 405)
    const data = await body(request)
    if (env.PAYMENT_TEST_ENABLED !== 'true' || !alipayConfigured(env)) throw new ApiError('支付联调尚未启用。', 503)
    if (typeof data.key !== 'string' || !/^[a-f0-9-]{36}$/.test(data.key)) throw new ApiError('请求编号无效。')
    const email = emailValue(data.email)
    const owner = await hash('admin-payment-test:' + env.ADMIN_USERNAME)
    const existing = await db.prepare('SELECT * FROM orders WHERE owner_hash=? AND idempotency_key=? AND is_test=1').bind(owner, data.key).first<Order>()
    if (existing) return json({ order: await view(env, existing) })
    if (await db.prepare("SELECT id FROM orders WHERE is_test=1 AND status IN ('pending','session') AND refunded_cents=0").first()) throw new ApiError('请先完成或关闭现有测试单，再创建新的测试。', 409)
    const id = 'RECHARGE-TEST-' + crypto.randomUUID().replaceAll('-', '').toUpperCase(); const event = crypto.randomUUID(); const now = Date.now()
    await db.batch([
      db.prepare("INSERT INTO orders(id,owner_hash,idempotency_key,plan,plan_name,price_cents,mode,status,is_test,customer_email,created_at,updated_at,last_event_id) VALUES (?,?,?,'plus','支付联调测试',1,'live','pending',1,?,?,?,?) ON CONFLICT DO NOTHING").bind(id, owner, data.key, email, now, now, event),
      db.prepare("INSERT INTO order_events(id,order_id,actor,action,to_status,note,order_version,created_at) SELECT ?,id,'admin','create-test','pending','真实支付联调 ¥0.01，不交付商品、不计入营业收入或代理收益',0,? FROM orders WHERE id=? ON CONFLICT DO NOTHING").bind(event, now, id),
    ])
    const created = await db.prepare('SELECT * FROM orders WHERE owner_hash=? AND idempotency_key=? AND is_test=1').bind(owner, data.key).first<Order>()
    if (!created) throw new ApiError('已有测试单正在创建，请刷新。', 409)
    return json({ order: await view(env, created) }, 201)
  }
  const match = url.pathname.match(/^\/api\/admin\/payments\/test\/(RECHARGE-TEST-[A-F0-9]{32})$/)
  if (!match) throw new ApiError('测试单不存在。', 404)
  const o = await getOrder(env, match[1])
  if (!o.is_test || o.mode !== 'live' || o.price_cents !== 1 || o.partner_id) throw new ApiError('不是有效的支付测试单。', 409)
  if (request.method === 'GET') return json({ order: await view(env, o) })
  if (request.method !== 'POST') throw new ApiError('请求方法不支持。', 405)
  const data = await body(request)
  if (data.action === 'close' && o.status === 'pending' && !await findPayment(env, o.id)) {
    const event = crypto.randomUUID(); const now = Date.now()
    await db.batch([
      db.prepare("UPDATE orders SET status='cancelled',version=version+1,updated_at=?,last_event_id=? WHERE id=? AND status='pending' AND payment_status='unpaid' AND NOT EXISTS(SELECT 1 FROM payments WHERE order_id=orders.id)").bind(now, event, o.id),
      db.prepare("INSERT INTO order_events(id,order_id,actor,action,from_status,to_status,note,order_version,created_at) SELECT ?,id,'admin','test-closed','pending','cancelled','关闭尚未向渠道发起付款的测试单',version,? FROM orders WHERE id=? AND last_event_id=?").bind(event, now, o.id, event),
    ])
    const fresh = await getOrder(env, o.id)
    if (fresh.status === 'cancelled') return json({ order: await view(env, fresh) })
    // Another request may have created a payment first; then close through the gateway.
  }
  if (data.action === 'refund') await testRefund(request, env, o)
  else await paymentAction(internal(request, url.pathname, data), env, o, true)
  return json({ order: await view(env, await getOrder(env, o.id)) })
}
