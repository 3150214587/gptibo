import test from 'node:test'
import assert from 'node:assert/strict'
import { credentialExpiryProblem } from '../shared/credential-expiry.ts'
import { checkoutSession } from '../src/lib/checkout-session.ts'

const jwt = (claims: unknown) => 'eyJhbGciOiJub25lIn0.' + Buffer.from(JSON.stringify(claims)).toString('base64url') + '.fixture'
const now = Date.parse('2026-10-11T00:00:00Z')

test('new credentials reject either expired token or session, including the exact boundary', () => {
  for (const exp of [0, -1, now / 1000 - 1, now / 1000]) assert.match(credentialExpiryProblem(jwt({ exp }), '2099-01-01T00:00:00Z', now), /Access Token 已过期/)
  assert.match(credentialExpiryProblem(jwt({ exp: now / 1000 + 60 }), new Date(now).toISOString(), now), /Session 已超过有效期/)
  assert.equal(credentialExpiryProblem(jwt({ exp: now / 1000 + 60 }), '2099-01-01T00:00:00Z', now), '')
})

test('unknown dates and unreadable tokens do not fabricate an expiry verdict', () => {
  for (const token of ['opaque', 'a.%%.b', jwt({}), jwt({ exp: null }), jwt({ exp: '1' }), jwt(null), jwt({ exp: 1e100 })]) {
    assert.equal(credentialExpiryProblem(token, 'not-a-date', now), '')
  }
})

test('checkout rejects expired input while original session and token recoveries preserve exact bytes', () => {
  const token = jwt({ exp: 1 }), raw = '  ' + JSON.stringify({ user: { email: 'buyer@example.test' }, accessToken: token, expires: '2000-01-01T00:00:00Z' }) + '\n'
  assert.equal(checkoutSession(raw).credential, null)
  assert.match(checkoutSession(raw).problem, /已过期/)
  assert.deepEqual(checkoutSession(raw, 'session').credential, { mode: 'session', session: raw })
  assert.deepEqual(checkoutSession(raw, 'access_token').credential, { mode: 'access_token', access_token: token })
})
