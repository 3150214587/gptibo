import { sendPartnerInvitationEmail, sendVerificationEmail } from './email.ts'
import { ApiError, body, cookie, hash, json, LOGIN_SESSION_SECONDS, randomToken, readCookie, type Env } from './platform.ts'

export type Partner = { id: string; slug: string; name: string; email: string; active: number; version: number; auth_version: number; created_at: number }
export const mailConfigured = (env: Env) => Boolean(env.RESEND_API_KEY && env.RESEND_FROM && env.PARTNER_AUTH_SECRET && env.PARTNER_AUTH_SECRET.length >= 32)
export function emailValue(value: unknown) {
  const email = typeof value === 'string' ? value.trim().toLowerCase() : ''
  if (email.length > 254 || !/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email)) throw new ApiError('请输入有效的邮箱地址。')
  return email
}
export async function partnerLimit(env: Env, key: string, max: number, duration: number) {
  const now = Date.now()
  const row = await env.INTEREST_DB.prepare('INSERT INTO admin_login_limits(key,attempts,reset_at) VALUES (?,1,?) ON CONFLICT(key) DO UPDATE SET attempts=CASE WHEN reset_at<=? THEN 1 ELSE attempts+1 END, reset_at=CASE WHEN reset_at<=? THEN excluded.reset_at ELSE reset_at END RETURNING attempts').bind(await hash(key), now + duration, now, now).first<{ attempts: number }>()
  if (!row || row.attempts > max) throw new ApiError('操作过于频繁，请稍后重试。', 429)
}
export async function requirePartner(request: Request, env: Env): Promise<Partner> {
  const token = readCookie(request, 'partner')
  if (!token || !env.PARTNER_AUTH_SECRET) throw new ApiError('请先登录代理工作台。', 401)
  const partner = await env.INTEREST_DB.prepare('SELECT p.* FROM partner_sessions s JOIN partners p ON p.id=s.partner_id WHERE s.token_hash=? AND s.expires_at>? AND s.auth_version=p.auth_version AND s.credential_version=? AND p.active=1').bind(await hash(token), Date.now(), await hash(env.PARTNER_AUTH_SECRET)).first<Partner>()
  if (!partner) throw new ApiError('登录已过期或账号已停用，请重新登录。', 401)
  return partner
}
export async function sendPartnerInvitation(env: Env, p: Partner, invitationId: string = crypto.randomUUID()) {
  if (!mailConfigured(env)) throw new ApiError('邮箱登录尚未启用，请联系平台管理员配置邮件服务。', 503)
  try {
    await sendPartnerInvitationEmail(env, p.email, invitationId)
  } catch {
    throw new ApiError('邀请邮件暂时发送失败，请稍后重试。', 503)
  }
}

