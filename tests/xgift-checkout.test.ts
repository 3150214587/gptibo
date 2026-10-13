import test from 'node:test'
import assert from 'node:assert/strict'
import { customerFixture } from './customer-fixture.ts'
import { fulfillXGift } from '../worker/xgift/service.ts'
import { applyPaymentResult, findPayment } from '../worker/payments/service.ts'
import { signature } from '../shared/xgift-signature.ts'
type Context = Parameters<Parameters<typeof test>[1]>[0]
async function fixture(t: Context) {
  const s = await customerFixture(); t.after(() => s.db.close())
  Object.assign(s.env, { ORDER_TESTING_ENABLED: 'false', XGIFT_CHECKOUT_ENABLED: 'true', XGIFT_API_BASE: 'https://x-api.example.test/v1', XGIFT_USER_ID: 'usr_fixture', XGIFT_KEY_ID: 'key_fixture', XGIFT_CLIENT_SECRET: 'test-only-secret' })
  // Retail is configured independently of procurement points; match the confirmed live admin prices.
  s.db.exec("UPDATE catalog_plans SET price_cents=CASE id WHEN 'x-premium-3m' THEN 3500 ELSE 5500 END WHERE product_id='x-premium'")
  const state = { eligible: true, recipient: '12345', capacity: true, points: 1700, creates: 0, queries: 0, lost: false, missing: false, success: false, wrongResult: false, failCheck: false, duringCreate: undefined as (() => Promise<void>) | undefined }
  let submitted: Record<string, unknown> = {}
  t.mock.method(globalThis, 'fetch', async (input: string | URL, options: RequestInit) => {
    const url = new URL(String(input)), headers = new Headers(options.headers), raw = options.body ? String(options.body) : ''
    assert.equal(url.origin, 'https://x-api.example.test'); assert.equal(options.redirect, 'manual')
    assert.equal(headers.get('X-Signature'), await signature(s.env.XGIFT_CLIENT_SECRET!, options.method!, url, headers.get('X-Timestamp')!, headers.get('X-Nonce')!, s.env.XGIFT_KEY_ID!, headers.get('Idempotency-Key') ?? '', raw))
    if (url.pathname === '/v1/eligibility') {
      if (state.failCheck) throw Error('upstream cookie-private')
      return Response.json({ data: { username: JSON.parse(raw).username, recipient_id: state.recipient, eligible: state.eligible, reason: 'not_eligible' } })
    }
    if (url.pathname === '/v1/products') return Response.json({ data: [{ code: 'x-premium-3m', enabled: 1, points: state.points }, { code: 'x-premium-6m', enabled: 1, points: 3400 }] })
    if (url.pathname === '/v1/balance') return Response.json({ data: { available: 3400, frozen: 0 } })
    if (url.pathname === '/v1/capabilities') return Response.json({ data: { accepts_orders: state.capacity } })
    assert.equal(url.pathname, '/v1/orders')
    if (options.method === 'POST') {
      state.creates++; submitted = JSON.parse(raw)
      assert.equal(headers.get('Idempotency-Key'), 'gptibo:' + submitted.merchant_order_no)
      assert.equal(submitted.recipient_id, '12345'); assert.equal(submitted.expected_points, submitted.product_code === 'x-premium-3m' ? 1700 : 3400)
      await state.duringCreate?.()
      if (state.lost) throw Error('lost accepted reply')
      state.missing = false
    } else {
      state.queries++; assert.equal(url.searchParams.get('merchant_order_no'), submitted.merchant_order_no)
      if (state.missing) return Response.json({ error: { code: 'not_found', message: 'fixture-private-error' } }, { status: 404 })
    }
    return Response.json({ data: { id: 'ord_' + 'a'.repeat(32), merchant_order_no: submitted.merchant_order_no, product_code: submitted.product_code, recipient: state.wrongResult ? 'other' : submitted.recipient,
      points: submitted.expected_points, currency: 'bdt', amount_minor: submitted.product_code === 'x-premium-3m' ? 30000 : 60000, status: state.success ? 'succeeded' : 'running', receipt: state.success ? 'cs_live_fixture123' : null, failure_code: null } })
  })
  const buyer = (await s.request('/api/orders/client', {})).headers.get('set-cookie')!.split(';')[0]
  async function check(plan = 'x-premium-3m', cookie = buyer) {
    return s.request('/api/orders/xgift/eligibility', { username: '@Receiver', plan }, cookie)
  }
  async function draft(plan = 'x-premium-3m') {
    const result = await check(plan); assert.equal(result.status, 200, await result.clone().text())
    return { key: crypto.randomUUID(), plan, expectedPrice: plan === 'x-premium-3m' ? 3500 : 5500, email: 'buyer@example.test', username: '@Receiver', preflightToken: (await result.json()).token }
  }
  async function create(plan = 'x-premium-3m') {
    const data = await draft(plan), response = await s.request('/api/orders', data, buyer)
    assert.equal(response.status, 201, await response.clone().text()); return { data, order: (await response.json()).order }
  }
  async function pay(id: string) {
    s.db.prepare("INSERT INTO payments(order_id,provider,account,amount_cents,expires_at,created_at,updated_at) SELECT id,'alipay','fixture-alipay',price_cents,?,?,? FROM orders WHERE id=?").run(Date.now()+60000, Date.now(), Date.now(), id)
    const p = (await findPayment(s.env, id))!
    await applyPaymentResult(s.env, p, { orderId: id, amountCents: p.amount_cents, status: 'paid', tradeNo: 'trade-fixture' })
  }
  return { ...s, state, buyer, check, draft, create, pay }
}
test('eligibility remains server signed and order creation requires owner-bound, unexpired identity proof', async t => {
  const s = await fixture(t), data = await s.draft()
  const other = (await s.request('/api/orders/client', {})).headers.get('set-cookie')!.split(';')[0]
  for (const [body, cookie] of [[{ ...data, preflightToken: '' }, s.buyer], [{ ...data, username: 'other' }, s.buyer], [data, other]] as const) assert.equal((await s.request('/api/orders', body, cookie)).status, 409)
  s.db.exec('UPDATE xgift_preflights SET expires_at=0')
  assert.equal((await s.request('/api/orders', data, s.buyer)).status, 409)
  assert.equal(s.state.creates, 0); assert.equal(s.db.prepare('SELECT COUNT(*) n FROM orders').get()!.n, 0)
})
test('not eligible, unavailable and price drift fail before collecting payment', async t => {
  const s = await fixture(t)
  s.state.eligible = false; assert.equal((await (await s.check()).json()).eligible, false)
  s.state.eligible = true; s.state.failCheck = true
  const failed = await s.check(); assert.equal(failed.status, 503); assert.doesNotMatch(await failed.text(), /cookie-private/)
  s.state.failCheck = false; s.state.points = 1701; assert.equal((await s.check()).status, 503)
  s.state.points = 1700; s.state.capacity = false; assert.equal((await s.check()).status, 503)
  assert.equal(s.state.creates, 0)
})
test('35 yuan is immutable, unpaid orders never dispatch, multiple customers can order after acceptance', async t => {
  const s = await fixture(t), { order, data } = await s.create()
  assert.equal(order.price, 35); assert.equal(order.xgift.username, 'receiver')
  await fulfillXGift(s.env, order.id); assert.equal(s.state.creates, 0)
  assert.equal((await s.request('/api/orders', data, s.buyer)).status, 200)
  assert.equal((await s.request('/api/orders', { ...data, username: 'other' }, s.buyer)).status, 409)
  assert.equal((await s.request('/api/orders', await s.draft('x-premium-6m'), s.buyer)).status, 201)
  assert.equal(s.db.prepare('SELECT COUNT(*) n FROM orders').get()!.n, 2)
  assert.equal(s.state.creates, 0)
})
test('55 yuan plan dispatches only after verified payment and settles once through original order polling', async t => {
  const s = await fixture(t), { order } = await s.create('x-premium-6m')
  assert.equal(order.price, 55); s.state.lost = true
  await s.pay(order.id); assert.equal(s.state.creates, 1)
  assert.equal(s.db.prepare('SELECT state FROM xgift_purchases').get()!.state, 'unknown')
  s.state.success = true
  s.db.exec('UPDATE xgift_purchases SET next_check=0')
  await fulfillXGift(s.env, order.id); await fulfillXGift(s.env, order.id)
  assert.equal(s.state.creates, 1); assert.equal(s.state.queries, 1)
  const row = s.db.prepare('SELECT * FROM orders').get()!
  assert.equal(row.status, 'success'); assert.equal(row.cost_cents, 3400)
  assert.equal(s.db.prepare("SELECT COUNT(*) n FROM order_events WHERE action='xgift-progress' AND to_status='success'").get()!.n, 1)
  assert.equal((await s.request('/api/orders', await s.draft(), s.buyer)).status, 201)
  assert.equal(s.db.prepare('SELECT COUNT(*) n FROM orders').get()!.n, 2)
  assert.equal(s.state.creates, 1)
})

