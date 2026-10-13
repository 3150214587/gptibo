import { ApiError, hash, json, type Env } from './platform.ts'
import { boundedResponseText } from './bounded-response.ts'
import { emailValue, mailConfigured, partnerLimit } from './partner-auth.ts'
import { provisionPartner } from './partner-provision.ts'
import { sendPartnerInvitationEmail } from './email.ts'

type Session = { user_id: number; group_id: number | null; step: string; wechat: string; qq: string; email: string; expires_at: number; last_update_id: number; lock_token: string }
type Application = { id: string; user_id: number; partner_id: string; email: string; created_at: number; mail_status: string; mail_attempts: number; notice_sent: number }
type Button = { text: string; callback_data?: string; url?: string }
type Reply = { chat_id: number; text: string; reply_markup?: { inline_keyboard: Button[][] }; link_preview_options: { is_disabled: true } }
type Update = { update_id: number; message?: { message_id: number; text?: string; from?: { id: number; is_bot?: boolean }; chat: { id: number; type: string } }; callback_query?: { id: string; data?: string; from: { id: number; is_bot?: boolean }; message?: { message_id: number; chat: { id: number; type: string } } } }
const DAY = 86_400_000
const LOCK = 60_000
const buttons = (s: Session) => [[{ text: '确认并开通', callback_data: 'apply_confirm:' + s.expires_at }], [{ text: '重新填写', callback_data: 'apply_restart' }, { text: '取消', callback_data: 'apply_cancel' }]]
const reply = (chatId: number, text: string, keyboard?: Button[][]): Reply => ({ chat_id: chatId, text, link_preview_options: { is_disabled: true }, ...(keyboard ? { reply_markup: { inline_keyboard: keyboard } } : {}) })

