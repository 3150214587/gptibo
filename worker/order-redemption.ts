import { ApiError, type Env } from './platform.ts'
import { getOrder, type Order } from './order-state.ts'

// Serialize the manual marker against an active provider operation. If the marker
// wins first, the customer's fresh read refuses the write; if submission wins,
// the administrator must wait for that original operation before marking it.
export async function setRedemptionBlocked(env: Env, order: Order, blocked: boolean, note: string) {
  const event = crypto.randomUUID(); const now = Date.now()
  const action = blocked ? 'redemption-block' : 'redemption-unblock'
  await env.INTEREST_DB.batch([
    env.INTEREST_DB.prepare(`UPDATE orders SET redemption_blocked=?,updated_at=?,version=version+1,last_event_id=?
      WHERE id=? AND version=? AND redemption_blocked<>?
      AND NOT EXISTS(SELECT 1 FROM recharge_orders t WHERE t.order_id=orders.id AND t.lease_until>?)
      AND NOT EXISTS(SELECT 1 FROM email_deliveries d WHERE d.order_id=orders.id AND d.lease_until>?)
      AND NOT EXISTS(SELECT 1 FROM account_supplier_orders k WHERE k.order_id=orders.id AND (k.lease_until>? OR k.state IN ('creating','unknown'))) AND NOT EXISTS(SELECT 1 FROM delivery_supplier_orders n WHERE n.order_id=orders.id AND (n.lease_until>? OR n.state IN ('creating','unknown','pending'))) AND NOT EXISTS(SELECT 1 FROM xgift_purchases x WHERE x.order_id=orders.id AND (x.lease_until>? OR x.state IN ('creating','unknown','pending')))
      AND NOT EXISTS(SELECT 1 FROM payments p WHERE p.order_id=orders.id AND p.lease_until>?)`)
      .bind(Number(blocked), now, event, order.id, order.version, Number(blocked), now, now, now, now, now, now),
    env.INTEREST_DB.prepare(`INSERT INTO order_events(id,order_id,actor,action,from_status,to_status,note,order_version,created_at)
      SELECT ?,id,?,?,status,status,?,version,? FROM orders WHERE id=? AND last_event_id=?`)
      .bind(event, 'admin:' + env.ADMIN_USERNAME, action, (blocked ? '已禁止再次兑换：' : '已解除禁止兑换：') + note, now, order.id, event),
  ])
  if (!await env.INTEREST_DB.prepare('SELECT id FROM order_events WHERE id=?').bind(event).first()) throw new ApiError('订单已更新或正在提交充值、核对支付，请刷新详情后重试。', 409)
  return getOrder(env, order.id)
}
