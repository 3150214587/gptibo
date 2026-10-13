import test from 'node:test'
import assert from 'node:assert/strict'
import { generateKeyPairSync, sign } from 'node:crypto'
import { customerFixture } from './customer-fixture.ts'
import { membershipDeliveryMessage } from '../worker/email.ts'
import { membershipClaimUrl } from '../shared/membership-claim.ts'

type Context = Parameters<Parameters<typeof test>[1]>[0]
const keys = generateKeyPairSync('rsa', { modulusLength: 2048 })
async function setup(t: Context, plan = 'gemini-pro-18m') {
  const s = await customerFixture(); t.after(() => s.db.close())
  Object.assign(s.env, { ORDER_TESTING_ENABLED: 'false', EMAIL_PRODUCTS_ENABLED: 'true', RESEND_API_KEY: 'test-mail-key', RESEND_FROM: 'GPTibo <delivery@example.test>', PAYMENT_NOTIFY_ORIGIN: 'https://shop.example.invalid', ALIPAY_APP_ID: '2026000000000000', ALIPAY_SELLER_ID: '2088000000000000', ALIPAY_APP_PRIVATE_KEY: keys.privateKey.export({ type: 'pkcs8', format: 'pem' }).toString(), ALIPAY_PUBLIC_KEY: keys.publicKey.export({ type: 'spki', format: 'pem' }).toString() })
  Object.assign(s.env, { DELIVERY_SUPPLIER_API_KEY: 'test-supplier-key', DELIVERY_SUPPLIER_CNY_PER_USD: '6.7', DELIVERY_SUPPLIER_CHECKOUT_ENABLED: 'true' })
  t.mock.method(globalThis, 'fetch', async () => Response.json({ services: [{ service_id: 'DEMO_PRODUCT_A', name: 'Gemini 18M', price: 0.6, stock: 10 }, { service_id: 'DEMO_PRODUCT_B', name: 'Duolingo 1Y', price: 0.38, stock: 10 }] }))
  const buyer = (await s.request('/api/orders/client', {})).headers.get('set-cookie')!.split(';')[0]
  const created = await s.request('/api/orders', { key: crypto.randomUUID(), plan, expectedPrice: 1990, email: 'member@example.test' }, buyer)
  assert.equal(created.status, 201, await created.clone().text())
  const order = (await created.json()).order
  // These cases cover pre-integration manual orders, which have no supplier snapshot.
  s.db.prepare('DELETE FROM delivery_supplier_orders WHERE order_id=?').run(order.id)
  const endpoint = `/api/admin/orders/${order.id}/delivery`
  const view = async () => (await (await s.request(endpoint, undefined, s.admin)).json())
  const act = async (action: string, extra = {}) => { const data = await view(); return s.request(endpoint, { action, version: data.delivery.version, orderVersion: data.orderVersion, ...extra }, s.admin) }
  const paid = () => s.db.prepare("UPDATE orders SET status='session',payment_status='paid' WHERE id=?").run(order.id)
  const save = async (value = plan === 'gemini-pro-18m' ? 'https://example.test/claim/private-link' : 'PRIVATE-CDK-123') => { const response = await act('save', { value, instructions: '请先核对账号。\n兑换后查看会员状态。' }); assert.equal(response.status, 200, await response.clone().text()); return response.json() }
  return { ...s, order, buyer, view, act, paid, save, endpoint }
}

test('legacy email orders retain exact prices, immutable purchase notes and manual fulfillment', async t => {
  const s = await setup(t)
  assert.equal(s.order.mode, 'live'); assert.equal(s.order.price, 19.9); assert.equal(s.order.manualFulfillment, true)
  assert.equal(s.order.delivery.kind, 'link'); assert.ok(s.order.purchaseNote)
  assert.equal(s.db.prepare('SELECT COUNT(*) n FROM recharge_orders').get()!.n, 0)
  const catalog = await (await s.request('/api/catalog')).json()
  assert.equal(catalog.checkoutEnabled, true)
  assert.deepEqual(catalog.plans.filter((p: { enabled: number }) => p.enabled).map((p: { id: string }) => p.id), ['gemini-pro-18m', 'duolingo-12m'])
  const plan = s.db.prepare("SELECT * FROM catalog_plans WHERE id='gemini-pro-18m'").get()!
  const edited = await s.request('/api/admin/plans/gemini-pro-18m', { priceCents: 1990, enabled: true, version: plan.version, note: '更新领取要求', purchaseNote: '新增购买说明', deliveryInstructions: '新的默认步骤' }, s.admin)
  assert.equal(edited.status, 200)
  assert.equal((await (await s.request('/api/orders/' + s.order.id, undefined, s.buyer)).json()).order.purchaseNote, s.order.purchaseNote)
  assert.equal((await s.view()).delivery.instructions, '新的默认步骤')
  s.env.EMAIL_PRODUCTS_ENABLED = 'false'
  const closed = await s.request('/api/orders', { key: crypto.randomUUID(), plan: 'duolingo-12m', expectedPrice: 1990, email: 'member@example.test' }, s.buyer)
  assert.equal(closed.status, 409)
})

