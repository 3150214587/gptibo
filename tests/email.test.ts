import test from 'node:test'
import assert from 'node:assert/strict'
import { partnerInvitationMessage, sendPartnerInvitationEmail, verificationMessage, sendVerificationEmail } from '../worker/email.ts'
import type { Env } from '../worker/platform.ts'

test('verification mail preserves leading zeroes, purpose and fallback without exposing a code in the inbox preview', () => {
  for (const kind of ['partner-login', 'order-recovery', 'admin-login'] as const) {
    const mail = verificationMessage(kind, '001234')
    assert.match(mail.html, /001234/)
    const preview = mail.html.match(/data-skip-in-text="true">([^<]*)/)?.[1]
    assert.ok(preview)
    assert.doesNotMatch(preview, /001234/)
    assert.match(mail.text, /001234/)
    assert.doesNotMatch(mail.html + mail.text, /__RECHARGE_VERIFICATION_CODE__/)
    assert.match(mail.html, /lang="zh-CN"/)
    assert.match(mail.html, /prefers-color-scheme: dark/)
    assert.match(mail.html, /https:\/\/shop.example.invalid\/email-logo.png/)
    assert.doesNotMatch(mail.html, /<script|<svg|<iframe|onclick=/i)
    assert.ok(mail.html.length < 100000, 'Stay below Gmail clipping threshold')
    assert.ok(mail.text.includes(kind === 'order-recovery' ? '订单' : kind === 'admin-login' ? '管理后台' : '代理工作台'))
    if (kind === 'partner-login') assert.match(mail.text, /https:\/\/shop.example.invalid\/join/)
    assert.match(mail.text, /10 分钟/)
  }
  assert.throws(() => verificationMessage('partner-login', '<img>'))
  assert.throws(() => verificationMessage('partner-login', '12345'))
})

test('partner invitation contains a durable login link and instructions without an OTP or expiry', async t => {
  const mail = partnerInvitationMessage()
  assert.match(mail.text, /账号已开通/)
  assert.match(mail.text, /https:\/\/shop.example.invalid\/join/)
  assert.match(mail.text, /获取验证码/)
  assert.match(mail.html, /进入代理工作台/)
  assert.doesNotMatch(mail.html + mail.text, /__RECHARGE_VERIFICATION_CODE__|10 分钟|身份验证/)
  assert.doesNotMatch(mail.text, /\b\d{6}\b/)
  assert.match(mail.html, /prefers-color-scheme: dark/)
  assert.doesNotMatch(mail.html, /<script|<svg|<iframe|onclick=/i)
  const env = { RESEND_FROM: 'GPTibo <noreply@shop.example.invalid>', RESEND_API_KEY: 'test' } as Env
  t.mock.method(globalThis, 'fetch', async (_url: string, options: RequestInit) => {
    assert.equal(new Headers(options.headers).get('Idempotency-Key'), 'partner-welcome/invitation')
    const payload = JSON.parse(String(options.body))
    assert.deepEqual(payload.to, ['partner@example.test'])
    assert.equal(payload.html, mail.html)
    assert.equal(payload.text, mail.text)
    return Response.json({ id: 'invitation-mail' })
  })
  await sendPartnerInvitationEmail(env, 'partner@example.test', 'invitation')
})

test('Resend receives HTML and text together with stable idempotency and delivery errors propagate', async t => {
  const env = { RESEND_FROM: 'GPTibo <noreply@shop.example.invalid>', RESEND_API_KEY: 'test' } as Env
  t.mock.method(globalThis, 'fetch', async (url: string, options: RequestInit) => {
    assert.equal(url, 'https://api.resend.com/emails')
    assert.equal(new Headers(options.headers).get('Idempotency-Key'), 'order-recovery/challenge')
    const mail = JSON.parse(String(options.body))
    assert.deepEqual(mail.to, ['buyer@example.test'])
    assert.match(mail.html, /001234/)
    assert.match(mail.text, /001234/)
    assert.match(mail.subject, /订单找回/)
    return new Response('{}', { status: 200 })
  })
  await sendVerificationEmail(env, 'order-recovery', 'buyer@example.test', '001234', 'challenge')
  t.mock.method(globalThis, 'fetch', async () => new Response('', { status: 500 }))
  await assert.rejects(sendVerificationEmail(env, 'partner-login', 'buyer@example.test', '001234', 'challenge'))
})