export function telegramGroups(env: Env): number[] {
  const ids = (env.TELEGRAM_ALLOWED_GROUP_IDS ?? '').split(',').map(s => s.trim()).filter(Boolean)
  return ids.length && ids.length <= 20 && ids.every(s => /^-[1-9]\d*$/.test(s) && Number.isSafeInteger(Number(s))) ? [...new Set(ids.map(Number))] : []
}
function configured(env: Env) {
  return Boolean(env.TELEGRAM_BOT_TOKEN && env.TELEGRAM_WEBHOOK_SECRET && /^[A-Za-z0-9_-]{32,256}$/.test(env.TELEGRAM_WEBHOOK_SECRET) && /^[A-Za-z0-9_]{5,32}$/.test(env.TELEGRAM_BOT_USERNAME ?? '') && telegramGroups(env).length)
}
async function telegram<T>(env: Env, method: string, payload: unknown): Promise<T> {
  const response = await fetch(`https://api.telegram.org/bot${env.TELEGRAM_BOT_TOKEN}/${method}`, {
    method: 'POST', redirect: 'manual', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(payload), signal: AbortSignal.timeout(10_000),
  })
  // Never expose Telegram response text or URLs: they can include credentials/data.
  if (!response.ok) throw new Error('Telegram service unavailable')
  const result = JSON.parse(await boundedResponseText(response, 32_768)) as { ok: boolean; result: T }
  if (result.ok !== true) throw new Error('Telegram operation rejected')
  return result.result
}
async function member(env: Env, groupId: number, userId: number) {
  if (!telegramGroups(env).includes(groupId)) return false
  const result = await telegram<{ status: string; is_member?: boolean }>(env, 'getChatMember', { chat_id: groupId, user_id: userId })
  return ['creator', 'administrator', 'member'].includes(result.status) || (result.status === 'restricted' && result.is_member === true)
}
async function applicantGroup(env: Env, userId: number, preferred: number | null) {
  const groups = telegramGroups(env)
  if (preferred !== null && groups.includes(preferred)) {
    groups.splice(groups.indexOf(preferred), 1)
    groups.unshift(preferred)
  }
  let unavailable = false
  // At most four batches of 10-second requests, within the 60-second session lease.
  for (let offset = 0; offset < groups.length; offset += 5) {
    const batch = groups.slice(offset, offset + 5)
    const results = await Promise.allSettled(batch.map(groupId => member(env, groupId, userId)))
    for (let i = 0; i < results.length; i++) {
      const result = results[i]
      if (result.status === 'fulfilled' && result.value) return batch[i]
      if (result.status === 'rejected') unavailable = true
    }
  }
  if (unavailable) throw new ApiError('暂时无法验证群成员资格，请稍后发送 /apply 重试。', 503)
  return null
}
async function application(env: Env, userId: number) {
  return env.INTEREST_DB.prepare('SELECT * FROM telegram_partner_applications WHERE user_id=?').bind(userId).first<Application>()
}
function statusMessage(app: Application) {
  return app.mail_status === 'sent' ? '代理已开通，邀请邮件已发送，请检查收件箱及垃圾邮件。\n工作台：https://shop.example.invalid/join\n登录时使用你的邮箱获取验证码。'
    : app.mail_status === 'failed' ? '代理已开通，但邀请邮件暂未确认发送成功。请联系群管理员重发邀请。\n你也可直接前往 https://shop.example.invalid/join，使用申请邮箱获取验证码登录。'
    : '代理已开通，邀请邮件正在发送，完成后会在这里通知你。\n工作台：https://shop.example.invalid/join'
}
function reset(s: Session) { s.step = 'idle'; s.wechat = ''; s.qq = ''; s.email = ''; s.expires_at = 0 }
async function plan(env: Env, s: Session, text: string, callback: boolean): Promise<Reply> {
  const userId = s.user_id
  const app = await application(env, userId)
  const command = text.split(/\s+/, 1)[0].toLowerCase()
  if (command === '/help') return reply(userId, '加入申请群后，直接在这里发送 /apply 或 /start，依次填写微信、QQ 和邮箱，确认后自动开通并发送邀请邮件。也可从群里的“申请代理”按钮进入。\n/status 查询结果，/cancel 取消填写，/restart 重新填写。')
  if (command === '/status' || app) return reply(userId, app ? statusMessage(app) : '还没有提交申请，发送 /apply 开始填写。')
  if (command === '/cancel' || text === 'apply_cancel') { reset(s); return reply(userId, '已取消，未提交的资料已清除。需要时发送 /apply 重新申请。') }
  if (command === '/start' || command === '/apply' || command === '/restart' || text === 'apply_restart') {
    const start = text.match(/^\/start\s+apply_(-\d+)$/i)
    await partnerLimit(env, 'tg-start:' + userId, 10, 3_600_000)
    const groupId = start ? (await member(env, Number(start[1]), userId) ? Number(start[1]) : null) : await applicantGroup(env, userId, s.group_id)
    if (!groupId) return reply(userId, '请先加入申请群，再在这里发送 /apply 申请，也可点击群里的“申请代理”按钮。')
    reset(s); s.group_id = groupId; s.step = 'wechat'; s.expires_at = Date.now() + 3_600_000
    return reply(userId, '申请 shop.example.invalid 代理\n微信和 QQ 会自动保存为你的商店联系方式并向客户展示；邮箱用于开户、邀请邮件和登录。管理员可查看申请资料。\n\n第 1/3 步：请输入微信号（不是昵称）。\n填写中可用 /cancel 取消；1 小时未完成需重新申请。')
  }
  if (s.expires_at <= Date.now() || s.step === 'idle') { reset(s); return reply(userId, '发送 /apply 开始申请；填写已过期时可用 /restart 重填。') }
  if (callback && text !== 'apply_confirm:' + s.expires_at) return reply(userId, '这个按钮已失效，请使用最新的确认按钮。')
  if (s.step === 'wechat') {
    if (callback || !/^[A-Za-z][A-Za-z0-9_-]{5,19}$/.test(text)) return reply(userId, '请输入有效微信号：6–20 位，以字母开头，支持字母、数字、下划线和减号。')
    s.wechat = text; s.step = 'qq'; return reply(userId, '第 2/3 步：请输入 QQ 号（5–12 位数字）。')
  }
  if (s.step === 'qq') {
    if (callback || !/^[1-9]\d{4,11}$/.test(text)) return reply(userId, 'QQ 号需为 5–12 位数字，不能以 0 开头。')
    s.qq = text; s.step = 'email'; return reply(userId, '第 3/3 步：请输入收件邮箱，也是以后登录代理工作台的邮箱。')
  }
  if (s.step === 'email') {
    let email: string
    try { email = emailValue(text); if (callback || /[<>]/.test(email) || [...email].some(char => char.charCodeAt(0) < 32 || char.charCodeAt(0) === 127)) throw new Error() }
    catch { return reply(userId, '邮箱格式不正确，请重新输入。') }
    s.email = email; s.step = 'confirm'
    return reply(userId, `请核对申请资料：\n微信：${s.wechat}\nQQ：${s.qq}\n邮箱：${s.email}\n\n点击“确认并开通”，同意使用以上资料开户，在你的商店公开展示微信和 QQ，并向该邮箱发送邀请邮件。`, buttons(s))
  }
  if (s.step === 'confirm') {
    if (!callback || text !== 'apply_confirm:' + s.expires_at) return reply(userId, '请点击“确认并开通”，需要修改可点“重新填写”。', buttons(s))
    if (!s.group_id || !await member(env, s.group_id, userId)) return reply(userId, '你已不在申请群，暂不能开通。请加入群后重新确认。', buttons(s))
    await partnerLimit(env, 'tg-confirm:' + userId, 5, 3_600_000)
    await partnerLimit(env, 'tg-email:' + s.email, 3, 3_600_000)
    await partnerLimit(env, 'tg-create-global', 100, 3_600_000)
    try {
      await provisionPartner(env, s.email, 'Telegram 用户 ' + userId, { userId, groupId: s.group_id, lockToken: s.lock_token, wechat: s.wechat, qq: s.qq })
      s.step = 'submitted'; s.wechat = ''; s.qq = ''; s.email = ''; s.expires_at = 0
      return reply(userId, '代理已开通，邀请邮件正在发送。稍后会在这里通知你。\n可用 /status 查询结果。')
    } catch (error) {
      if (error instanceof ApiError && error.status === 409) { reset(s); return reply(userId, error.message) }
      throw error
    }
  }
  return reply(userId, '请使用 /status 查询申请结果。')
}

