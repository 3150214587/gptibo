import { sendVerificationEmail } from './email.ts'
import { ApiError, body, cookie, hash, json, LOGIN_SESSION_SECONDS, owner, randomToken, readCookie, type Env } from './platform.ts'
import { emailValue, mailConfigured, partnerLimit } from './partner-auth.ts'

export async function customerEmail(request: Request, env: Env) {
  const token = readCookie(request, 'customer')
  if (!token || !env.PARTNER_AUTH_SECRET) return null
  const session = await env.INTEREST_DB.prepare('SELECT email FROM customer_email_sessions WHERE token_hash=? AND expires_at>? AND credential_version=?').bind(await hash(token), Date.now(), await hash(env.PARTNER_AUTH_SECRET)).first<{ email: string }>()
  return session?.email ?? null
}
export async function buyerAccess(request: Request, env: Env) {
  return { ownerHash: await owner(request), email: await customerEmail(request, env) }
}
export async function recoveryRoutes(request: Request, env: Env, path: string) {
  if (!path.startsWith('/api/orders/recovery/')) return null
  if (path === '/api/orders/recovery/session' && request.method === 'GET') return json({ email: await customerEmail(request, env), mailReady: mailConfigured(env) })
  if (request.method !== 'POST' || !['/api/orders/recovery/send','/api/orders/recovery/verify','/api/orders/recovery/logout'].includes(path)) throw new ApiError('未找到找回接口。',404)
  const data = await body(request); const db = env.INTEREST_DB; const now = Date.now()
  if (path.endsWith('/logout')) {
    const token = readCookie(request, 'customer')
    if (token) await db.prepare('DELETE FROM customer_email_sessions WHERE token_hash=?').bind(await hash(token)).run()
    return json({ ok: true }, 200, { 'Set-Cookie': cookie(request, 'customer', '', 0) })
  }
  if (!mailConfigured(env)) throw new ApiError('邮箱找回暂未启用，请联系平台支持；此浏览器中的订单仍可正常查看。', 503)
  const ip = request.headers.get('CF-Connecting-IP') ?? 'local'
  if (path.endsWith('/send')) {
    const email = emailValue(data.email)
    await partnerLimit(env, 'recovery-ip:' + ip, 10, 3600000)
    await partnerLimit(env, 'recovery-minute:' + email, 1, 60000)
    await partnerLimit(env, 'recovery-hour:' + email, 5, 3600000)
    const id = crypto.randomUUID()
    if (await db.prepare('SELECT id FROM orders WHERE customer_email=? AND is_test=0 LIMIT 1').bind(email).first()) {
      let value: number
      do { value = crypto.getRandomValues(new Uint32Array(1))[0] } while (value >= 4294000000)
      const code = String(value % 1000000).padStart(6, '0')
      await db.batch([
        db.prepare('DELETE FROM customer_login_codes WHERE email=? OR expires_at<=?').bind(email, now),
        db.prepare('DELETE FROM customer_email_sessions WHERE expires_at<=?').bind(now),
        db.prepare('INSERT INTO customer_login_codes(id,email,code_hash,expires_at) VALUES (?,?,?,?)').bind(id, email, await hash(env.PARTNER_AUTH_SECRET + ':customer:' + id + ':' + code), now + 600000),
      ])
      try {
        await sendVerificationEmail(env, 'order-recovery', email, code, id)
      } catch {
        await db.prepare('DELETE FROM customer_login_codes WHERE id=?').bind(id).run()
        throw new ApiError('验证码暂时发送失败，请稍后重试。', 503)
      }
    }
    return json({ challenge: id, retryAfter: 60 })
  }
  if (path.endsWith('/verify')) {
    await partnerLimit(env, 'recovery-verify:' + ip, 20, 900000)
    if (typeof data.challenge !== 'string' || data.challenge.length !== 36 || typeof data.code !== 'string' || !/^\d{6}$/.test(data.code)) throw new ApiError('验证码无效或已过期。', 401)
    const challenge = await db.prepare('UPDATE customer_login_codes SET attempts=attempts+1 WHERE id=? AND attempts<5 AND used=0 AND expires_at>? RETURNING *').bind(data.challenge, now).first<{ email: string; code_hash: string }>()
    if (!challenge || challenge.code_hash !== await hash(env.PARTNER_AUTH_SECRET + ':customer:' + data.challenge + ':' + data.code)) throw new ApiError('验证码无效或已过期。', 401)
    const claimed = await db.prepare('UPDATE customer_login_codes SET used=1 WHERE id=? AND used=0 AND expires_at>? RETURNING email').bind(data.challenge, Date.now()).first()
    if (!claimed) throw new ApiError('验证码无效或已过期。', 401)
    const token = randomToken()
    await db.prepare('INSERT INTO customer_email_sessions(token_hash,email,credential_version,expires_at) VALUES (?,?,?,?)').bind(await hash(token), challenge.email, await hash(env.PARTNER_AUTH_SECRET!), Date.now() + LOGIN_SESSION_SECONDS * 1000).run()
    return json({ ok: true }, 200, { 'Set-Cookie': cookie(request, 'customer', token, LOGIN_SESSION_SECONDS) })
  }
  throw new ApiError('未找到找回接口。', 404)
}
