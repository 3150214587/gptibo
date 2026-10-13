import test from 'node:test'
import assert from 'node:assert/strict'
import { customerFixture } from './customer-fixture.ts'
import { seedAdminEmailCode } from './admin-email-fixture.ts'
import { hash } from '../worker/platform.ts'
import { totpCode } from '../worker/totp.ts'

const THREE_DAYS_MS = 72 * 60 * 60 * 1000
const roles = [
  { name: 'admin', table: 'admin_sessions', session: '/api/admin/session', logout: '/api/admin/logout' },
  { name: 'partner', table: 'partner_sessions', session: '/api/partner/auth/session', logout: '/api/partner/auth/logout' },
  { name: 'customer', table: 'customer_email_sessions', session: '/api/orders/recovery/session', logout: '/api/orders/recovery/logout' },
] as const

for (const role of roles) test(`${role.name}: cookie and server session last exactly three days, while logout revokes immediately`, async t => {
  let now = Date.now()
  t.mock.method(Date, 'now', () => now)
  const s = await customerFixture(); t.after(() => s.db.close())
  Object.assign(s.env, { RESEND_API_KEY: 'fixture-mail-key', RESEND_FROM: 'fixture@example.test' })
  now += 30_000 // A fresh TOTP step after the fixture's administrator login.
  async function login() {
    if (role.name === 'admin') return s.request('/api/admin/login', {
      username: s.env.ADMIN_USERNAME, code: await totpCode(s.env.ADMIN_TOTP_SECRET!), ...await seedAdminEmailCode(s.db, s.env),
    })
    const id = crypto.randomUUID(), code = '001234', email = 'session@example.test'
    if (role.name === 'partner') {
      const p = await s.agent('Session agent')
      s.db.prepare('INSERT INTO partner_login_codes(id,partner_id,code_hash,auth_version,expires_at) VALUES (?,?,?,0,?)')
        .run(id, p.id, await hash(s.env.PARTNER_AUTH_SECRET + ':' + id + ':' + code), now + 600_000)
      return s.request('/api/partner/auth/verify', { challenge: id, code })
    }
    s.db.prepare('INSERT INTO customer_login_codes(id,email,code_hash,expires_at) VALUES (?,?,?,?)')
      .run(id, email, await hash(s.env.PARTNER_AUTH_SECRET + ':customer:' + id + ':' + code), now + 600_000)
    return s.request('/api/orders/recovery/verify', { challenge: id, code })
  }
  async function authenticated(cookie: string) {
    const response = await s.request(role.session, undefined, cookie)
    assert.equal(response.status, 200)
    const value = await response.json()
    return role.name === 'customer' ? value.email === 'session@example.test' : value.authenticated === true
  }
  const issuedAt = now, response = await login()
  assert.equal(response.status, 200, await response.clone().text())
  const header = response.headers.get('Set-Cookie')!
  assert.match(header, new RegExp(`^__Host-ymx_${role.name}=`))
  assert.match(header, /Max-Age=259200(?:;|$)/)
  assert.match(header, /HttpOnly/); assert.match(header, /SameSite=Strict/); assert.match(header, /Secure/)
  const cookie = header.split(';')[0], tokenHash = await hash(cookie.split('=')[1])
  assert.equal(s.db.prepare(`SELECT expires_at FROM ${role.table} WHERE token_hash=?`).get(tokenHash)!.expires_at, issuedAt + THREE_DAYS_MS)
  for (const elapsed of [8 * 60 * 60 * 1000 + 1, THREE_DAYS_MS - 1]) {
    now = issuedAt + elapsed
    assert.equal(await authenticated(cookie), true)
  }
  now = issuedAt + THREE_DAYS_MS
  assert.equal(await authenticated(cookie), false)
  // A new valid session can still be logged out well before its three-day expiry.
  const second = await login()
  assert.equal(second.status, 200, await second.clone().text())
  const secondCookie = second.headers.get('Set-Cookie')!.split(';')[0]
  now += 24 * 60 * 60 * 1000
  assert.equal(await authenticated(secondCookie), true)
  const logout = await s.request(role.logout, {}, secondCookie)
  assert.equal(logout.status, 200); assert.match(logout.headers.get('Set-Cookie')!, /Max-Age=0(?:;|$)/)
  assert.equal(await authenticated(secondCookie), false)
  assert.equal(s.db.prepare(`SELECT token_hash FROM ${role.table} WHERE token_hash=?`).get(await hash(secondCookie.split('=')[1])), undefined)
})
