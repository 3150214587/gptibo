import test from 'node:test'
import assert from 'node:assert/strict'
import { publicAlipayQr } from '../shared/public-payment.ts'

test('browser and legacy API allow only official Alipay QR payloads', () => {
  const qr = 'https://qr.alipay.com/abcdefghijklmnop'
  assert.equal(publicAlipayQr(qr), qr)
  for (const value of [null, undefined, 1, '', 'https://recharge.example.invalid/payments/original?token=private', 'https://qr.alipay.com.evil.test/abcdefghijklmnop', qr + '?redirect=https://recharge.example.invalid', qr + '#private', qr + '\n', ' ' + qr, 'http://qr.alipay.com/abcdefghijklmnop', 'https://user@qr.alipay.com/abcdefghijklmnop']) {
    assert.equal(publicAlipayQr(value), null)
  }
})
