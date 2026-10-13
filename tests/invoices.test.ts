import test from 'node:test'
import assert from 'node:assert/strict'
import { generateKeyPairSync, sign, verify } from 'node:crypto'
import worker from '../worker/index.ts'
import { customerFixture } from './customer-fixture.ts'
import { canonical } from '../worker/payments/alipay.ts'
import { reconcileInvoicePayments } from '../worker/invoices.ts'
import { invoicePlatformPayload } from '../worker/invoice-platform.ts'
import { invoiceAmounts, parseInvoiceAmount, type Invoice } from '../shared/invoices.ts'

const keys = generateKeyPairSync('rsa', { modulusLength: 2048 })
const config = {
  ALIPAY_APP_ID: '2026000000000000', ALIPAY_SELLER_ID: '2088000000000000',
  ALIPAY_APP_PRIVATE_KEY: keys.privateKey.export({ type: 'pkcs8', format: 'pem' }).toString(),
  ALIPAY_PUBLIC_KEY: keys.publicKey.export({ type: 'spki', format: 'pem' }).toString(),
  PAYMENT_NOTIFY_ORIGIN: 'https://shop.example.invalid',
}
const signature = (raw: string) => sign('RSA-SHA256', Buffer.from(raw), keys.privateKey).toString('base64')
type Context = Parameters<Parameters<typeof test>[1]>[0]
async function setup(t: Context) {
  const s = await customerFixture(); t.after(() => s.db.close())
  Object.assign(s.env, config)
  const agent = await s.agent('开票代理'); const other = await s.agent('另一位代理')
  const draft = { key: crypto.randomUUID(), title: '测试企业有限公司', taxNumber: '记录原样税号', invoiceType: 'ordinary', email: 'invoice@example.test', baseCents: 14000, includeFee: true }
  const create = (data: unknown = draft, cookie = agent.cookie) => s.request('/api/partner/invoices', data, cookie)
  const pay = (id = draft.key, action = 'create', cookie = agent.cookie) => s.request(`/api/partner/invoices/${id}/payment`, { action }, cookie)
  const payment = () => s.db.prepare('SELECT * FROM invoice_payments WHERE invoice_id=? ORDER BY created_at DESC').get(draft.key)!
  const invoice = () => s.db.prepare('SELECT * FROM partner_invoices WHERE id=?').get(draft.key)!
  async function notify(patch: Record<string, string> = {}) {
    const p = payment()
    const values = { app_id: config.ALIPAY_APP_ID, seller_id: config.ALIPAY_SELLER_ID, sign_type: 'RSA2', out_trade_no: String(p.id), total_amount: '7.00', trade_status: 'TRADE_SUCCESS', trade_no: '202610010000000001', ...patch }
    const form = new URLSearchParams({ ...values, sign: signature(canonical(values, ['sign', 'sign_type'])) })
    return worker.fetch(new Request('https://shop.example.invalid/api/invoices/notify/alipay', { method: 'POST', headers: { 'Content-Type': 'application/x-www-form-urlencoded' }, body: form }), s.env)
  }
  return { ...s, agent, other, draft, create, pay, payment, invoice, notify }
}
function gateway(t: Context, handler: (method: string, biz: Record<string, unknown>) => Record<string, unknown> | Promise<Record<string, unknown>>) {
  t.mock.method(globalThis, 'fetch', async (url: string, options: RequestInit) => {
    assert.equal(url, 'https://openapi.alipay.com/gateway.do')
    const params = Object.fromEntries(new URLSearchParams(String(options.body)))
    assert.ok(verify('RSA-SHA256', Buffer.from(canonical(params)), keys.publicKey, Buffer.from(params.sign, 'base64')))
    if (params.method === 'alipay.trade.precreate') assert.equal(params.notify_url, 'https://shop.example.invalid/api/invoices/notify/alipay')
    const raw = JSON.stringify(await handler(params.method, JSON.parse(params.biz_content)))
    return new Response(`{"${params.method.replaceAll('.', '_')}_response":${raw},"sign":"${signature(raw)}"}`)
  })
}
const precreate = (_method: string, biz: Record<string, unknown>) => ({ code: '10000', out_trade_no: biz.out_trade_no, qr_code: 'https://qr.alipay.com/invoice-fixture' })