test('admin X partner switch controls catalog and orders; fulfillment preserves agreed supply price and profit', async t => {
  const s = await fixture(t), agent = await s.agent('X partner')
  s.db.prepare("INSERT INTO partner_prices(partner_id,plan,cost_cents,price_cents) VALUES (?,'x-premium-6m',4000,9900)").run(agent.id)
  const slug = agent.id.slice(0, 6)
  const catalogPath = '/api/catalog?partner=' + slug
  const offered = async () => (await (await s.request(catalogPath)).json()).plans.find((p: { id: string }) => p.id === 'x-premium-6m')
  assert.equal((await offered()).enabled, 0)
  const data = { ...await s.draft('x-premium-6m'), expectedPrice: 9900, partner: slug }
  assert.equal((await s.request('/api/orders', data, s.buyer)).status, 409)
  const settings = { priceCents: 5500, enabled: true, version: 0, note: 'Open X partner sales', partnerEnabled: true }
  assert.equal((await s.request('/api/admin/plans/x-premium-6m', settings)).status, 401)
  assert.equal((await s.request('/api/admin/plans/x-premium-6m', settings, s.admin)).status, 200)
  assert.equal((await offered()).enabled, 1)
  assert.equal((await offered()).price_cents, 9900)
  const response = await s.request('/api/orders', data, s.buyer)
  assert.equal(response.status, 201, await response.clone().text())
  const order = (await response.json()).order
  s.db.prepare("UPDATE partner_prices SET cost_cents=4500,version=version+1 WHERE partner_id=? AND plan='x-premium-6m'").run(agent.id)
  await s.pay(order.id)
  assert.equal(s.db.prepare('SELECT cost_cents FROM orders WHERE id=?').get(order.id)!.cost_cents, 4000)
  s.state.success = true; s.db.exec('UPDATE xgift_purchases SET next_check=0')
  await fulfillXGift(s.env, order.id)
  const saved = s.db.prepare('SELECT status,price_cents,cost_cents FROM orders WHERE id=?').get(order.id)!
  assert.equal(saved.status, 'success'); assert.equal(saved.price_cents, 9900); assert.equal(saved.cost_cents, 4000)
  const earnings = await (await s.request('/api/partner/orders', undefined, agent.cookie)).json()
  assert.equal(earnings.rows[0].profit_cents, 5900)
  assert.equal((await s.request('/api/admin/plans/x-premium-6m', { ...settings, version: 1, partnerEnabled: false }, s.admin)).status, 200)
  assert.equal((await offered()).enabled, 0)
  assert.equal(s.db.prepare('SELECT cost_cents FROM orders WHERE id=?').get(order.id)!.cost_cents, 4000)
})
test('changed recipient and tampered results never become successful gifts', async t => {
  const s = await fixture(t), { order } = await s.create()
  s.state.recipient = '999'
  await s.pay(order.id); assert.equal(s.state.creates, 0)
  assert.equal(s.db.prepare('SELECT status FROM orders').get()!.status, 'failed')
})
test('a create that never arrived is recovered only after a not-found query, with the same durable key', async t => {
  const s = await fixture(t), { order } = await s.create()
  s.state.missing = true; s.state.lost = true
  await s.pay(order.id); assert.equal(s.state.creates, 1)
  s.state.lost = false; s.state.success = true; s.db.exec('UPDATE xgift_purchases SET next_check=0')
  await fulfillXGift(s.env, order.id)
  assert.equal(s.state.queries, 1); assert.equal(s.state.creates, 2)
  assert.equal(s.db.prepare('SELECT status FROM orders').get()!.status, 'success')
})
test('active dispatch serializes concurrent calls and refuses a racing refund', async t => {
  const s = await fixture(t), { order } = await s.create()
  s.state.duringCreate = async () => {
    await fulfillXGift(s.env, order.id)
    const o = s.db.prepare('SELECT version FROM orders WHERE id=?').get(order.id)!
    const result = await s.request(`/api/orders/${order.id}/refunds`, { key: crypto.randomUUID(), reason: 'Fixture refund', version: o.version }, s.buyer)
    assert.equal(result.status, 409)
  }
  await s.pay(order.id); assert.equal(s.state.creates, 1)
  assert.equal(s.db.prepare('SELECT refund_hold FROM orders').get()!.refund_hold, 0)
  s.state.wrongResult = true; s.state.success = true; s.db.exec('UPDATE xgift_purchases SET next_check=0')
  await fulfillXGift(s.env, order.id)
  assert.equal(s.db.prepare('SELECT state FROM xgift_purchases').get()!.state, 'unknown')
  assert.notEqual(s.db.prepare('SELECT status FROM orders').get()!.status, 'success')
})