test('membership payment uses its own signed Alipay amount and subject; confirmed payment waits for manual email', async t => {
  const s = await setup(t, 'duolingo-12m')
  const methods: string[] = []
  t.mock.method(globalThis, 'fetch', async (url: string, options: RequestInit) => {
    assert.equal(url, 'https://openapi.alipay.com/gateway.do')
    const params = Object.fromEntries(new URLSearchParams(String(options.body))), biz = JSON.parse(params.biz_content)
    methods.push(params.method)
    let result: Record<string, unknown>
    if (params.method === 'alipay.trade.precreate') { assert.equal(biz.total_amount, '19.90'); assert.match(biz.subject, /多邻国/); assert.doesNotMatch(biz.subject, /ChatGPT/); result = { code: '10000', out_trade_no: s.order.id, qr_code: 'https://qr.alipay.com/test-membership' } }
    else { assert.equal(params.method, 'alipay.trade.query'); result = { code: '10000', out_trade_no: s.order.id, total_amount: '19.90', seller_id: s.env.ALIPAY_SELLER_ID, trade_status: 'TRADE_SUCCESS', trade_no: 'trade_membership' } }
    const raw = JSON.stringify(result)
    return new Response(`{"${params.method.replaceAll('.', '_')}_response":${raw},"sign":"${sign('RSA-SHA256', Buffer.from(raw), keys.privateKey).toString('base64')}"}`)
  })
  assert.equal((await s.request(`/api/orders/${s.order.id}/payment`, { action: 'create' }, s.buyer)).status, 200)
  assert.equal((await s.request(`/api/orders/${s.order.id}/payment`, { action: 'query' }, s.buyer)).status, 200)
  const current = (await (await s.request('/api/orders/' + s.order.id, undefined, s.buyer)).json()).order
  assert.equal(current.paymentStatus, 'paid'); assert.equal(current.status, 'session'); assert.equal(current.delivery.status, 'waiting')
  assert.deepEqual(methods, ['alipay.trade.precreate', 'alipay.trade.query'])
})

test('administrator-only drafts and email search preserve customer isolation and never expose the CDK in public data or audits', async t => {
  const s = await setup(t, 'duolingo-12m'); s.paid()
  assert.equal((await s.request(s.endpoint, undefined, s.buyer)).status, 401)
  assert.equal((await s.request(s.endpoint, { action: 'save' }, s.admin, 'https://evil.test')).status, 403)
  const saved = await s.save()
  assert.match(saved.delivery.preview.html, /PRIVATE-CDK-123/)
  assert.match(saved.delivery.preview.text, /PRIVATE-CDK-123/)
  const searched = await (await s.request('/api/admin/orders?q=MEMBER%40EXAMPLE.TEST', undefined, s.admin)).json()
  assert.equal(searched.total, 1); assert.equal(searched.orders[0].id, s.order.id)
  assert.doesNotMatch(JSON.stringify(searched), /PRIVATE-CDK|mail_body/)
  const customer = await (await s.request('/api/orders/' + s.order.id, undefined, s.buyer)).json()
  assert.doesNotMatch(JSON.stringify(customer), /PRIVATE-CDK|领取说明|mail_body/)
  assert.doesNotMatch(JSON.stringify(s.db.prepare('SELECT * FROM order_events').all()), /PRIVATE-CDK/)
  const other = (await s.request('/api/orders/client', {})).headers.get('set-cookie')!.split(';')[0]
  assert.equal((await s.request('/api/orders/' + s.order.id, undefined, other)).status, 404)
})