export async function sendPartnerCode(env: Env, p: Partner) {
  if (!mailConfigured(env)) throw new ApiError('邮箱登录尚未启用，请联系平台管理员配置邮件服务。', 503)
  const id = crypto.randomUUID(); const now = Date.now()
  // Rejection sampling keeps all six-digit values equally likely.
  let value: number
  do { value = crypto.getRandomValues(new Uint32Array(1))[0] } while (value >= 4294000000)
  const code = String(value % 1000000).padStart(6, '0')
  await env.INTEREST_DB.batch([
    env.INTEREST_DB.prepare('DELETE FROM partner_login_codes WHERE partner_id=? OR expires_at<=?').bind(p.id, now),
    env.INTEREST_DB.prepare('DELETE FROM partner_sessions WHERE expires_at<=?').bind(now),
    env.INTEREST_DB.prepare('INSERT INTO partner_login_codes(id,partner_id,code_hash,auth_version,expires_at) SELECT ?,id,?,auth_version,? FROM partners WHERE id=? AND active=1 AND auth_version=?').bind(id, await hash(env.PARTNER_AUTH_SECRET + ':' + id + ':' + code), now + 600000, p.id, p.auth_version),
  ])
  if (!await env.INTEREST_DB.prepare('SELECT id FROM partner_login_codes WHERE id=?').bind(id).first()) throw new ApiError('账号已更新，请刷新后重试。', 409)
  try {
    await sendVerificationEmail(env, 'partner-login', p.email, code, id)
  } catch {
    await env.INTEREST_DB.prepare('DELETE FROM partner_login_codes WHERE id=?').bind(id).run()
    throw new ApiError('验证码暂时发送失败，请稍后重试。', 503)
  }
  return id
}
export async function partnerAuth(request: Request, env: Env, path: string) {
  if (path === '/api/partner/auth/session' && request.method === 'GET') {
    try { const p = await requirePartner(request, env); return json({ authenticated: true, mailReady: mailConfigured(env), name: p.name }) }
    catch (e) { if (e instanceof ApiError && e.status === 401) return json({ authenticated: false, mailReady: mailConfigured(env) }); throw e }
  }
  if (path === '/api/partner/auth/logout' && request.method === 'POST') {
    await body(request); const token = readCookie(request, 'partner')
    if (token) await env.INTEREST_DB.prepare('DELETE FROM partner_sessions WHERE token_hash=?').bind(await hash(token)).run()
    return json({ ok: true }, 200, { 'Set-Cookie': cookie(request, 'partner', '', 0) })
  }
  if (path === '/api/partner/auth/send' && request.method === 'POST') {
    const data = await body(request); const email = emailValue(data.email)
    if (!mailConfigured(env)) throw new ApiError('邮箱登录尚未启用，请联系平台管理员配置邮件服务。', 503)
    await partnerLimit(env, 'partner-send-ip:' + (request.headers.get('CF-Connecting-IP') ?? 'local'), 10, 3600000)
    await partnerLimit(env, 'partner-send-minute:' + email, 1, 60000)
    await partnerLimit(env, 'partner-send-hour:' + email, 5, 3600000)
    const p = await env.INTEREST_DB.prepare('SELECT * FROM partners WHERE email=? AND active=1').bind(email).first<Partner>()
    const id = p ? await sendPartnerCode(env, p) : crypto.randomUUID()
    // Unknown and disabled addresses receive the same public response.
    return json({ challenge: id, message: '若该邮箱已开通代理，验证码将发送至邮箱。', retryAfter: 60 })
  }
  if (path === '/api/partner/auth/verify' && request.method === 'POST') {
    const data = await body(request)
    if (!mailConfigured(env)) throw new ApiError('邮箱登录尚未启用。', 503)
    await partnerLimit(env, 'partner-verify-ip:' + (request.headers.get('CF-Connecting-IP') ?? 'local'), 20, 900000)
    let challengeId = data.challenge
    if (data.email !== undefined) {
      const email = emailValue(data.email)
      await partnerLimit(env, 'partner-verify-email:' + email, 10, 900000)
      const latest = await env.INTEREST_DB.prepare('SELECT c.id FROM partner_login_codes c JOIN partners p ON p.id=c.partner_id WHERE p.email=? AND p.active=1 AND c.auth_version=p.auth_version AND c.used=0 AND c.expires_at>? ORDER BY c.expires_at DESC LIMIT 1').bind(email, Date.now()).first<{ id: string }>()
      challengeId = latest?.id
    }
    if (typeof challengeId !== 'string' || challengeId.length !== 36 || typeof data.code !== 'string' || !/^\d{6}$/.test(data.code)) throw new ApiError('验证码无效或已过期。', 401)
    const challenge = await env.INTEREST_DB.prepare('UPDATE partner_login_codes SET attempts=attempts+1 WHERE id=? AND attempts<5 AND used=0 AND expires_at>? RETURNING *').bind(challengeId, Date.now()).first<{ code_hash: string; partner_id: string; auth_version: number }>()
    if (!challenge || challenge.code_hash !== await hash(env.PARTNER_AUTH_SECRET + ':' + challengeId + ':' + data.code)) throw new ApiError('验证码无效或已过期。', 401)
    const claimed = await env.INTEREST_DB.prepare('UPDATE partner_login_codes SET used=1 WHERE id=? AND used=0 AND expires_at>? AND EXISTS(SELECT 1 FROM partners WHERE id=partner_id AND active=1 AND auth_version=partner_login_codes.auth_version) RETURNING partner_id').bind(challengeId, Date.now()).first()
    if (!claimed) throw new ApiError('验证码无效或已过期。', 401)
    const token = randomToken(); const tokenHash = await hash(token)
    await env.INTEREST_DB.prepare('INSERT INTO partner_sessions(token_hash,partner_id,auth_version,credential_version,expires_at) SELECT ?,id,auth_version,?,? FROM partners WHERE id=? AND active=1 AND auth_version=?').bind(tokenHash, await hash(env.PARTNER_AUTH_SECRET!), Date.now() + LOGIN_SESSION_SECONDS * 1000, challenge.partner_id, challenge.auth_version).run()
    if (!await env.INTEREST_DB.prepare('SELECT token_hash FROM partner_sessions WHERE token_hash=?').bind(tokenHash).first()) throw new ApiError('账号已更新，请重新登录。', 401)
    return json({ ok: true }, 200, { 'Set-Cookie': cookie(request, 'partner', token, LOGIN_SESSION_SECONDS) })
  }
  return null
}