/** Persist the response with the state change before Telegram delivery. Retried updates only replay the response. */
export async function telegramWebhook(request: Request, env: Env): Promise<Response> {
  if (!configured(env)) return json({ error: '机器人尚未配置。' }, 503)
  const supplied = request.headers.get('X-Telegram-Bot-Api-Secret-Token') ?? ''
  if (supplied.length > 256 || await hash(supplied) !== await hash(env.TELEGRAM_WEBHOOK_SECRET!)) return json({ error: '验证失败。' }, 403)
  if (request.method === 'GET') return json({ ready: mailConfigured(env), groups: telegramGroups(env) })
  if (request.method !== 'POST') return json({ error: '不支持此方法。' }, 405)
  if (!request.headers.get('Content-Type')?.toLowerCase().startsWith('application/json')) return json({ error: '格式不正确。' }, 415)
  let update: Update
  try { update = JSON.parse(await boundedResponseText(new Response(request.body), 16_384)) }
  catch { return json({ error: '格式不正确或内容过长。' }, 400) }
  if (!update || !Number.isSafeInteger(update.update_id) || update.update_id < 0) return json({ error: '更新编号无效。' }, 400)
  const message = update.message ?? update.callback_query?.message
  const from = update.message?.from ?? update.callback_query?.from
  if (!message || !from || from.is_bot || !Number.isSafeInteger(from.id) || from.id <= 0 || !Number.isSafeInteger(message.chat?.id)) return json({ ok: true })
  const chat = message.chat
  const text = (update.message?.text ?? update.callback_query?.data ?? '').trim()
  if (text.length > 512 || !text) return json({ ok: true })
  if (chat.type === 'private' && chat.id !== from.id) return json({ ok: true })
  const command = text.match(/^\/(apply|start|help|status|cancel|restart|groupid)(?:@([A-Za-z0-9_]+))?(?:\s|$)/i)
  if (command?.[2] && command[2].toLowerCase() !== env.TELEGRAM_BOT_USERNAME!.toLowerCase()) return json({ ok: true })
  if (chat.type !== 'private' && (!['group', 'supergroup'].includes(chat.type) || !telegramGroups(env).includes(chat.id) || !command || !['apply', 'start', 'help', 'groupid'].includes(command[1].toLowerCase()))) return json({ ok: true })
  const db = env.INTEREST_DB, id = update.update_id, now = Date.now(), lockToken = crypto.randomUUID()
  const saved = await db.prepare('SELECT reply FROM telegram_partner_updates WHERE update_id=?').bind(id).first<{ reply: string | null }>()
  if (saved?.reply) { const data = JSON.parse(saved.reply); if (data) await telegram(env, 'sendMessage', data); return json({ ok: true }) }
  await db.prepare('INSERT INTO telegram_partner_updates(update_id,created_at,lock_token,lock_until) VALUES (?,?,?,?) ON CONFLICT(update_id) DO UPDATE SET lock_token=excluded.lock_token,lock_until=excluded.lock_until WHERE telegram_partner_updates.reply IS NULL AND telegram_partner_updates.lock_until<=?').bind(id, now, lockToken, now + LOCK, now).run()
  if (!await db.prepare('SELECT update_id FROM telegram_partner_updates WHERE update_id=? AND lock_token=?').bind(id, lockToken).first()) return json({ error: '正在处理，请重试。' }, 503)
  let ownsSession = false
  try {
    let response: Reply | null = null
    if (chat.type !== 'private') {
      response = command?.[1].toLowerCase() === 'groupid' ? reply(chat.id, `本群 ID：${chat.id}`)
        : reply(chat.id, '申请 shop.example.invalid 代理：点击按钮，在私聊中填写资料并确认。', [[{ text: '申请代理', url: `https://t.me/${env.TELEGRAM_BOT_USERNAME}?start=apply_${chat.id}` }]])
    } else {
      await db.prepare('INSERT INTO telegram_partner_sessions(user_id,lock_token,lock_until) VALUES (?,?,?) ON CONFLICT(user_id) DO UPDATE SET lock_token=excluded.lock_token,lock_until=excluded.lock_until WHERE telegram_partner_sessions.lock_until<=?').bind(from.id, lockToken, now + LOCK, now).run()
      const s = await db.prepare('SELECT * FROM telegram_partner_sessions WHERE user_id=? AND lock_token=?').bind(from.id, lockToken).first<Session>()
      if (!s) throw new Error('Session busy')
      ownsSession = true
      if (s.last_update_id !== id) {
        try {
          await partnerLimit(env, 'tg-message:' + from.id, 30, 60_000)
          response = await plan(env, s, command ? text.replace(/@[A-Za-z0-9_]+(?=\s|$)/, '') : text, Boolean(update.callback_query))
        }
        catch (error) { if (error instanceof ApiError && [429, 503].includes(error.status)) response = reply(from.id, error.message); else throw error }
        await db.batch([
          db.prepare('UPDATE telegram_partner_sessions SET group_id=?,step=?,wechat=?,qq=?,email=?,expires_at=?,last_update_id=? WHERE user_id=? AND lock_token=? AND lock_until>?').bind(s.group_id, s.step, s.wechat, s.qq, s.email, s.expires_at, id, from.id, lockToken, Date.now()),
          db.prepare('UPDATE telegram_partner_updates SET reply=?,lock_until=0 WHERE update_id=? AND lock_token=? AND EXISTS(SELECT 1 FROM telegram_partner_sessions WHERE user_id=? AND lock_token=? AND last_update_id=?)').bind(JSON.stringify(response), id, lockToken, from.id, lockToken, id),
        ])
      } else await db.prepare("UPDATE telegram_partner_updates SET reply='null',lock_until=0 WHERE update_id=? AND lock_token=?").bind(id, lockToken).run()
    }
    if (!ownsSession) await db.prepare('UPDATE telegram_partner_updates SET reply=?,lock_until=0 WHERE update_id=? AND lock_token=?').bind(JSON.stringify(response), id, lockToken).run()
    const committed = await db.prepare('SELECT reply FROM telegram_partner_updates WHERE update_id=? AND lock_token=?').bind(id, lockToken).first<{ reply: string | null }>()
    if (!committed?.reply) throw new Error('Update lease expired')
    if (update.callback_query && typeof update.callback_query.id === 'string' && update.callback_query.id.length <= 256) {
      await telegram(env, 'answerCallbackQuery', { callback_query_id: update.callback_query.id }).catch(() => {})
    }
    if (response) await telegram(env, 'sendMessage', response)
    return json({ ok: true })
  } finally {
    if (ownsSession) await db.prepare('UPDATE telegram_partner_sessions SET lock_token=NULL,lock_until=0 WHERE user_id=? AND lock_token=?').bind(from.id, lockToken).run()
    await db.prepare('UPDATE telegram_partner_updates SET lock_token=NULL,lock_until=0 WHERE update_id=? AND lock_token=?').bind(id, lockToken).run()
  }
}