test('140 yuan always charges 7, with an optional 147 yuan invoice and exact cent rounding', () => {
  assert.deepEqual(invoiceAmounts(14000, false), { baseCents: 14000, feeCents: 700, invoiceCents: 14000 })
  assert.deepEqual(invoiceAmounts(14000, true), { baseCents: 14000, feeCents: 700, invoiceCents: 14700 })
  assert.equal(invoiceAmounts(14010, true).feeCents, 701)
  assert.equal(invoiceAmounts(10, false).feeCents, 1)
  assert.equal(parseInvoiceAmount('140.10'), 14010)
  for (const amount of ['1e2', '-1', '140.001', '', '0.01', '100000.01', 'NaN']) assert.equal(parseInvoiceAmount(amount), null)
})
test('server snapshots amounts, preserves title info and makes submission idempotent', async t => {
  const s = await setup(t)
  assert.equal((await s.create({ ...s.draft, feeCents: 1, invoiceCents: 1, partnerId: s.other.id })).status, 201)
  assert.equal((await s.create()).status, 201)
  const i = s.invoice(); assert.equal(i.partner_id, s.agent.id); assert.equal(i.fee_cents, 700); assert.equal(i.invoice_cents, 14700); assert.equal(i.tax_number, s.draft.taxNumber)
  assert.equal(s.db.prepare('SELECT COUNT(*) n FROM invoice_events').get()!.n, 1)
  assert.equal((await s.create({ ...s.draft, includeFee: false })).status, 409)
  const unselected = await (await s.create({ ...s.draft, key: crypto.randomUUID(), includeFee: false })).json()
  assert.equal(unselected.invoice.invoice_cents, 14000); assert.equal(unselected.invoice.fee_cents, 700)
  assert.equal(s.db.prepare('SELECT COUNT(*) n FROM orders').get()!.n, 0)
})
test('authentication, ownership, same-origin and numeric validations protect invoice records', async t => {
  const s = await setup(t)
  assert.equal((await s.request('/api/partner/invoices')).status, 401)
  assert.equal((await s.request('/api/admin/invoices')).status, 401)
  assert.equal((await s.create()).status, 201)
  assert.equal((await s.request('/api/partner/invoices/' + s.draft.key, undefined, s.other.cookie)).status, 404)
  assert.equal((await s.pay(s.draft.key, 'create', s.other.cookie)).status, 404)
  assert.equal((await s.create(s.draft, s.other.cookie)).status, 409)
  assert.equal((await s.request('/api/partner/invoices', { ...s.draft, key: crypto.randomUUID() }, s.agent.cookie, 'https://foreign.test')).status, 403)
  for (const baseCents of [0, -100, 100.5, 10000001, '14000']) assert.equal((await s.create({ ...s.draft, key: crypto.randomUUID(), baseCents })).status, 400)
  assert.equal((await s.create({ ...s.draft, key: crypto.randomUUID(), includeFee: 'true' })).status, 400)
  for (const taxNumber of [undefined, '', '   ']) assert.equal((await s.create({ ...s.draft, key: crypto.randomUUID(), taxNumber })).status, 400)
  assert.equal((await s.create({ ...s.draft, key: crypto.randomUUID(), invoiceType: 'special' })).status, 400)
  const list = await (await s.request('/api/partner/invoices', undefined, s.other.cookie)).json(); assert.equal(list.total, 0)
  const own = await (await s.request('/api/partner/invoices', undefined, s.agent.cookie)).json(); assert.equal(own.rows[0].lease_token, undefined)
})
test('precreate uses our Alipay, charges fee only and serializes duplicate clicks', async t => {
  const s = await setup(t); await s.create(); let calls = 0
  gateway(t, async (_method, biz) => { calls++; assert.equal(biz.total_amount, '7.00'); assert.equal(biz.seller_id, config.ALIPAY_SELLER_ID); await new Promise(resolve => setTimeout(resolve, 30)); return precreate(_method, biz) })
  const responses = await Promise.all([s.pay(), s.pay()]); assert.deepEqual(responses.map(r => r.status).sort(), [200, 409]); assert.equal(calls, 1)
  assert.equal((await s.pay()).status, 200); assert.equal(calls, 1)
  assert.equal(s.payment().amount_cents, 700)
})
test('payment configuration errors retain the same application for retry', async t => {
  const s = await setup(t); await s.create(); delete s.env.ALIPAY_APP_PRIVATE_KEY
  assert.equal((await s.pay()).status, 503); assert.equal(s.invoice().status, 'awaiting_payment')
  assert.equal(s.db.prepare('SELECT COUNT(*) n FROM invoice_payments').get()!.n, 0)
  Object.assign(s.env, config); gateway(t, precreate)
  assert.equal((await s.pay()).status, 200)
})
test('signed receipt advances once, rejects wrong amount/account and cannot be undone by close', async t => {
  const s = await setup(t); await s.create(); gateway(t, precreate); await s.pay()
  for (const patch of [{ total_amount: '147.00' }, { seller_id: 'other' }, { app_id: 'other' }]) assert.equal((await s.notify(patch)).status, 400)
  assert.equal(s.invoice().status, 'awaiting_payment')
  assert.equal((await s.notify()).status, 200); assert.equal((await s.notify()).status, 200)
  assert.equal(s.invoice().status, 'awaiting_submission'); assert.equal(s.payment().state, 'paid'); assert.equal(s.payment().qr, null)
  assert.equal(s.db.prepare("SELECT COUNT(*) n FROM invoice_events WHERE action='paid'").get()!.n, 1)
  assert.equal((await s.notify({ trade_status: 'TRADE_CLOSED' })).status, 200); assert.equal(s.invoice().status, 'awaiting_submission')
  const paid = await (await s.pay()).json(); assert.equal(paid.payment.qr, null)
  assert.equal(s.db.prepare('SELECT COUNT(*) n FROM invoice_payments').get()!.n, 1)
})
test('a receipt and its history roll back atomically and notification retries recover', async t => {
  const s = await setup(t); await s.create(); gateway(t, precreate); await s.pay()
  s.db.exec("CREATE TRIGGER invoice_fail BEFORE INSERT ON invoice_events WHEN NEW.action='paid' BEGIN SELECT RAISE(ABORT,'fixture'); END")
  assert.equal((await s.notify()).status, 400); assert.equal(s.payment().state, 'pending'); assert.equal(s.invoice().status, 'awaiting_payment')
  s.db.exec('DROP TRIGGER invoice_fail')
  assert.equal((await s.notify()).status, 200); assert.equal(s.invoice().status, 'awaiting_submission')
})
test('cron recovers a missing callback through a signed query', async t => {
  const s = await setup(t); await s.create(); gateway(t, (method, biz) => method === 'alipay.trade.precreate' ? precreate(method, biz) : { code: '10000', out_trade_no: biz.out_trade_no, total_amount: '7.00', trade_status: 'TRADE_SUCCESS', trade_no: '202610010000000002' }); await s.pay()
  assert.deepEqual(await reconcileInvoicePayments(s.env), { checked: 1, failed: 0 })
  assert.equal(s.invoice().status, 'awaiting_submission')
  assert.equal(s.payment().notified_at, null)
})
test('expired codes wait for confirmed closure before a new payment attempt', async t => {
  const s = await setup(t); await s.create(); let creates = 0
  gateway(t, (method, biz) => { if (method === 'alipay.trade.precreate') { creates++; return precreate(method, biz) } return { code: '40004', sub_code: 'ACQ.TRADE_NOT_EXIST' } }); await s.pay()
  s.db.prepare('UPDATE invoice_payments SET expires_at=?').run(Date.now() - 1000)
  assert.equal((await s.pay()).status, 200); assert.equal(creates, 1); assert.equal(s.payment().state, 'pending')
  s.db.prepare('UPDATE invoice_payments SET expires_at=?').run(Date.now() - 121000)
  assert.equal((await s.pay()).status, 200); assert.equal(creates, 2)
  assert.equal(s.db.prepare("SELECT COUNT(*) n FROM invoice_payments WHERE state='closed'").get()!.n, 1)
})
test('uncertain gateway failure keeps a payment pending and retries do not double-charge', async t => {
  const s = await setup(t); await s.create(); let unavailable = false; let creates = 0
  gateway(t, (method, biz) => { if (unavailable) throw new Error('offline'); creates++; return precreate(method, biz) }); await s.pay()
  unavailable = true; assert.equal((await s.pay(s.draft.key, 'query')).status, 503)
  assert.equal(s.payment().state, 'pending'); unavailable = false
  assert.equal((await s.pay()).status, 200); assert.equal(creates, 1)
})
test('a late receipt for an expired attempt closes any newer pending code during reconciliation', async t => {
  const s = await setup(t); await s.create(); let closeConfirmed = false
  gateway(t, (method, biz) => {
    if (method === 'alipay.trade.precreate') return precreate(method, biz)
    if (method === 'alipay.trade.close' && closeConfirmed) return { code: '10000', out_trade_no: biz.out_trade_no }
    return { code: '40004', sub_code: 'ACQ.TRADE_NOT_EXIST' }
  })
  await s.pay(); const oldId = String(s.payment().id)
  s.db.prepare('UPDATE invoice_payments SET expires_at=?').run(Date.now() - 121000)
  await s.pay(); const newId = String(s.payment().id); assert.notEqual(newId, oldId)
  assert.equal((await s.notify({ out_trade_no: oldId })).status, 200)
  closeConfirmed = true
  assert.deepEqual(await reconcileInvoicePayments(s.env), { checked: 1, failed: 0 })
  assert.equal(s.db.prepare('SELECT state FROM invoice_payments WHERE id=?').get(newId)!.state, 'closed')
  assert.equal(s.invoice().status, 'awaiting_submission')
})
test('failed admin history write rolls back platform state and permits a safe retry', async t => {
  const s = await setup(t); await s.create(); gateway(t, precreate); await s.pay(); await s.notify()
  const path = '/api/admin/invoices/' + s.draft.key + '/submit'
  s.db.exec("CREATE TRIGGER submit_fail BEFORE INSERT ON invoice_events WHEN NEW.action='submitted' BEGIN SELECT RAISE(ABORT,'fixture'); END")
  assert.equal((await s.request(path, { version: 1 }, s.admin)).status, 503)
  assert.equal(s.invoice().status, 'awaiting_submission'); assert.equal(s.invoice().version, 1)
  s.db.exec('DROP TRIGGER submit_fail')
  assert.equal((await s.request(path, { version: 1 }, s.admin)).status, 200)
})
test('manual platform transitions require verified payment and current version; payload uses final amount', async t => {
  const s = await setup(t); await s.create(); const path = '/api/admin/invoices/' + s.draft.key
  assert.equal((await s.request(path + '/submit', { version: 0 }, s.admin)).status, 409)
  assert.equal((await s.request(path + '/submit', { version: 0 }, s.agent.cookie)).status, 401)
  gateway(t, precreate); await s.pay(); await s.notify()
  assert.equal((await s.request(path + '/complete', { version: 1 }, s.admin)).status, 409)
  assert.equal((await s.request(path + '/submit', { version: 0 }, s.admin)).status, 409)
  assert.equal((await s.request(path + '/submit', { version: 1, reference: 'PLATFORM-147', note: '已人工提交' }, s.admin)).status, 200)
  assert.equal((await s.request(path + '/submit', { version: 1 }, s.admin)).status, 409)
  assert.equal((await s.request(path + '/complete', { version: 2 }, s.admin)).status, 200)
  const detail = await (await s.request(path, undefined, s.admin)).json()
  assert.equal(detail.invoice.status, 'completed'); assert.equal(detail.invoice.platform_reference, 'PLATFORM-147')
  assert.equal(detail.submission.amountCents, 14700); assert.equal(detail.submission.title, s.draft.title)
  assert.equal(invoicePlatformPayload(s.invoice() as unknown as Invoice).amountCents, 14700)
  assert.equal(detail.events.length, 4)
  const filter = await (await s.request('/api/admin/invoices?status=completed&q=' + encodeURIComponent(s.draft.title), undefined, s.admin)).json(); assert.equal(filter.total, 1)
})
