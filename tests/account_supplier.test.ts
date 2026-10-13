import test from 'node:test'
import assert from 'node:assert/strict'
import { createHash, generateKeyPairSync } from 'node:crypto'
import { customerFixture } from './customer-fixture.ts'
import { call, signature } from '../worker/account_supplier/client.ts'
import { fulfillGmail, reconcileGmail } from '../worker/account_supplier/service.ts'
import { applyPaymentResult, findPayment } from '../worker/payments/service.ts'

type Context = Parameters<Parameters<typeof test>[1]>[0]
const keys = generateKeyPairSync('rsa', { modulusLength: 2048 })
const privateValue = 'member@gmail.test----PRIVATE-PASSWORD----PRIVATE-2FA----https://verify.example.test/private'
async function setup(t: Context) {
  const s = await customerFixture(); t.after(() => s.db.close())
  Object.assign(s.env, { ORDER_TESTING_ENABLED: 'false', EMAIL_PRODUCTS_ENABLED: 'true', RESEND_API_KEY: 'mock-mail-key', RESEND_FROM: 'GPTibo <delivery@example.test>', PAYMENT_NOTIFY_ORIGIN: 'https://shop.example.invalid', ALIPAY_APP_ID: '2026000000000000', ALIPAY_SELLER_ID: '2088000000000000', ALIPAY_APP_PRIVATE_KEY: keys.privateKey.export({ type: 'pkcs8', format: 'pem' }).toString(), ALIPAY_PUBLIC_KEY: keys.publicKey.export({ type: 'spki', format: 'pem' }).toString(), ACCOUNT_SUPPLIER_API_BASE: 'https://supplier.example.test', ACCOUNT_SUPPLIER_APP_ID: '12345', ACCOUNT_SUPPLIER_APP_SECRET: 'mock-supplier-secret', ACCOUNT_SUPPLIER_GOODS_ID: '1001', ACCOUNT_SUPPLIER_MAX_COST_CENTS: '600', ACCOUNT_SUPPLIER_CHECKOUT_ENABLED: 'true' })
  const state = { price: 6, stock: 5, creates: 0, queries: 0, mails: [] as { body: string; key: string | null }[], losePurchase: false, rejectPurchase: false, loseMail: false, pending: false, mismatch: false, actualCost: 6, refunded: false, duringCreate: undefined as (() => Promise<void>) | undefined }
  t.mock.method(globalThis, 'fetch', async (url: string | URL | Request, options: RequestInit) => {
    const headers = new Headers(options.headers), raw = String(options.body), data = JSON.parse(raw)
    if (String(url) === 'https://api.resend.com/emails') {
      state.mails.push({ body: raw, key: headers.get('Idempotency-Key') })
      assert.equal(headers.get('Authorization'), 'Bearer mock-mail-key')
      if (state.loseMail) { state.loseMail = false; throw new Error('lost mail') }
      return Response.json({ id: 'mail_gmail' })
    }
    assert.match(String(url), /^https:\/\/supplier\.example\.test\/api\/v3\//)
    assert.equal(headers.get('X-App-Id'), s.env.ACCOUNT_SUPPLIER_APP_ID)
    assert.equal(headers.get('X-Version'), '3.0')
    assert.match(headers.get('X-Timestamp')!, /^\d{10}$/)
    assert.equal(headers.get('X-Signature'), createHash('md5').update(s.env.ACCOUNT_SUPPLIER_APP_ID! + s.env.ACCOUNT_SUPPLIER_APP_SECRET! + '3.0' + headers.get('X-Timestamp') + raw).digest('hex'))
    assert.equal(options.redirect, 'manual'); assert.doesNotMatch(raw, /customer@example.test/)
    if (String(url).endsWith('/goods/getDetail')) return Response.json({ code: 1000, data: { goodsId: data.goodsId, name: '美国 Gmail 白号 · 带 2FA', salesPrice: state.price, stockCount: state.stock, goodsType: 1, status: 1, skuType: 0, minQuantity: 1, maxQuantity: 100, multiple: 1, rechargeTemplates: [] } })
    if (String(url).endsWith('/order/create')) {
      state.creates++; assert.equal(data.goodsId, 1001); assert.equal(data.count, 1); assert.equal(data.safePrice, 6); assert.deepEqual(data.attach, [])
      await state.duringCreate?.()
      if (state.losePurchase) throw new Error('lost purchase')
      if (state.rejectPurchase) return Response.json({ code: 999, msg: 'PRIVATE-PASSWORD upstream error' })
      return Response.json({ code: 1000, data: { orderNumber: 'SUPPLIER-1', cards: [] } })
    }
    assert.ok(String(url).endsWith('/order/getDetail')); state.queries++
    return Response.json({ code: 1000, data: { orderNumber: 'SUPPLIER-1', outerNumber: state.mismatch ? 'OTHER-ORDER' : data.outerNumber, status: state.refunded ? 4 : state.pending ? 2 : 3, count: 1, money: state.actualCost, retMoney: state.refunded ? state.actualCost : 0, cards: [{ cardId: 'card-1', cardNo: privateValue, cardPwd: '', status: 0 }] } })
  })
  const buyer = (await s.request('/api/orders/client', {})).headers.get('set-cookie')!.split(';')[0]
  async function create(extra = {}) {
    const response = await s.request('/api/orders', { key: crypto.randomUUID(), plan: 'gmail-account', expectedPrice: 1880, email: 'customer@example.test', ...extra }, buyer)
    assert.equal(response.status, 201, await response.clone().text())
    return (await response.json()).order
  }
  async function pay(id: string) {
    s.db.prepare("INSERT INTO payments(order_id,provider,account,amount_cents,expires_at,created_at,updated_at) SELECT id,'alipay',?,price_cents,?,?,? FROM orders WHERE id=?").run(s.env.ALIPAY_APP_ID!, Date.now() + 60000, Date.now(), Date.now(), id)
    await applyPaymentResult(s.env, (await findPayment(s.env, id))!, { orderId: id, amountCents: 1880, status: 'paid', tradeNo: 'trade_gmail' })
  }
  return { ...s, buyer, state, create, pay }
}

test('supplier signs exact UTF-8 bytes and rejects all non-1000 responses without leaking upstream messages', async t => {
  const s = await setup(t)
  const text = JSON.stringify({ name: '谷歌邮箱', amount: 1 })
  assert.equal(signature('10000', 'example-secret', '1760767397', text), createHash('md5').update('10000example-secret3.01760767397' + text).digest('hex'))
  t.mock.method(globalThis, 'fetch', async () => Response.json({ code: 0, msg: privateValue }))
  await assert.rejects(call(s.env, '/goods/getDetail', { goodsId: 1001 }), error => error instanceof Error && !error.message.includes(privateValue))
})

test('supplier redirects never forward signed credentials or accept an HTML challenge as goods', async t => {
  const s = await setup(t)
  let requests = 0
  t.mock.method(globalThis, 'fetch', async (_url: unknown, options: RequestInit) => {
    requests++; assert.equal(options.redirect, 'manual')
    return new Response(null, { status: 302, headers: { Location: 'https://other.example.test/' } })
  })
  await assert.rejects(call(s.env, '/goods/getDetail', { goodsId: 1001 }))
  assert.equal(requests, 1)
  t.mock.method(globalThis, 'fetch', async () => new Response('<html>challenge</html>'))
  await assert.rejects(call(s.env, '/goods/getDetail', { goodsId: 1001 }))
})

test('only verified live payment buys once and delivers private account by immutable email; defaults use supply 10 and retail 18.8', async t => {
  const s = await setup(t), order = await s.create()
  assert.equal(order.mode, 'live'); assert.equal(order.price, 18.8); assert.equal(order.delivery.status, 'waiting')
  assert.equal(s.db.prepare("SELECT cost_cents FROM partner_default_prices WHERE plan='gmail-account'").get()!.cost_cents, 1000)
  await fulfillGmail(s.env, order.id); assert.equal(s.state.creates, 0)
  await s.pay(order.id)
  assert.equal(s.state.creates, 1); assert.equal(s.state.mails.length, 1)
  const mail = JSON.parse(s.state.mails[0].body)
  assert.deepEqual(mail.to, ['customer@example.test']); assert.match(mail.subject, /Gmail/); assert.match(mail.text, /PRIVATE-PASSWORD/); assert.doesNotMatch(mail.html, /多邻国/)
  assert.equal(s.db.prepare('SELECT status FROM orders WHERE id=?').get(order.id)!.status, 'success')
  await fulfillGmail(s.env, order.id); await reconcileGmail(s.env)
  assert.equal(s.state.creates, 1); assert.equal(s.state.mails.length, 1)
  const publicData = await (await s.request('/api/orders/' + order.id, undefined, s.buyer)).json()
  assert.doesNotMatch(JSON.stringify(publicData), /PRIVATE-PASSWORD|PRIVATE-2FA|cardNo|cards|api_base/)
  assert.doesNotMatch(JSON.stringify(s.db.prepare('SELECT * FROM order_events').all()), /PRIVATE-PASSWORD|PRIVATE-2FA/)
  assert.equal((await s.request(`/api/admin/orders/${order.id}/account_supplier`, undefined, s.buyer)).status, 401)
  assert.equal((await s.request(`/api/admin/orders/${order.id}/account_supplier`, {}, s.admin, 'https://evil.test')).status, 403)
  assert.equal((await s.request(`/api/admin/orders/${order.id}/delivery`, { action: 'save', version: 0, orderVersion: 0, value: 'replacement', instructions: 'replacement' }, s.admin)).status, 409)
  const candidate = s.db.prepare('SELECT cost_cents FROM accounting_candidates WHERE order_id=?').get(order.id)!
  assert.equal(candidate.cost_cents, 600)
  const report = await (await s.request('/api/admin/accounting/report')).json(); assert.equal(report.error !== undefined, true)
  const adminReport = await (await s.request('/api/admin/accounting/report', undefined, s.admin)).json()
  assert.equal(adminReport.orders.find((o: { id: string }) => o.id === order.id).cost_cents, 600)
  assert.equal(adminReport.orders.find((o: { id: string }) => o.id === order.id).profit_cents, 1280)
})

test('lost create response and concurrent payment jobs query the same original order without a second purchase', async t => {
  const s = await setup(t), order = await s.create(); s.state.losePurchase = true
  s.state.duringCreate = async () => {
    await fulfillGmail(s.env, order.id)
    const version = s.db.prepare('SELECT version FROM orders WHERE id=?').get(order.id)!.version
    assert.equal((await s.request(`/api/orders/${order.id}/refunds`, { key: crypto.randomUUID(), version, reason: '申请退款' }, s.buyer)).status, 409)
    assert.equal((await s.request(`/api/admin/orders/${order.id}/actions`, { action: 'redemption-control', blocked: true, version, note: '暂停采购' }, s.admin)).status, 409)
  }
  await s.pay(order.id)
  assert.equal(s.db.prepare('SELECT state FROM account_supplier_orders WHERE order_id=?').get(order.id)!.state, 'unknown')
  assert.equal(s.state.creates, 1); assert.equal(s.state.mails.length, 0)
  const version = s.db.prepare('SELECT version FROM orders WHERE id=?').get(order.id)!.version
  assert.equal((await s.request(`/api/orders/${order.id}/refunds`, { key: crypto.randomUUID(), version, reason: '超时退款' }, s.buyer)).status, 409)
  await fulfillGmail(s.env, order.id)
  assert.equal(s.state.creates, 1); assert.equal(s.state.queries, 1); assert.equal(s.state.mails.length, 1)
})

test('uncertain mail retries exact body and key, preserves procured account and refuses retries after 23h', async t => {
  const s = await setup(t), order = await s.create(); s.state.loseMail = true
  await s.pay(order.id)
  assert.equal(s.db.prepare('SELECT state FROM account_supplier_orders WHERE order_id=?').get(order.id)!.state, 'purchased')
  assert.equal(s.db.prepare('SELECT status FROM email_deliveries WHERE order_id=?').get(order.id)!.status, 'unknown')
  s.env.RESEND_FROM = 'changed@example.test'
  await fulfillGmail(s.env, order.id)
  assert.equal(s.state.creates, 1); assert.equal(s.state.mails.length, 2); assert.deepEqual(s.state.mails[0], s.state.mails[1])
  s.db.prepare("UPDATE orders SET status='processing' WHERE id=?").run(order.id)
  s.db.prepare("UPDATE email_deliveries SET status='unknown',first_attempt_at=? WHERE order_id=?").run(Date.now() - 24 * 3600000, order.id)
  await assert.rejects(fulfillGmail(s.env, order.id), /安全重试/)
  assert.equal(s.state.mails.length, 2); assert.equal(s.state.creates, 1)
})

test('stock, purchase price ceiling, test mode and frozen/refunded orders prevent procurement', async t => {
  const s = await setup(t)
  for (const [key, value] of [['stock', 0], ['price', 6.01]] as const) {
    s.state[key] = value
    const catalog = await (await s.request('/api/catalog')).json()
    assert.equal(catalog.plans.find((p: { id: string }) => p.id === 'gmail-account').enabled, 1)
    assert.equal((await s.request('/api/orders', { key: crypto.randomUUID(), plan: 'gmail-account', expectedPrice: 1880, email: 'customer@example.test' }, s.buyer)).status, 409)
    s.state.stock = 5; s.state.price = 6
  }
  const order = await s.create()
  for (const patch of ["mode='demo',payment_status='simulated'", 'is_test=1', 'refund_hold=1', 'redemption_blocked=1', 'refunded_cents=1']) {
    s.db.prepare("UPDATE orders SET payment_status='paid',status='session'," + patch + ' WHERE id=?').run(order.id)
    await fulfillGmail(s.env, order.id); assert.equal(s.state.creates, 0)
    s.db.prepare("UPDATE orders SET mode='live',payment_status='paid',is_test=0,refund_hold=0,redemption_blocked=0,refunded_cents=0 WHERE id=?").run(order.id)
  }
  s.state.price = 6.01
  await assert.rejects(fulfillGmail(s.env, order.id), /进价/); assert.equal(s.state.creates, 0)
  assert.equal(s.db.prepare('SELECT attempted_at FROM account_supplier_orders WHERE order_id=?').get(order.id)!.attempted_at, null)
})

test('pending, mismatched and over-budget supplier results never send credentials; terminal rejection never repeats create', async t => {
  const s = await setup(t), order = await s.create(); s.state.pending = true
  await s.pay(order.id); assert.equal(s.state.mails.length, 0)
  s.state.pending = false; s.state.mismatch = true
  await assert.rejects(fulfillGmail(s.env, order.id)); assert.equal(s.state.mails.length, 0)
  s.state.mismatch = false; s.state.actualCost = 6.01
  await assert.rejects(fulfillGmail(s.env, order.id)); assert.equal(s.state.mails.length, 0)
  s.state.actualCost = 6; s.state.refunded = true
  await fulfillGmail(s.env, order.id)
  assert.equal(s.db.prepare('SELECT state FROM account_supplier_orders WHERE order_id=?').get(order.id)!.state, 'rejected')
  await fulfillGmail(s.env, order.id); assert.equal(s.state.creates, 1)
})

test('changed supplier account cannot redirect recovery to another account; checkout flag stops new buys', async t => {
  const s = await setup(t), order = await s.create()
  s.db.prepare("UPDATE orders SET payment_status='paid',status='session' WHERE id=?").run(order.id)
  s.env.ACCOUNT_SUPPLIER_CHECKOUT_ENABLED = 'false'; await fulfillGmail(s.env, order.id); assert.equal(s.state.creates, 0)
  s.env.ACCOUNT_SUPPLIER_CHECKOUT_ENABLED = 'true'; s.env.ACCOUNT_SUPPLIER_APP_ID = '98765'
  await assert.rejects(fulfillGmail(s.env, order.id), /账户已变化/); assert.equal(s.state.creates, 0)
})

test('agent order snapshots supply 10, earns 8.8 and accounts for confirmed procurement 6 separately', async t => {
  const s = await setup(t), agent = await s.agent('Gmail 代理')
  s.db.prepare("INSERT INTO partner_prices(partner_id,plan,cost_cents,price_cents) VALUES (?,'gmail-account',1000,1880)").run(agent.id)
  const slug = s.db.prepare('SELECT slug FROM partners WHERE id=?').get(agent.id)!.slug
  const order = await s.create({ partner: slug })
  s.db.prepare("UPDATE partner_prices SET cost_cents=1100,price_cents=1990 WHERE partner_id=? AND plan='gmail-account'").run(agent.id)
  await s.pay(order.id)
  assert.equal(s.db.prepare('SELECT cost_cents FROM orders WHERE id=?').get(order.id)!.cost_cents, 1000)
  const report = await (await s.request('/api/admin/accounting/report', undefined, s.admin)).json()
  assert.equal(report.orders[0].commission_cents, 880); assert.equal(report.orders[0].cost_cents, 600); assert.equal(report.orders[0].profit_cents, 400)
  s.db.prepare("UPDATE partner_prices SET cost_cents=500,price_cents=1880 WHERE partner_id=? AND plan='gmail-account'").run(agent.id)
  const catalog = await (await s.request('/api/catalog?partner=' + slug)).json()
  assert.equal(catalog.plans.find((p: { id: string }) => p.id === 'gmail-account').enabled, 1)
  assert.equal((await s.request('/api/orders', { key: crypto.randomUUID(), plan: 'gmail-account', expectedPrice: 1880, email: 'customer@example.test', partner: slug }, s.buyer)).status, 409)
  assert.doesNotMatch(JSON.stringify(catalog), /cost_cents|ACCOUNT_SUPPLIER|mock-supplier-secret/)
})

test('purchase audit failure rolls back the attempt before any spend, while stale crash attempts only query', async t => {
  const s = await setup(t), order = await s.create()
  s.db.prepare("UPDATE orders SET payment_status='paid',status='session' WHERE id=?").run(order.id)
  s.db.exec("CREATE TRIGGER fail_gmail BEFORE INSERT ON order_events WHEN NEW.action='gmail-purchase-start' BEGIN SELECT RAISE(ABORT,'failed'); END")
  await assert.rejects(fulfillGmail(s.env, order.id)); assert.equal(s.state.creates, 0)
  assert.equal(s.db.prepare('SELECT attempted_at FROM account_supplier_orders WHERE order_id=?').get(order.id)!.attempted_at, null)
  s.db.exec('DROP TRIGGER fail_gmail')
  s.db.prepare("UPDATE account_supplier_orders SET state='creating',attempted_at=?,lease_until=0 WHERE order_id=?").run(Date.now() - 120000, order.id)
  await fulfillGmail(s.env, order.id)
  assert.equal(s.state.creates, 0); assert.equal(s.state.queries, 1); assert.equal(s.state.mails.length, 1)
})

test('uncertain procurement needs explicit admin evidence before releasing refund; resolution and audit are atomic', async t => {
  const s = await setup(t), order = await s.create(); s.state.losePurchase = true
  await s.pay(order.id)
  const endpoint = `/api/admin/orders/${order.id}/account_supplier`
  const purchase = (await (await s.request(endpoint, undefined, s.admin)).json()).purchase
  const version = s.db.prepare('SELECT version FROM orders WHERE id=?').get(order.id)!.version
  const data = { action: 'confirm-not-purchased', confirmed: true, note: '供应商核查原单不存在且无扣款', orderVersion: version, updatedAt: purchase.updatedAt }
  assert.equal((await s.request(endpoint, { ...data, confirmed: false }, s.admin)).status, 409)
  assert.equal((await s.request(endpoint, { ...data, updatedAt: -1 }, s.admin)).status, 409)
  s.db.exec("CREATE TRIGGER fail_gmail_resolution BEFORE INSERT ON order_events WHEN NEW.action='gmail-purchase-reconciled' BEGIN SELECT RAISE(ABORT,'failed'); END")
  assert.equal((await s.request(endpoint, data, s.admin)).status, 503)
  assert.equal(s.db.prepare('SELECT state FROM account_supplier_orders WHERE order_id=?').get(order.id)!.state, 'unknown')
  s.db.exec('DROP TRIGGER fail_gmail_resolution')
  assert.equal((await s.request(endpoint, data, s.admin)).status, 200)
  assert.equal(s.db.prepare('SELECT status FROM orders WHERE id=?').get(order.id)!.status, 'failed')
  await fulfillGmail(s.env, order.id); assert.equal(s.state.creates, 1); assert.equal(s.state.mails.length, 0)
  const current = s.db.prepare('SELECT version FROM orders WHERE id=?').get(order.id)!.version
  assert.equal((await s.request(`/api/orders/${order.id}/refunds`, { key: crypto.randomUUID(), version: current, reason: '采购未发生申请退款' }, s.buyer)).status, 201)
})
