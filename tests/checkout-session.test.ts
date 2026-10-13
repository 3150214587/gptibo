import test from 'node:test'
import assert from 'node:assert/strict'
import { checkoutSession } from '../src/lib/checkout-session.ts'

test('Session input requires both a complete account and token before authorizing recharge', () => {
  for (const raw of ['{', 'null', '{}', '{"user":{"email":"buyer@example.test"}}', '{"user":{"email":"wrong"},"accessToken":"token"}', '{"user":{"email":"buyer@example.test"},"accessToken":" "}']) {
    const parsed = checkoutSession(raw)
    assert.equal(parsed.credential, null)
    assert.equal(parsed.email, '')
    assert.ok(parsed.problem)
  }
  assert.equal(checkoutSession('').problem, '')
})

test('Session recovery preserves exact JSON bytes for the original idempotent submission', () => {
  const original = '  { "user": { "email": "buyer@example.test" }, "accessToken": "private-token" }\n'
  const parsed = checkoutSession(original, 'session')
  assert.equal(parsed.email, 'buyer@example.test')
  assert.equal(parsed.problem, '')
  assert.deepEqual(parsed.credential, { mode: 'session', session: original })
})

test('an existing uncertain access-token submission can recover from Session without changing credential mode or token bytes', () => {
  const raw = JSON.stringify({ user: { email: 'buyer@example.test' }, accessToken: ' private-token ' })
  assert.deepEqual(checkoutSession(raw, 'access_token').credential, { mode: 'access_token', access_token: ' private-token ' })
  assert.deepEqual(checkoutSession(raw).credential, { mode: 'session', session: raw })
})
