import test from 'node:test'
import assert from 'node:assert/strict'
import { generateTotpSecret, totpCode, validTotpSecret, verifyTotp } from '../worker/totp.ts'

const secret = 'GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ' // RFC test key: 12345678901234567890
test('TOTP matches RFC 6238 SHA-1 vectors, including dates beyond 2038', async () => {
  for (const [seconds, expected] of [[59, '287082'], [1111111109, '081804'], [1111111111, '050471'], [1234567890, '005924'], [2000000000, '279037'], [20000000000, '353130']] as const) {
    assert.equal(await totpCode(secret, Math.floor(seconds / 30)), expected)
  }
})
test('TOTP accepts one step of clock drift and rejects expired or malformed codes', async () => {
  const now = 1234567890000; const step = Math.floor(now / 30000)
  for (const delta of [-1, 0, 1]) assert.equal(await verifyTotp(secret, await totpCode(secret, step + delta), now), step + delta)
  for (const delta of [-2, 2]) assert.equal(await verifyTotp(secret, await totpCode(secret, step + delta), now), null)
  for (const code of [123456, '12345', '1234567', '123 456', null, 'abcdef']) assert.equal(await verifyTotp(secret, code, now), null)
  assert.equal(validTotpSecret('invalid'), false)
  assert.ok(validTotpSecret(generateTotpSecret()))
  assert.notEqual(generateTotpSecret(), generateTotpSecret())
})
