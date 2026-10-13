import { type Env } from './platform.ts'
import { boundedResponseText } from './bounded-response.ts'

type Notification = {
  payout_id: string
  attempt_count: number
  amount_cents: number
  gross_cents: number
  deduction_cents: number
  recipient: string
  partner_name: string
  partner_slug: string
  created_at: number
}

const FEISHU = 'https://open.feishu.cn/open-apis'
const LEASE_MS = 60_000
const MAX_BACKOFF_MS = 60 * 60_000

/** Receive ID of the shared "ymx-stock" alert bot; only ou_/oc_ forms are accepted. */
function payoutRecipient(env: Env) {
  const value = env.FEISHU_PAYOUT_RECIPIENT ?? env.FEISHU_STOCK_RECIPIENT ?? ''
  return /^(?:ou|oc)_[a-z0-9]+$/.test(value) ? value : ''
}

export function payoutNotificationsConfigured(env: Env) {
  return Boolean(env.FEISHU_APP_ID && env.FEISHU_APP_SECRET && payoutRecipient(env))
}

async function feishu(path: string, payload: unknown, token?: string) {
  const response = await fetch(FEISHU + path, {
    method: 'POST',
    redirect: 'manual',
    headers: { 'Content-Type': 'application/json', ...(token ? { Authorization: 'Bearer ' + token } : {}) },
    body: JSON.stringify(payload),
    signal: AbortSignal.timeout(8000),
  })
  const data = await boundedResponseText(response, 64000).then(text => JSON.parse(text)).catch(() => null) as { code?: number; tenant_access_token?: string; msg?: string } | null
  if (!response.ok || data?.code !== 0) throw new Error('Feishu request failed: HTTP ' + response.status + ', code ' + (Number.isInteger(data?.code) ? data?.code : 'unknown'))
  return data
}

function money(cents: number) { return '¥' + (cents / 100).toFixed(2) }

export function payoutMessage(row: Notification) {
  const lines = [
    '【提现申请】',
    '代理：' + row.partner_name + '（' + row.partner_slug + '）',
    '金额：' + money(row.amount_cents) + (row.deduction_cents > 0 ? '（抵扣售后退款 ' + money(row.deduction_cents) + '，抵扣前 ' + money(row.gross_cents) + '）' : ''),
    '收款：' + row.recipient,
    '时间：' + new Date(row.created_at).toLocaleString('zh-CN', { timeZone: 'Asia/Shanghai', hour12: false }),
    '后台：https://shop.example.invalid/admin/partners',
  ]
  return lines.join('\n')
}

async function send(env: Env, row: Notification) {
  const auth = await feishu('/auth/v3/tenant_access_token/internal', { app_id: env.FEISHU_APP_ID, app_secret: env.FEISHU_APP_SECRET })
  if (typeof auth?.tenant_access_token !== 'string' || !auth.tenant_access_token) throw new Error('Feishu token unavailable')
  const recipient = payoutRecipient(env)
  await feishu('/im/v1/messages?receive_id_type=' + (recipient.startsWith('oc_') ? 'chat_id' : 'open_id'), {
    receive_id: recipient,
    msg_type: 'text',
    content: JSON.stringify({ text: payoutMessage(row) }),
    // Feishu drops duplicate sends that reuse a uuid, so retries stay idempotent.
    uuid: 'ymx-payout-' + row.payout_id,
  }, auth.tenant_access_token)
}

async function claim(env: Env, now: number) {
  const token = crypto.randomUUID()
  const row = await env.INTEREST_DB.prepare(`SELECT n.payout_id,n.attempt_count,p.amount_cents,p.gross_cents,p.deduction_cents,p.recipient,p.created_at,pt.name partner_name,pt.slug partner_slug
    FROM payout_notifications n
    JOIN partner_payouts p ON p.id=n.payout_id
    JOIN partners pt ON pt.id=p.partner_id
    WHERE n.delivered_at IS NULL AND n.next_attempt_at<=? AND n.locked_until<=?
    ORDER BY n.created_at LIMIT 1`).bind(now, now).first<Notification>()
  if (!row) return null
  await env.INTEREST_DB.prepare('UPDATE payout_notifications SET locked_until=?,lock_token=? WHERE payout_id=? AND delivered_at IS NULL AND locked_until<=?').bind(now + LEASE_MS, token, row.payout_id, now).run()
  const locked = await env.INTEREST_DB.prepare('SELECT lock_token FROM payout_notifications WHERE payout_id=?').bind(row.payout_id).first<{ lock_token: string }>()
  return locked?.lock_token === token ? { row, token } : null
}

async function release(env: Env, payoutId: string, token: string, delivered: boolean, attempts: number) {
  const now = Date.now()
  if (delivered) return env.INTEREST_DB.prepare('UPDATE payout_notifications SET delivered_at=?,locked_until=0,lock_token=?,attempt_count=attempt_count+1 WHERE payout_id=? AND lock_token=?').bind(now, '', payoutId, token).run()
  const backoff = Math.min(MAX_BACKOFF_MS, 60_000 * 2 ** Math.min(attempts, 6))
  return env.INTEREST_DB.prepare('UPDATE payout_notifications SET locked_until=0,lock_token=?,next_attempt_at=?,attempt_count=attempt_count+1 WHERE payout_id=? AND lock_token=?').bind('', now + backoff, payoutId, token).run()
}

async function deliver(env: Env, payoutId: string) {
  if (!payoutNotificationsConfigured(env)) return
  const now = Date.now()
  const token = crypto.randomUUID()
  await env.INTEREST_DB.prepare('UPDATE payout_notifications SET locked_until=?,lock_token=? WHERE payout_id=? AND delivered_at IS NULL AND locked_until<=?').bind(now + LEASE_MS, token, payoutId, now).run()
  const locked = await env.INTEREST_DB.prepare('SELECT lock_token FROM payout_notifications WHERE payout_id=?').bind(payoutId).first<{ lock_token: string }>()
  if (locked?.lock_token !== token) return
  const row = await env.INTEREST_DB.prepare(`SELECT n.payout_id,n.attempt_count,p.amount_cents,p.gross_cents,p.deduction_cents,p.recipient,p.created_at,pt.name partner_name,pt.slug partner_slug
    FROM payout_notifications n JOIN partner_payouts p ON p.id=n.payout_id JOIN partners pt ON pt.id=p.partner_id WHERE n.payout_id=?`).bind(payoutId).first<Notification>()
  if (!row) return release(env, payoutId, token, true, 0)
  try { await send(env, row); await release(env, payoutId, token, true, row.attempt_count) }
  catch { await release(env, payoutId, token, false, row.attempt_count) }
}

/** Best-effort immediate send; the scheduled retry covers delivery failures. */
export async function notifyPayout(env: Env, payoutId: string) {
  try { await deliver(env, payoutId) } catch { /* left pending for retryPayoutNotifications */ }
}

export async function retryPayoutNotifications(env: Env, limit = 5) {
  if (!payoutNotificationsConfigured(env)) return { configured: false, sent: 0, failed: 0 }
  let sent = 0; let failed = 0
  for (let i = 0; i < limit; i++) {
    const claimed = await claim(env, Date.now())
    if (!claimed) break
    try { await send(env, claimed.row); await release(env, claimed.row.payout_id, claimed.token, true, claimed.row.attempt_count); sent += 1 }
    catch { await release(env, claimed.row.payout_id, claimed.token, false, claimed.row.attempt_count); failed += 1 }
  }
  return { configured: true, sent, failed }
}