/** Existing minute cron delivers the durable invitation queue, including after worker restarts. */
export async function retryTelegramInvitations(env: Env) {
  if (!configured(env) || !mailConfigured(env)) return
  const db = env.INTEREST_DB, now = Date.now()
  const rows = (await db.prepare("SELECT * FROM telegram_partner_applications WHERE (mail_status='pending' AND next_attempt_at<=? OR mail_status IN ('sent','failed') AND notice_sent=0 AND next_attempt_at<=?) AND lock_until<=? ORDER BY next_attempt_at LIMIT 10").bind(now, now, now).all<Application>()).results
  for (const row of rows) {
    const lockToken = crypto.randomUUID()
    const claimed = await db.prepare("UPDATE telegram_partner_applications SET lock_token=?,lock_until=? WHERE id=? AND lock_until<=? AND next_attempt_at<=? AND (mail_status='pending' OR notice_sent=0) RETURNING *").bind(lockToken, Date.now() + LOCK, row.id, Date.now(), Date.now()).first<Application>()
    if (!claimed) continue
    try {
      const p = await db.prepare('SELECT email,active FROM partners WHERE id=?').bind(row.partner_id).first<{ email: string; active: number }>()
      if (!p?.active || p.email !== claimed.email) {
        // Never deliver an obsolete invitation after administrator changes/disablement.
        await db.prepare("UPDATE telegram_partner_applications SET mail_status='failed',notice_sent=1 WHERE id=? AND lock_token=?").bind(row.id, lockToken).run()
        continue
      }
      if (claimed.mail_status === 'pending') {
        // Resend retains keys for 24h. Stop at 23h, preserving ambiguity for admin review.
        if (claimed.mail_attempts >= 10 || Date.now() - claimed.created_at >= 23 * 3_600_000) {
          claimed.mail_status = 'failed'
          await db.prepare("UPDATE telegram_partner_applications SET mail_status='failed' WHERE id=? AND lock_token=?").bind(row.id, lockToken).run()
        } else {
          await db.prepare('UPDATE telegram_partner_applications SET mail_attempts=mail_attempts+1 WHERE id=? AND lock_token=?').bind(row.id, lockToken).run()
          try { await sendPartnerInvitationEmail(env, claimed.email, 'telegram-' + row.id) }
          catch {
            await db.prepare('UPDATE telegram_partner_applications SET next_attempt_at=? WHERE id=? AND lock_token=?').bind(Date.now() + Math.min(3_600_000, 60_000 * 2 ** claimed.mail_attempts), row.id, lockToken).run()
            continue
          }
          await db.prepare("UPDATE telegram_partner_applications SET mail_status='sent' WHERE id=? AND lock_token=?").bind(row.id, lockToken).run()
          claimed.mail_status = 'sent'
        }
      }
      if (!claimed.notice_sent) {
        try {
          await telegram(env, 'sendMessage', reply(row.user_id, statusMessage(claimed)))
          await db.prepare('UPDATE telegram_partner_applications SET notice_sent=1 WHERE id=? AND lock_token=?').bind(row.id, lockToken).run()
        } catch {
          await db.prepare('UPDATE telegram_partner_applications SET next_attempt_at=? WHERE id=? AND lock_token=?').bind(Date.now() + 3_600_000, row.id, lockToken).run()
        }
      }
    } finally { await db.prepare('UPDATE telegram_partner_applications SET lock_token=NULL,lock_until=0 WHERE id=? AND lock_token=?').bind(row.id, lockToken).run() }
  }
  await db.batch([
    db.prepare('DELETE FROM telegram_partner_updates WHERE created_at<? AND lock_until<=?').bind(now - 7 * DAY, now),
    db.prepare("UPDATE telegram_partner_sessions SET step='idle',wechat='',qq='',email='' WHERE expires_at<=? AND lock_until<=? AND step NOT IN ('idle','submitted')").bind(now, now),
  ])
}
