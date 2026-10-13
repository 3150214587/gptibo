import { ApiError, body, json, type Env } from './platform.ts'
import { rechargeReminderMessage, sendRechargeReminderEmail } from './email.ts'
import { supportContactsSettings } from './support-contacts.ts'
import { adminLoginEmail } from './admin-auth.ts'

const LEASE_MS = 60_000
// Resend retains idempotency keys for 24 hours. Stop ambiguous retries before that expires.
const RETRY_WINDOW_MS = 23 * 60 * 60_000
type Reminder = { order_id: string; paid_at: number; attempt_count: number; first_attempt_at: number | null; message_json: string | null; recipient: string | null; lock_token: string }
type WaitingOrder = { customer_email: string; plan_name: string; partner_id: string | null }
const eligible = `o.mode='live' AND o.is_test=0 AND o.payment_status='paid' AND o.status='session'
  AND o.plan<>'codex-sms-once' AND o.customer_email IS NOT NULL AND o.customer_email<>''
  AND o.redemption_blocked=0 AND o.refund_hold=0 AND o.refunded_cents=0 AND o.started_at IS NULL
  AND NOT EXISTS(SELECT 1 FROM recharge_redemptions r WHERE r.order_id=o.id)
  AND NOT EXISTS(SELECT 1 FROM order_events e WHERE e.order_id=o.id AND e.to_status IN ('processing','success','failed'))
  AND NOT EXISTS(SELECT 1 FROM recharge_orders t WHERE t.order_id=o.id AND (t.financial_hold=1 OR t.refunded_cents>0))`

async function messageFor(env: Env, orderId: string, planName: string, partnerId: string | null) {
  let contacts = (await supportContactsSettings(env, partnerId ?? 'platform')).contacts
  if (partnerId && !Object.values(contacts).some(Boolean)) {
    contacts = (await supportContactsSettings(env)).contacts
  }
  const lines = [contacts.wechat && '微信：' + contacts.wechat, contacts.qq && 'QQ：' + contacts.qq, contacts.email && '邮箱：' + contacts.email].filter(Boolean)
  return rechargeReminderMessage({ orderId, planName, contacts: lines.join('\n') || '请返回订单页面查看客服联系方式，或联系向您提供购买链接的服务商。' })
}

export async function sendDueRechargeReminders(env: Env, limit = 20) {
  if (!env.RESEND_API_KEY || !env.RESEND_FROM) return { configured:false, sent:0, failed:0 }
  const now = Date.now()
  await env.INTEREST_DB.prepare(`UPDATE recharge_reminders SET cancelled_at=?
    WHERE delivered_at IS NULL AND cancelled_at IS NULL AND locked_until<=?
      AND ((first_attempt_at IS NOT NULL AND first_attempt_at<=?)
        OR NOT EXISTS(SELECT 1 FROM orders o WHERE o.id=recharge_reminders.order_id AND ${eligible}))`)
    .bind(now, now, now - RETRY_WINDOW_MS).run()
  const rows = (await env.INTEREST_DB.prepare(`SELECT n.* FROM recharge_reminders n JOIN orders o ON o.id=n.order_id
    WHERE n.delivered_at IS NULL AND n.cancelled_at IS NULL AND n.next_attempt_at<=? AND n.locked_until<=?
      AND n.paid_at<=? AND ${eligible} ORDER BY n.next_attempt_at LIMIT ?`)
    .bind(now, now, now - 15 * 60_000, limit).all<Reminder>()).results
  let sent = 0; let failed = 0
  for (let row of rows) {
    const token = crypto.randomUUID(); const claimedAt = Date.now()
    await env.INTEREST_DB.prepare(`UPDATE recharge_reminders SET lock_token=?,locked_until=?
      WHERE order_id=? AND delivered_at IS NULL AND cancelled_at IS NULL AND locked_until<=? AND next_attempt_at<=?`)
      .bind(token, claimedAt + LEASE_MS, row.order_id, claimedAt, claimedAt).run()
    const claimed = await env.INTEREST_DB.prepare('SELECT * FROM recharge_reminders WHERE order_id=?').bind(row.order_id).first<Reminder>()
    if (claimed?.lock_token !== token) continue
    row = claimed
    try {
      const order = await env.INTEREST_DB.prepare(`SELECT o.customer_email,o.plan_name,o.partner_id FROM orders o WHERE o.id=? AND ${eligible}`).bind(row.order_id).first<WaitingOrder>()
      if (!order) {
        await env.INTEREST_DB.prepare('UPDATE recharge_reminders SET cancelled_at=?,locked_until=0 WHERE order_id=? AND lock_token=?').bind(Date.now(), row.order_id, token).run()
        continue
      }
      const message = row.message_json ? JSON.parse(row.message_json) as ReturnType<typeof rechargeReminderMessage> : await messageFor(env, row.order_id, order.plan_name, order.partner_id)
      // Persist the exact payload before sending so network retries keep the same idempotency body.
      const recipient = row.recipient ?? order.customer_email
      await env.INTEREST_DB.prepare('UPDATE recharge_reminders SET message_json=?,recipient=?,first_attempt_at=COALESCE(first_attempt_at,?) WHERE order_id=? AND lock_token=?').bind(JSON.stringify(message), recipient, Date.now(), row.order_id, token).run()
      // Recheck after loading contacts; a customer may submit while we prepare the email.
      if (!await env.INTEREST_DB.prepare(`SELECT o.id FROM orders o WHERE o.id=? AND ${eligible}`).bind(row.order_id).first()) {
        await env.INTEREST_DB.prepare('UPDATE recharge_reminders SET cancelled_at=?,locked_until=0 WHERE order_id=? AND lock_token=?').bind(Date.now(), row.order_id, token).run()
        continue
      }
      await sendRechargeReminderEmail(env, recipient, message, row.order_id)
      await env.INTEREST_DB.prepare('UPDATE recharge_reminders SET delivered_at=?,locked_until=0,attempt_count=attempt_count+1 WHERE order_id=? AND lock_token=?').bind(Date.now(), row.order_id, token).run()
      sent++
    } catch {
      const backoff = Math.min(60 * 60_000, 60_000 * 2 ** Math.min(row.attempt_count, 6))
      await env.INTEREST_DB.prepare('UPDATE recharge_reminders SET locked_until=0,next_attempt_at=?,attempt_count=attempt_count+1 WHERE order_id=? AND lock_token=?').bind(Date.now() + backoff, row.order_id, token).run()
      failed++
    }
  }
  return { configured:true, sent, failed }
}

/** Called only behind requireAdmin; preview destination is server-owned. */
export async function rechargeReminderPreview(request: Request, env: Env) {
  if (request.method !== 'POST') throw new ApiError('请通过 POST 发送模板预览。', 405)
  const data = await body(request)
  if (!env.RESEND_API_KEY || !env.RESEND_FROM) throw new ApiError('邮件服务暂不可用。', 503)
  let partner: {id:string} | null = null
  if (data.partnerId) {
    if (typeof data.partnerId !== 'string') throw new ApiError('代理编号无效。')
    partner = await env.INTEREST_DB.prepare('SELECT id FROM partners WHERE id=?').bind(data.partnerId).first<{id:string}>()
    if (!partner) throw new ApiError('未找到代理。', 404)
  }
  const message = await messageFor(env, '模板预览 · 非真实订单', 'ChatGPT Plus 月度充值', partner?.id ?? null)
  await sendRechargeReminderEmail(env, adminLoginEmail(env), message, 'preview-' + crypto.randomUUID())
  return json({ ok:true, to:adminLoginEmail(env) })
}