test('payment, refund and redemption guards reject drafts and sending; unsafe links and missing instructions are rejected', async t => {
  const s = await setup(t)
  assert.equal((await s.act('save', { value: 'https://example.test/claim', instructions: '说明' })).status, 409)
  s.paid()
  for (const value of ['javascript:alert(1)', 'http://example.test', 'https://user:pass@example.test', 'https://example.test/\nsecret']) assert.equal((await s.act('save', { value, instructions: '说明' })).status, 400)
  assert.equal((await s.act('save', { value: 'https://example.test', instructions: '' })).status, 400)
  await s.save()
  for (const field of ['refund_hold', 'refunded_cents', 'redemption_blocked']) {
    s.db.prepare(`UPDATE orders SET ${field}=1 WHERE id=?`).run(s.order.id)
    assert.equal((await s.act('send')).status, 409)
    s.db.prepare(`UPDATE orders SET ${field}=0 WHERE id=?`).run(s.order.id)
  }
})

test('sending freezes content, serializes concurrent sends and blocks a racing refund or stop marker', async t => {
  const s = await setup(t); s.paid(); await s.save()
  const before = await s.view(); let calls = 0
  t.mock.method(globalThis, 'fetch', async (url: string, options: RequestInit) => {
    calls++; assert.equal(url, 'https://api.resend.com/emails')
    const mail = JSON.parse(String(options.body)); assert.deepEqual(mail.to, ['member@example.test']); assert.match(mail.text, /private-link/)
    assert.equal((await s.act('send')).status, 409)
    assert.equal((await s.act('save', { value: 'https://example.test/new', instructions: '替换' })).status, 409)
    const version = s.db.prepare('SELECT version FROM orders WHERE id=?').get(s.order.id)!.version
    assert.equal((await s.request(`/api/orders/${s.order.id}/refunds`, { key: crypto.randomUUID(), version, reason: '申请退款' }, s.buyer)).status, 409)
    assert.equal((await s.request(`/api/admin/orders/${s.order.id}/actions`, { action: 'redemption-control', blocked: true, version, note: '停止发货' }, s.admin)).status, 409)
    return Response.json({ id: 'mail_accepted' })
  })
  assert.equal((await s.act('send')).status, 200); assert.equal(calls, 1)
  assert.equal(s.db.prepare('SELECT status FROM orders WHERE id=?').get(s.order.id)!.status, 'success')
  assert.equal((await s.request(s.endpoint, { action: 'send', version: before.delivery.version, orderVersion: before.orderVersion }, s.admin)).status, 200)
  assert.equal(calls, 1)
})

test('lost mail response retries the same frozen body and idempotency key then records one successful delivery', async t => {
  const s = await setup(t); s.paid(); await s.save()
  const sent: { key: string | null; body: string }[] = []
  t.mock.method(globalThis, 'fetch', async (_url: string, options: RequestInit) => {
    sent.push({ key: new Headers(options.headers).get('Idempotency-Key'), body: String(options.body) })
    if (sent.length === 1) throw new Error('lost response')
    return Response.json({ id: 'mail_recovered' })
  })
  assert.equal((await s.act('send')).status, 503)
  assert.equal((await s.view()).delivery.status, 'unknown')
  assert.equal(s.db.prepare('SELECT status FROM orders WHERE id=?').get(s.order.id)!.status, 'processing')
  s.env.RESEND_FROM = 'changed@example.test'
  assert.equal((await s.act('send')).status, 200)
  assert.deepEqual(sent[0], sent[1])
  assert.equal(s.db.prepare("SELECT COUNT(*) n FROM order_events WHERE action='email-delivery-sent'").get()!.n, 1)
})

test('expired uncertain sends cannot replay outside provider deduplication and require explicit manual verification', async t => {
  const s = await setup(t); s.paid(); await s.save()
  let calls = 0
  t.mock.method(globalThis, 'fetch', async () => { calls++; return new Response('upstream private error', { status: 503 }) })
  assert.equal((await s.act('send')).status, 503)
  s.db.prepare('UPDATE email_deliveries SET first_attempt_at=? WHERE order_id=?').run(Date.now() - 24 * 3600000, s.order.id)
  assert.equal((await s.act('send')).status, 409); assert.equal(calls, 1)
  assert.equal((await s.act('confirm-sent', { note: '' })).status, 400)
  assert.equal((await s.act('confirm-sent', { note: '已在邮件服务后台核实原邮件发送成功' })).status, 200)
  assert.equal(calls, 1); assert.equal((await s.view()).delivery.status, 'sent')
})

