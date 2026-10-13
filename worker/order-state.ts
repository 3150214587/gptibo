import { ApiError, type Env } from './platform.ts'

export type Status = 'pending' | 'session' | 'processing' | 'success' | 'failed' | 'cancelled'
export type Order = {
  purchase_note?: string
  is_test: number
  customer_email: string | null; refunded_cents: number; refund_hold: number; redemption_blocked: number
  id: string; owner_hash: string; plan: string; plan_name: string; price_cents: number; mode: 'demo' | 'live'
  payment_status: 'unpaid' | 'simulated' | 'paid'; status: Status; created_at: number; updated_at: number
  started_at: number | null; outcome: 'success' | 'failed' | null; sms_phone: string | null; version: number
  partner_id: string | null; partner_name: string | null; cost_cents: number | null
}
export async function getOrder(env: Env, id: string, ownerHash?: string, email?: string | null) {
  const o = await env.INTEREST_DB.prepare('SELECT * FROM orders WHERE id = ?' + (ownerHash ? ' AND is_test=0 AND (owner_hash = ? OR customer_email = ?)' : '')).bind(...(ownerHash ? [id, ownerHash, email ?? null] : [id])).first<Order>()
  if (!o) throw new ApiError('未找到订单或没有访问权限。', 404)
  return o
}
export async function change(env: Env, order: Order, patch: Partial<Order>, actor: string, action: string, note = '') {
  const next = { ...order, ...patch }; const eventId = crypto.randomUUID(); const now = Date.now()
  await env.INTEREST_DB.batch([
    env.INTEREST_DB.prepare('UPDATE orders SET status=?, payment_status=?, started_at=?, outcome=?, sms_phone=?, updated_at=?, version=version+1, last_event_id=? WHERE id=? AND version=?')
      .bind(next.status, next.payment_status, next.started_at, next.outcome, next.sms_phone, now, eventId, order.id, order.version),
    env.INTEREST_DB.prepare('INSERT INTO order_events(id,order_id,actor,action,from_status,to_status,note,order_version,created_at) SELECT ?,id,?,?,?,?,?,version,? FROM orders WHERE id=? AND last_event_id=?')
      .bind(eventId, actor, action, order.status, next.status, note, now, order.id, eventId),
  ])
  const written = await env.INTEREST_DB.prepare('SELECT id FROM order_events WHERE id=?').bind(eventId).first()
  if (!written) throw new ApiError('订单已更新，请刷新后再操作。', 409)
  return getOrder(env, order.id)
}
export async function settle(env: Env, o: Order) {
  if (o.redemption_blocked || o.refund_hold || o.refunded_cents >= o.price_cents || o.mode !== 'demo' || o.status !== 'processing' || !o.started_at || Date.now() - o.started_at < 4500) return o
  try { return await change(env, o, { status: o.outcome === 'failed' ? 'failed' : 'success' }, 'system', 'demo-settle', '历史测试记录已结束，无支付或履约') }
  catch (e) { if (e instanceof ApiError && e.status === 409) return getOrder(env, o.id); throw e }
}

export async function settleDue(env: Env, partnerId?: string) {
  const query = env.INTEREST_DB.prepare("SELECT * FROM orders WHERE mode='demo' AND redemption_blocked=0 AND refund_hold=0 AND refunded_cents<price_cents AND status='processing' AND started_at <= ?" + (partnerId ? ' AND partner_id=?' : '') + ' ORDER BY started_at LIMIT 100')
  const due = (await query.bind(...(partnerId ? [Date.now() - 4500, partnerId] : [Date.now() - 4500])).all<Order>()).results
  await Promise.all(due.map(o => settle(env, o)))
}
