import test from 'node:test'
import assert from 'node:assert/strict'
import { generateKeyPairSync } from 'node:crypto'
import { readFileSync } from 'node:fs'
import { DatabaseSync } from 'node:sqlite'
import { customerFixture } from './customer-fixture.ts'
import { call, mappings, DeliverySupplierReceiptRejected, usdMicros, validateProduct } from '../worker/delivery_supplier/client.ts'
import { fulfillDeliverySupplier, reconcileDeliverySupplier } from '../worker/delivery_supplier/service.ts'
import { applyPaymentResult, findPayment } from '../worker/payments/service.ts'

type Context = Parameters<Parameters<typeof test>[1]>[0]
const keys = generateKeyPairSync('rsa', { modulusLength: 2048 })
async function setup(t: Context, plan: keyof typeof mappings = 'gemini-pro-18m', amounts: { price?: number; actual?: number; balance?: number } = {}) {
  const s = await customerFixture(); t.after(() => s.db.close())
  Object.assign(s.env, { ORDER_TESTING_ENABLED: 'false', EMAIL_PRODUCTS_ENABLED: 'true', RESEND_API_KEY: 'mock-mail-key', RESEND_FROM: 'GPTibo <delivery@example.test>', PAYMENT_NOTIFY_ORIGIN: 'https://shop.example.invalid', ALIPAY_APP_ID: '2026000000000000', ALIPAY_SELLER_ID: '2088000000000000', ALIPAY_APP_PRIVATE_KEY: keys.privateKey.export({ type: 'pkcs8', format: 'pem' }).toString(), ALIPAY_PUBLIC_KEY: keys.publicKey.export({ type: 'spki', format: 'pem' }).toString(), DELIVERY_SUPPLIER_API_KEY: 'mock-delivery_supplier-key', DELIVERY_SUPPLIER_CHECKOUT_ENABLED: 'true', DELIVERY_SUPPLIER_CNY_PER_USD: '6.7' })
  const value = plan === 'gemini-pro-18m' ? 'https://claim.example.test/PRIVATE-CLAIM' : 'PRIVATE-CDK'
  const state = { posts: [] as { key: string | null; body: string }[], mails: [] as { key: string | null; body: string }[], losePost: false, loseMail: false, pending: false, refund: false, stock: 10, price: mappings[plan].maxUsdCents / 100, balance: 1.9, mismatch: false, actual: mappings[plan].maxUsdCents / 100, netDelta: 0, refundDelta: 0, badValue: false, duringPost: undefined as (() => Promise<void>) | undefined, ...amounts }
  t.mock.method(globalThis, 'fetch', async (url: string | URL | Request, options: RequestInit) => {
    const headers = new Headers(options.headers)
    if (String(url) === 'https://api.resend.com/emails') {
      state.mails.push({ key: headers.get('Idempotency-Key'), body: String(options.body) })
      if (state.loseMail) throw new Error('lost mail')
      return Response.json({ id: 'mail_delivery_supplier' })
    }
    assert.match(String(url), /^https:\/\/delivery\.example\.invalid\/api\/v1\//)
    assert.equal(headers.get('X-API-Key'), 'mock-delivery_supplier-key'); assert.equal(options.redirect, 'manual')
    assert.doesNotMatch(String(options.body), /customer@example.test/)
    if (String(url).endsWith('/products')) return Response.json({ services: Object.values(mappings).map(m => ({ service_id: m.id, name: 'Membership', price: m.id === mappings[plan].id ? state.price : m.maxUsdCents / 100, stock: state.stock, availability_state: 'available' })) })
    if (String(url).endsWith('/me')) return Response.json({ wallet_balance: state.balance })
    if (String(url).endsWith('/order')) {
      state.posts.push({ key: headers.get('Idempotency-Key'), body: String(options.body) })
      assert.deepEqual(JSON.parse(String(options.body)), { service_id: mappings[plan].id, quantity: 1, precheck: true, include_proofs: true })
      if (!/^[A-Za-z0-9_-]{8,128}$/.test(headers.get('Idempotency-Key') ?? '')) return Response.json({ error_code: 'INVALID_IDEMPOTENCY_KEY', error: 'PRIVATE-UPSTREAM-ERROR' }, { status: 400 })
      await state.duringPost?.()
      if (state.losePost) throw new Error('lost response')
      return Response.json({ order_id: 'API_TEST', order_status: 'pending' }, { status: 503 })
    }
    assert.ok(String(url).endsWith('/order/API_TEST'))
    return Response.json({ success: true, order: { order_id: state.mismatch ? 'API_OTHER' : 'API_TEST', service_id: mappings[plan].id, order_status: state.refund ? 'refunded' : state.pending ? 'pending' : 'success', requested_quantity: 1, delivered_quantity: state.refund || state.pending ? 0 : 1, charged_amount: state.actual, refunded_amount: (state.refund ? state.actual : 0) + state.refundDelta, net_charged_amount: (state.refund ? 0 : state.actual) + state.netDelta, partial_delivery: false, delivered_products: state.pending || state.refund ? [] : [state.badValue ? 'javascript:alert(1)' : value], retry_after_seconds: 120 } })
  })
  const buyer = (await s.request('/api/orders/client', {})).headers.get('set-cookie')!.split(';')[0]
  const create = () => s.request('/api/orders', { key: crypto.randomUUID(), plan, expectedPrice: 1990, email: 'customer@example.test' }, buyer)
  const response = await create(); assert.equal(response.status, 201, await response.clone().text())
  const order = (await response.json()).order
  async function pay() {
    s.db.prepare("INSERT INTO payments(order_id,provider,account,amount_cents,expires_at,created_at,updated_at) SELECT id,'alipay',?,price_cents,?,?,? FROM orders WHERE id=? ON CONFLICT DO NOTHING").run(s.env.ALIPAY_APP_ID!, Date.now() + 60000, Date.now(), Date.now(), order.id)
    await applyPaymentResult(s.env, (await findPayment(s.env, order.id))!, { orderId: order.id, amountCents: 1990, status: 'paid', tradeNo: 'trade_delivery_supplier' })
  }
  const ready = () => s.db.prepare('UPDATE delivery_supplier_orders SET next_check_at=0 WHERE order_id=?').run(order.id)
  const view = async () => (await (await s.request('/api/orders/' + order.id, undefined, buyer)).json()).order
  return { ...s, state, buyer, order, value, create, pay, ready, view }
}

test('supplier amounts preserve micro-USDT precision and reject malformed or excessive values', () => {
  assert.equal(usdMicros(0.445), 445000)
  assert.equal(usdMicros(0.600001), 600001)
  assert.equal(usdMicros(0), 0)
  for (const amount of ['0.445', null, undefined, true, {}, NaN, Infinity, -0.001, 0.4450001, Number.MAX_SAFE_INTEGER]) assert.throws(() => usdMicros(amount), /供应商金额格式异常/)
  const product = { service_id: mappings['gemini-pro-18m'].id, name: 'Membership', stock: 1, price: 0.445 }
  assert.equal(validateProduct([product], product.service_id, 60), product)
  assert.throws(() => validateProduct([{ ...product, price: 0.600001 }], product.service_id, 60), /采购价格已变化/)
})

test('precision migration preserves historical charges and CNY accounting', t => {
  const db = new DatabaseSync(':memory:'); t.after(() => db.close())
  db.exec('CREATE TABLE delivery_supplier_orders (max_usd_cents INTEGER, actual_usd_cents INTEGER, actual_cost_cents INTEGER)')
  db.exec('INSERT INTO delivery_supplier_orders VALUES(60,60,402),(38,38,255),(60,NULL,NULL)')
  db.exec(readFileSync(new URL('../migrations/0042_delivery_supplier_amount_precision.sql', import.meta.url), 'utf8'))
  assert.deepEqual(db.prepare('SELECT * FROM delivery_supplier_orders').all().map(row => ({ ...row })), [
    { max_usd_cents: 60, actual_usd_cents: 60, actual_cost_cents: 402, actual_usd_micros: 600000 },
    { max_usd_cents: 38, actual_usd_cents: 38, actual_cost_cents: 255, actual_usd_micros: 380000 },
    { max_usd_cents: 60, actual_usd_cents: null, actual_cost_cents: null, actual_usd_micros: null },
  ])
})

test('Gemini 0.445 USDT quote and receipt deliver once and book ¥2.98 from the exact charge', async t => {
  const s = await setup(t, 'gemini-pro-18m', { price: 0.445, actual: 0.445, balance: 0.600001 })
  await s.pay(); await s.pay(); await reconcileDeliverySupplier(s.env)
  assert.equal((await s.view()).status, 'success')
  assert.equal((await s.view()).delivery.value, s.value)
  assert.equal(s.state.posts.length, 1); assert.equal(s.state.mails.length, 1)
  const row = s.db.prepare('SELECT actual_usd_micros,actual_usd_cents,actual_cost_cents FROM delivery_supplier_orders WHERE order_id=?').get(s.order.id)!
  assert.equal(row.actual_usd_micros, 445000); assert.equal(row.actual_usd_cents, 45); assert.equal(row.actual_cost_cents, 298)
  const purchase = (await (await s.request(`/api/admin/orders/${s.order.id}/delivery_supplier`, undefined, s.admin)).json()).purchase
  assert.equal(purchase.actualUsdMicros, 445000)
  assert.equal(s.db.prepare('SELECT cost_cents FROM accounting_candidates WHERE order_id=?').get(s.order.id)!.cost_cents, 298)
})

test('sub-cent price, balance, net and refund discrepancies cannot bypass procurement checks', async t => {
  const s = await setup(t, 'gemini-pro-18m', { price: 0.445, actual: 0.445, balance: 0.599999 })
  await s.pay(); assert.equal(s.state.posts.length, 0)
  s.state.balance = 0.600001; s.state.actual = 0.600001; s.ready()
  await assert.rejects(fulfillDeliverySupplier(s.env, s.order.id), /采购金额/)
  s.state.actual = 0.445; s.state.netDelta = 0.000001; s.ready()
  await assert.rejects(fulfillDeliverySupplier(s.env, s.order.id), /采购金额/)
  s.state.netDelta = 0; s.state.refundDelta = 0.000001; s.ready()
  await assert.rejects(fulfillDeliverySupplier(s.env, s.order.id), /采购金额/)
  assert.equal((await s.view()).delivery.value, undefined); assert.equal(s.state.mails.length, 0)
  s.state.refund = true; s.ready()
  await fulfillDeliverySupplier(s.env, s.order.id)
  assert.equal(s.db.prepare('SELECT state FROM delivery_supplier_orders WHERE order_id=?').get(s.order.id)!.state, 'pending')
  s.state.refundDelta = 0; s.ready(); await fulfillDeliverySupplier(s.env, s.order.id)
  assert.equal(s.db.prepare('SELECT state FROM delivery_supplier_orders WHERE order_id=?').get(s.order.id)!.state, 'rejected')
  assert.equal(s.state.posts.length, 1)
})

for (const plan of Object.keys(mappings) as (keyof typeof mappings)[]) test(plan + ': paid purchase delivers the same item on-page and by email, with fixed FX and one charge', async t => {
  const s = await setup(t, plan)
  await fulfillDeliverySupplier(s.env, s.order.id); assert.equal(s.state.posts.length, 0)
  await s.pay(); await s.pay(); await reconcileDeliverySupplier(s.env)
  assert.equal(s.state.posts.length, 1); assert.equal(s.state.mails.length, 1)
  assert.match(s.state.posts[0].key!, /^[A-Za-z0-9_-]{8,128}$/)
  const result = await s.view(); assert.equal(result.status, 'success'); assert.equal(result.delivery.value, s.value)
  const mail = JSON.parse(s.state.mails[0].body); assert.deepEqual(mail.to, ['customer@example.test']); assert.ok(mail.text.includes(s.value))
  const cost = plan === 'gemini-pro-18m' ? 402 : 255
  assert.equal(s.db.prepare('SELECT cost_cents FROM accounting_candidates WHERE order_id=?').get(s.order.id)!.cost_cents, cost)
  s.env.DELIVERY_SUPPLIER_CNY_PER_USD = '9'
  assert.equal(s.db.prepare('SELECT actual_cost_cents FROM delivery_supplier_orders WHERE order_id=?').get(s.order.id)!.actual_cost_cents, cost)
  const report = await (await s.request('/api/admin/accounting/report', undefined, s.admin)).json()
  assert.equal(report.orders.find((o: { id: string }) => o.id === s.order.id).cost_cents, cost)
  assert.doesNotMatch(JSON.stringify(s.db.prepare('SELECT * FROM order_events').all()), /PRIVATE|mock-delivery_supplier/)
  const other = (await s.request('/api/orders/client', {})).headers.get('set-cookie')!.split(';')[0]
  assert.equal((await s.request('/api/orders/' + s.order.id, undefined, other)).status, 404)
  assert.equal((await s.request(`/api/admin/orders/${s.order.id}/delivery_supplier`, undefined, s.buyer)).status, 401)
  assert.equal((await s.request(`/api/admin/orders/${s.order.id}/delivery_supplier`, {}, s.admin, 'https://evil.test')).status, 403)
})

test('lost response recovers identical receipt/body; concurrent workers and refunds cannot race procurement', async t => {
  const s = await setup(t); s.state.losePost = true
  s.state.duringPost = async () => {
    await fulfillDeliverySupplier(s.env, s.order.id)
    const version = (await s.view()).version
    assert.equal((await s.request(`/api/orders/${s.order.id}/refunds`, { key: crypto.randomUUID(), version, reason: '退款' }, s.buyer)).status, 409)
    assert.equal((await s.request(`/api/admin/orders/${s.order.id}/actions`, { action: 'redemption-control', blocked: true, version, note: '暂停采购' }, s.admin)).status, 409)
  }
  await s.pay(); assert.equal(s.state.posts.length, 1); assert.equal((await s.view()).delivery.value, undefined)
  s.state.losePost = false; s.state.duringPost = undefined; s.ready()
  await reconcileDeliverySupplier(s.env)
  assert.equal(s.state.posts.length, 2); assert.deepEqual(s.state.posts[0], s.state.posts[1]); assert.equal((await s.view()).status, 'success')
})

test('pending 503 retains supplier id, honors poll delay, and never exposes pending items or purchases again', async t => {
  const s = await setup(t); s.state.pending = true
  await s.pay(); assert.equal(s.state.posts.length, 1); assert.equal((await s.view()).delivery.value, undefined)
  const row = s.db.prepare('SELECT * FROM delivery_supplier_orders WHERE order_id=?').get(s.order.id)!
  assert.equal(row.supplier_order, 'API_TEST'); assert.ok(Number(row.next_check_at) > Date.now() + 100000)
  s.state.pending = false; await reconcileDeliverySupplier(s.env); assert.equal(s.state.mails.length, 0)
  s.ready(); await reconcileDeliverySupplier(s.env); assert.equal((await s.view()).status, 'success'); assert.equal(s.state.posts.length, 1)
})

test('email timeout leaves direct delivery usable and retries immutable mail without another purchase', async t => {
  const s = await setup(t); s.state.loseMail = true; await s.pay()
  assert.equal((await s.view()).delivery.value, s.value); assert.equal((await s.view()).delivery.status, 'unknown')
  s.state.loseMail = false; s.ready(); await reconcileDeliverySupplier(s.env)
  assert.equal(s.state.posts.length, 1); assert.equal(s.state.mails.length, 2); assert.deepEqual(s.state.mails[0], s.state.mails[1]); assert.equal((await s.view()).status, 'success')
})

test('unpaid, test, demo, frozen and refunded orders never buy; stock, price, balance and kill switch stop new purchases', async t => {
  const s = await setup(t)
  for (const [field, value] of [['mode', 'demo'], ['is_test', 1], ['refund_hold', 1], ['refunded_cents', 1], ['redemption_blocked', 1]] as const) {
    s.db.prepare(`UPDATE orders SET status='session',payment_status=?,${field}=? WHERE id=?`).run(field === 'mode' ? 'simulated' : 'paid', value, s.order.id)
    await fulfillDeliverySupplier(s.env, s.order.id)
    s.db.prepare(`UPDATE orders SET payment_status='paid',${field}=? WHERE id=?`).run(field === 'mode' ? 'live' : 0, s.order.id)
  }
  assert.equal(s.state.posts.length, 0)
  s.state.stock = 0; assert.equal((await s.create()).status, 409)
  s.ready(); await assert.rejects(fulfillDeliverySupplier(s.env, s.order.id)); assert.equal(s.state.posts.length, 0)
  s.state.stock = 10; s.state.price = 9; assert.equal((await s.create()).status, 409)
  s.state.price = 0.6; s.state.balance = 0; s.ready(); await assert.rejects(fulfillDeliverySupplier(s.env, s.order.id)); assert.equal(s.state.posts.length, 0)
  s.state.balance = 1.9; s.env.DELIVERY_SUPPLIER_CHECKOUT_ENABLED = 'false'; s.ready(); await fulfillDeliverySupplier(s.env, s.order.id); assert.equal(s.state.posts.length, 0)
  assert.equal((await s.create()).status, 409)
})

test('refund, mismatched identity, excessive cost and unsafe delivery never release an item', async t => {
  const s = await setup(t); s.state.mismatch = true; await s.pay()
  assert.equal((await s.view()).delivery.value, undefined)
  s.state.mismatch = false; s.state.actual = 3; s.ready(); await assert.rejects(fulfillDeliverySupplier(s.env, s.order.id))
  s.state.actual = 0.6; s.state.badValue = true; s.ready(); await assert.rejects(fulfillDeliverySupplier(s.env, s.order.id))
  s.state.badValue = false; s.state.refund = true; s.ready(); await fulfillDeliverySupplier(s.env, s.order.id)
  assert.equal(s.db.prepare('SELECT state FROM delivery_supplier_orders WHERE order_id=?').get(s.order.id)!.state, 'rejected')
  assert.equal(s.state.mails.length, 0); assert.equal((await s.view()).delivery.value, undefined); assert.equal(s.state.posts.length, 1)
})

test('supplier credentials cannot rotate during recovery; automatic delivery cannot be manually substituted', async t => {
  const s = await setup(t); s.state.pending = true; await s.pay(); s.ready()
  s.env.DELIVERY_SUPPLIER_API_KEY = 'different-key'; await assert.rejects(fulfillDeliverySupplier(s.env, s.order.id)); assert.equal(s.state.posts.length, 1)
  const order = await s.view()
  assert.equal((await s.request(`/api/admin/orders/${order.id}/delivery`, { action: 'save', orderVersion: order.version, version: -1, value: 'https://example.test/substitute', instructions: 'new' }, s.admin)).status, 409)
})

test('redirects, HTML challenges and upstream errors never leak credentials or private response text', async t => {
  const s = await setup(t)
  for (const response of [new Response(null, { status: 302, headers: { Location: 'https://evil.test' } }), new Response('<html>PRIVATE</html>'), Response.json({ error: 'PRIVATE' }, { status: 500 })]) {
    t.mock.method(globalThis, 'fetch', async (_url: unknown, options: RequestInit) => { assert.equal(options.redirect, 'manual'); return response })
    await assert.rejects(call(s.env, '/products'), e => e instanceof Error && !e.message.includes('PRIVATE'))
  }
})

test('purchase audit rollback prevents spend and a saved receipt survives a worker crash', async t => {
  const s = await setup(t)
  s.db.exec("CREATE TRIGGER reject_delivery_supplier_audit BEFORE INSERT ON order_events WHEN NEW.action='delivery_supplier-purchase-start' BEGIN SELECT RAISE(ABORT,'audit failure'); END")
  await s.pay(); assert.equal(s.state.posts.length, 0)
  assert.equal(s.db.prepare('SELECT attempted_at FROM delivery_supplier_orders WHERE order_id=?').get(s.order.id)!.attempted_at, null)
  s.db.exec('DROP TRIGGER reject_delivery_supplier_audit')
  s.db.prepare("UPDATE delivery_supplier_orders SET state='creating',attempted_at=?,next_check_at=0 WHERE order_id=?").run(Date.now(), s.order.id)
  const receipt = s.db.prepare('SELECT request_key,request_body FROM delivery_supplier_orders WHERE order_id=?').get(s.order.id)!
  await reconcileDeliverySupplier(s.env)
  assert.equal(s.state.posts[0].key, receipt.request_key); assert.equal(s.state.posts[0].body, receipt.request_body)
  assert.equal((await s.view()).status, 'success')
})

test('manual no-purchase evidence stops uncertain retries and releases the original refund flow', async t => {
  const s = await setup(t); s.state.losePost = true; await s.pay()
  const endpoint = `/api/admin/orders/${s.order.id}/delivery_supplier`
  const purchase = (await (await s.request(endpoint, undefined, s.admin)).json()).purchase
  const data = { action: 'confirm-not-purchased', confirmed: true, note: '供应商已核实原请求无扣款无发货', orderVersion: (await s.view()).version, updatedAt: purchase.updatedAt }
  assert.equal((await s.request(endpoint, { ...data, confirmed: false }, s.admin)).status, 409)
  assert.equal((await s.request(endpoint, data, s.admin)).status, 200)
  s.ready(); await reconcileDeliverySupplier(s.env); assert.equal(s.state.posts.length, 1)
  const current = await s.view(); assert.equal(current.status, 'failed')
  assert.equal((await s.request(`/api/orders/${s.order.id}/refunds`, { key: crypto.randomUUID(), version: current.version, reason: '采购未发生，申请退款' }, s.buyer)).status, 201)
})

test('direct delivery is withheld during after-sales and partner APIs do not expose the purchased item', async t => {
  const s = await setup(t, 'duolingo-12m'); await s.pay()
  for (const field of ['refund_hold', 'refunded_cents', 'redemption_blocked']) {
    s.db.prepare(`UPDATE orders SET ${field}=1 WHERE id=?`).run(s.order.id)
    assert.equal((await s.view()).delivery.value, undefined)
    s.db.prepare(`UPDATE orders SET ${field}=0 WHERE id=?`).run(s.order.id)
  }
  const agent = await s.agent('Agent')
  s.db.prepare('UPDATE orders SET partner_id=? WHERE id=?').run(agent.id, s.order.id)
  const response = await s.request('/api/partner/orders', undefined, agent.cookie)
  assert.equal(response.status, 200); assert.doesNotMatch(await response.text(), /PRIVATE-CDK|mock-delivery_supplier-key/)
})

test('unattempted legacy receipts are corrected before a charge', async t => {
  const s = await setup(t)
  s.db.prepare('UPDATE delivery_supplier_orders SET request_key=? WHERE order_id=?').run('ymx/' + s.order.id, s.order.id)
  await s.pay()
  assert.equal(s.state.posts.length, 1); assert.equal(s.state.posts[0].key, 'ymx_' + s.order.id)
  assert.equal((await s.view()).status, 'success')
})

test('explicit legacy receipt rejection stops retries and guarded recovery delivers once', async t => {
  const s = await setup(t, 'duolingo-12m')
  s.db.prepare("UPDATE delivery_supplier_orders SET request_key=?,state='unknown',attempted_at=? WHERE order_id=?").run('ymx/' + s.order.id, Date.now(), s.order.id)
  await s.pay()
  assert.equal(s.state.posts[0].key, 'ymx/' + s.order.id)
  let order = await s.view()
  assert.equal(order.status, 'failed'); assert.equal(order.delivery.value, undefined)
  assert.equal(s.db.prepare('SELECT state FROM delivery_supplier_orders WHERE order_id=?').get(order.id)!.state, 'rejected')
  s.ready(); await reconcileDeliverySupplier(s.env); assert.equal(s.state.posts.length, 1)
  const endpoint = `/api/admin/orders/${order.id}/delivery_supplier`
  const purchase = (await (await s.request(endpoint, undefined, s.admin)).json()).purchase
  const data = { action: 'retry-rejected-receipt', confirmed: true, note: '供应商明确拒绝旧编号，核实无扣款无发货', orderVersion: order.version, updatedAt: purchase.updatedAt }
  assert.equal((await s.request(endpoint, data, s.buyer)).status, 401)
  assert.equal((await s.request(endpoint, data, s.admin, 'https://evil.test')).status, 403)
  assert.equal((await s.request(endpoint, { ...data, confirmed: false }, s.admin)).status, 409)
  assert.equal((await s.request(endpoint, { ...data, orderVersion: order.version - 1 }, s.admin)).status, 409)
  assert.equal((await s.request(endpoint, { ...data, updatedAt: purchase.updatedAt - 1 }, s.admin)).status, 409)
  s.db.prepare('UPDATE delivery_supplier_orders SET lease_until=? WHERE order_id=?').run(Date.now() + 60000, order.id)
  assert.equal((await s.request(endpoint, data, s.admin)).status, 409)
  s.db.prepare('UPDATE delivery_supplier_orders SET lease_until=0 WHERE order_id=?').run(order.id)
  s.db.prepare('UPDATE orders SET refund_hold=1 WHERE id=?').run(order.id)
  assert.equal((await s.request(endpoint, data, s.admin)).status, 409)
  s.db.prepare('UPDATE orders SET refund_hold=0 WHERE id=?').run(order.id)
  s.db.exec("CREATE TRIGGER reject_recovery_audit BEFORE INSERT ON order_events WHEN NEW.action='delivery_supplier-receipt-recovered' BEGIN SELECT RAISE(ABORT,'audit failure'); END")
  assert.equal((await s.request(endpoint, data, s.admin)).status, 503)
  assert.equal(s.db.prepare('SELECT request_key FROM delivery_supplier_orders WHERE order_id=?').get(order.id)!.request_key, 'ymx/' + order.id)
  s.db.exec('DROP TRIGGER reject_recovery_audit')
  assert.equal((await s.request(endpoint, data, s.admin)).status, 200)
  assert.equal((await s.request(endpoint, data, s.admin)).status, 409)
  await reconcileDeliverySupplier(s.env); await reconcileDeliverySupplier(s.env)
  order = await s.view(); assert.equal(order.status, 'success'); assert.equal(order.delivery.value, s.value)
  assert.equal(s.state.posts.length, 2); assert.equal(s.state.posts[1].key, 'ymx_' + order.id)
  assert.equal(s.state.mails.length, 1)
  assert.equal(s.db.prepare("SELECT COUNT(*) n FROM order_events WHERE order_id=? AND action='delivery_supplier-receipt-recovered'").get(order.id)!.n, 1)
  assert.doesNotMatch(JSON.stringify(s.db.prepare('SELECT * FROM order_events').all()), /PRIVATE-UPSTREAM-ERROR/)
})

test('uncertain requests and supplier refunds cannot replace their original receipt', async t => {
  const s = await setup(t); s.state.losePost = true; await s.pay()
  const endpoint = `/api/admin/orders/${s.order.id}/delivery_supplier`
  const recover = async () => {
    const purchase = (await (await s.request(endpoint, undefined, s.admin)).json()).purchase
    const data = { action: 'retry-rejected-receipt', confirmed: true, note: '核查恢复采购编号', orderVersion: (await s.view()).version, updatedAt: purchase.updatedAt }
    return s.request(endpoint, data, s.admin)
  }
  assert.equal((await recover()).status, 409)
  s.state.losePost = false; s.state.refund = true; s.ready(); await fulfillDeliverySupplier(s.env, s.order.id)
  assert.equal((await recover()).status, 409)
  assert.equal(s.db.prepare('SELECT request_key FROM delivery_supplier_orders WHERE order_id=?').get(s.order.id)!.request_key, 'ymx_' + s.order.id)
})

test('only explicit invalid receipt without an order identity is a definite rejection', async t => {
  const s = await setup(t)
  const response = (body: object, status: number) => t.mock.method(globalThis, 'fetch', async () => Response.json(body, { status }))
  response({ error_code: 'INVALID_IDEMPOTENCY_KEY', error: 'PRIVATE' }, 400)
  await assert.rejects(call(s.env, '/order', 'legacy/receipt', '{}'), e => e instanceof DeliverySupplierReceiptRejected && !e.message.includes('PRIVATE'))
  for (const status of [409, 503]) {
    response({ error_code: 'INVALID_IDEMPOTENCY_KEY', error: 'PRIVATE' }, status)
    await assert.rejects(call(s.env, '/order', 'legacy/receipt', '{}'), e => !(e instanceof DeliverySupplierReceiptRejected))
  }
  response({ error_code: 'INVALID_IDEMPOTENCY_KEY', order_id: 'API_PAID', order_status: 'pending' }, 400)
  assert.equal((await call<{ order_id: string }>(s.env, '/order', 'legacy/receipt', '{}')).order_id, 'API_PAID')
})