test('draft and success audit failures roll back consistently and preserve the retryable original mail', async t => {
  const s = await setup(t); s.paid()
  s.db.exec("CREATE TRIGGER fail_draft BEFORE INSERT ON order_events WHEN NEW.action='email-delivery-draft' BEGIN SELECT RAISE(ABORT,'secret'); END")
  assert.equal((await s.act('save', { value: 'https://example.test/claim', instructions: '说明' })).status, 503)
  assert.equal(s.db.prepare('SELECT COUNT(*) n FROM email_deliveries').get()!.n, 0)
  s.db.exec('DROP TRIGGER fail_draft'); await s.save()
  s.db.exec("CREATE TRIGGER fail_sent BEFORE INSERT ON order_events WHEN NEW.action='email-delivery-sent' BEGIN SELECT RAISE(ABORT,'secret'); END")
  t.mock.method(globalThis, 'fetch', async () => Response.json({ id: 'already_accepted' }))
  assert.equal((await s.act('send')).status, 503); assert.equal((await s.view()).delivery.status, 'unknown')
  assert.equal(s.db.prepare('SELECT status FROM orders WHERE id=?').get(s.order.id)!.status, 'processing')
  s.db.exec('DROP TRIGGER fail_sent')
  assert.equal((await s.act('send')).status, 200)
})

test('delivery templates escape values once, offer plain text and do not imply automatic membership activation', () => {
  const mail = membershipDeliveryMessage('cdk', 'YMX-EXAMPLE', '<secret>&__RECHARGE_DELIVERY_ORDER__', '<img src=x onerror=evil>\n领取步骤')
  assert.match(mail.html, /&lt;secret&gt;&amp;__RECHARGE_DELIVERY_ORDER__/)
  assert.doesNotMatch(mail.html, /<img src=x|<script|<iframe/)
  assert.match(mail.text, /<secret>&__RECHARGE_DELIVERY_ORDER__/)
  assert.match(mail.text, /实际激活结果/)
  const link = membershipDeliveryMessage('link', 'YMX-EXAMPLE', 'https://example.test/claim?a=1&b=2', '请确认账号')
  assert.match(link.html, /href="https:\/\/example.test\/claim\?a=1&amp;b=2"/)
  assert.match(link.text, /18 个月/)
})

test('Duolingo HTTPS deliveries stored as CDKs offer a claim button and the complete copyable link', () => {
  const value = 'https://www.duolingo.com/claim/super?code=DEMO&source=mail'
  const mail = membershipDeliveryMessage('cdk', 'YMX-EXAMPLE', value, '请核对领取账号', 'duolingo-12m')
  assert.match(mail.subject, /多邻国.*领取链接/)
  assert.match(mail.html, /立即领取/)
  assert.match(mail.html, /复制下方链接到浏览器打开/)
  assert.equal((mail.html.match(/href="https:\/\/www.duolingo.com\/claim\/super\?code=DEMO&amp;source=mail"/g) ?? []).length, 2)
  assert.match(mail.html, />https:\/\/www.duolingo.com\/claim\/super\?code=DEMO&amp;source=mail<\/a>/)
  assert.ok(mail.text.includes(value)); assert.match(mail.text, /立即领取/)
  assert.doesNotMatch(mail.html + mail.text, /Gemini|兑换 CDK|__RECHARGE_DELIVERY_/)
  const gemini = membershipDeliveryMessage('link', 'YMX-EXAMPLE', value, '请核对领取账号', 'gemini-pro-18m')
  assert.match(gemini.html, /立即领取/); assert.match(gemini.subject, /Gemini/)
})

test('claim actions reject unsafe or mixed content and preserve code and Gmail delivery templates', () => {
  for (const value of ['PLAIN-CDK', 'javascript:alert(1)', 'http://example.test/claim', 'https://user:password@example.test/claim', 'https://example.test/claim\n<script>', 'https://example.test/claim" onmouseover=evil', 'code: https://example.test/claim', 'https://example.test/claim extra', 'https://', 'https://example.test/' + 'x'.repeat(2048)]) {
    assert.equal(membershipClaimUrl(value), null)
    const mail = membershipDeliveryMessage('cdk', 'YMX-EXAMPLE', value, '领取说明', 'duolingo-12m')
    assert.doesNotMatch(mail.html, /立即领取|__RECHARGE_DELIVERY_URL__/)
    assert.match(mail.subject, /兑换 CDK/)
  }
  assert.equal(membershipClaimUrl(' https://example.test/claim?a=1&b=2 '), 'https://example.test/claim?a=1&b=2')
  assert.throws(() => membershipDeliveryMessage('link', 'YMX-EXAMPLE', 'javascript:alert(1)', '说明'))
  const gmail = membershipDeliveryMessage('cdk', 'YMX-EXAMPLE', 'https://example.test/claim', '账号使用说明', 'gmail-account')
  assert.match(gmail.subject, /Gmail/); assert.doesNotMatch(gmail.html, /立即领取/)
})

test('membership previews use the normal link template, fixed review mailbox and do not create or fulfill orders', async t => {
  const s = await setup(t)
  const path = '/api/admin/membership-delivery/preview'
  const data = { plan: 'duolingo-12m', key: crypto.randomUUID(), to: 'attacker@example.test' }
  const mails: { body: string; key: string | null }[] = []
  t.mock.method(globalThis, 'fetch', async (url: string | URL | Request, init: RequestInit) => {
    assert.equal(String(url), 'https://api.resend.com/emails')
    mails.push({ body: String(init.body), key: new Headers(init.headers).get('Idempotency-Key') })
    return Response.json({ id: 'preview_accepted' })
  })
  assert.equal((await s.request(path, data)).status, 401)
  assert.equal((await s.request(path, data, s.admin, 'https://evil.test')).status, 403)
  assert.equal((await s.request(path, undefined, s.admin)).status, 405)
  assert.equal((await s.request(path, { ...data, plan: 'gmail-account' }, s.admin)).status, 400)
  assert.equal((await s.request(path, { ...data, key: 'invalid' }, s.admin)).status, 400)
  assert.equal(mails.length, 0)
  const orders = s.db.prepare('SELECT * FROM orders').all()
  const deliveries = s.db.prepare('SELECT * FROM email_deliveries').all()
  const response = await s.request(path, data, s.admin)
  assert.equal(response.status, 200); assert.equal((await response.json()).providerId, 'preview_accepted')
  const mail = JSON.parse(mails[0].body)
  assert.deepEqual(mail.to, ['admin@example.invalid'])
  assert.match(mail.subject, /多邻国.*领取链接/); assert.match(mail.html, /立即领取/)
  assert.match(mail.text, /PREVIEW-ONLY-NOT-A-REAL-CODE|YMX-PREVIEW/)
  assert.ok(mail.text.includes(s.db.prepare("SELECT delivery_instructions FROM catalog_plans WHERE id='duolingo-12m'").get()!.delivery_instructions as string))
  assert.deepEqual(s.db.prepare('SELECT * FROM orders').all(), orders)
  assert.deepEqual(s.db.prepare('SELECT * FROM email_deliveries').all(), deliveries)
  assert.equal((await s.request(path, data, s.admin)).status, 200)
  assert.deepEqual(mails[0], mails[1]); assert.equal(mails[0].key, 'membership-preview/' + data.key)
})


test('membership products remain platform-only until an administrator enables partner sales', async t => {
  const s = await setup(t)
  const agent = await s.agent('测试代理')
  s.db.prepare("INSERT INTO partner_prices(partner_id,plan,cost_cents,price_cents) VALUES (?,'gemini-pro-18m',1990,1990)").run(agent.id)
  const slug = s.db.prepare('SELECT slug FROM partners WHERE id=?').get(agent.id)!.slug
  const request = () => s.request('/api/orders', { key: crypto.randomUUID(), plan: 'gemini-pro-18m', expectedPrice: 1990, email: 'member@example.test', partner: slug }, s.buyer)
  assert.equal((await request()).status, 409)
  const plan = s.db.prepare("SELECT version FROM catalog_plans WHERE id='gemini-pro-18m'").get()!
  assert.equal((await s.request('/api/admin/plans/gemini-pro-18m', { priceCents: 1990, enabled: true, version: plan.version, note: '启用测试代理销售', partnerEnabled: true }, s.admin)).status, 200)
  assert.equal((await request()).status, 201)
})
