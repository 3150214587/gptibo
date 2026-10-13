import { seedAdminEmailCode } from './admin-email-fixture.ts'
import test from 'node:test'
import assert from 'node:assert/strict'
import { createHash, createHmac } from 'node:crypto'
import { DatabaseSync } from 'node:sqlite'
import { readFileSync, readdirSync } from 'node:fs'
import worker from '../worker/index.ts'
import { alipayQr, canonicalQuery, request as supplierRequest, signature, marginCents, moneyCents, RechargeError } from '../worker/recharge/client.ts'
import { reconcileRecharge } from '../worker/recharge/service.ts'
import { getOrder } from '../worker/order-state.ts'
import { orderLabel } from '../src/lib/demo-orders.ts'
import { confirmedRefundLabel } from '../shared/platform-order-status.ts'
import { reconcileRechargeRefunds } from '../worker/refunds.ts'
import { totpCode } from '../worker/totp.ts'
import type { Env, Statement, Value } from '../worker/platform.ts'

type Context = Parameters<Parameters<typeof test>[1]>[0]
const secret = 'private-test-api-secret'
const webhookSecret = 'private-test-webhook-secret'
const session = JSON.stringify({ user: { email: 'target@example.test' }, accessToken: 'PRIVATE-TOKEN-NOT-FOR-STORAGE' })
const digest = (s: string) => createHash('sha256').update(s).digest('hex')
const mac = (s: string, key = secret) => createHmac('sha256', key).update(s).digest('hex')
function setup(t: Context) {
  const db = new DatabaseSync(':memory:'); t.after(() => db.close())
  for (const file of readdirSync(new URL('../migrations/', import.meta.url)).filter(f => f.endsWith('.sql')).sort()) db.exec(readFileSync(new URL('../migrations/' + file, import.meta.url), 'utf8'))
  const bound = new WeakMap<Statement, { sql: string; values: Value[] }>()
  const env: Env = {
    ASSETS: { fetch: async () => new Response('asset') }, INTEREST_LIMITER: { limit: async () => ({ success: true }) }, API_LIMITER: { limit: async () => ({ success: true }) }, EXPENSIVE_LIMITER: { limit: async () => ({ success: true }) },
    ADMIN_EMAIL: 'admin@example.invalid', ADMIN_USERNAME: 'tester', ADMIN_TOTP_SECRET: 'GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ',
    RECHARGE_PARTNER_ID: 'pt_test', RECHARGE_KEY_ID: 'key_test', RECHARGE_CLIENT_SECRET: secret, RECHARGE_WEBHOOK_SECRET: webhookSecret, RECHARGE_NOTIFY_URL: 'https://shop.example.invalid/api/recharge/webhook', RECHARGE_PRODUCT_MAP: '{"plus":"demo_plus"}', RECHARGE_CHECKOUT_ENABLED: 'true',
    INTEREST_DB: {
      prepare(sql) {
        const state = { sql, values: [] as Value[] }
        const stmt: Statement = { bind(...values) { state.values = values; return this }, async first<T>() { return (db.prepare(sql).get(...state.values) ?? null) as T | null }, async all<T>() { return { results: db.prepare(sql).all(...state.values) as T[] } }, async run() { return db.prepare(sql).run(...state.values) } }
        bound.set(stmt, state); return stmt
      },
      async batch(statements) {
        db.exec('BEGIN')
        try { const results = statements.map(s => { const state = bound.get(s)!; return db.prepare(state.sql).run(...state.values) }); db.exec('COMMIT'); return results }
        catch (e) { db.exec('ROLLBACK'); throw e }
      },
    },
  }
  const call = (path: string, data?: unknown, cookie = '', origin = 'https://shop.example.invalid') => worker.fetch(new Request('https://shop.example.invalid' + path, { method: data === undefined ? 'GET' : 'POST', headers: { Origin: origin, Cookie: cookie, 'Content-Type': 'application/json' }, body: data === undefined ? undefined : JSON.stringify(data) }), env)
  const buyer = async () => (await call('/api/orders/client', {})).headers.get('Set-Cookie')!.split(';')[0]
  const create = async (cookie: string, key = crypto.randomUUID()) => {
    const response = await call('/api/orders', { plan: 'plus', expectedPrice: 13900, email: 'buyer@example.test', key }, cookie)
    assert.equal(response.status, 201, await response.clone().text()); return (await response.json()).order
  }
  const login = async () => (await call('/api/admin/login', { username: 'tester', code: await totpCode(env.ADMIN_TOTP_SECRET!), ...await seedAdminEmailCode(db, env) })).headers.get('Set-Cookie')!.split(';')[0]
  let merchant = ''; let retail = '139.00'; let payment = 'pending'; let taskStatus = 'queued'; let fallback = false; let refundStatus = 'requested'; let refundNo = ''; let refundAmount = '139.00'
  let redemptionId = 'ful_test'; const redemptionKeys = new Map<string, string>()
  let faceToFace = false; let qr: string | null = null
  let expiry = new Date(Date.now() + 900000).toISOString()
  let quotePatch: Record<string, unknown> = {}
  const calls: { path: string; body: string; key: string; nonce: string }[] = []
  const products = [{ product_code: 'demo_plus', supply_price: '110.00', max_sale_price: '159.00', currency: 'CNY', max_quantity: 1, available: true, delivery_modes: ['auto_recharge', 'cdk'] }]
  const remoteOrder = () => ({ order_id: 'ord_test', merchant_order_no: merchant, product_code: products[0].product_code, quantity: 1, sale_amount: retail, supply_amount: products[0].supply_price, merchant_margin: Math.max(0, Number(retail) - Number(products[0].supply_price) - (refundStatus === 'succeeded' ? Number(refundAmount) : 0)).toFixed(2), currency: 'CNY', collection_mode: 'platform_collect', payment_scope: 'retail', delivery_mode: 'auto_recharge', payment_status: payment, qr_payload: faceToFace ? 'https://recharge.example.invalid/payments/ord_test?token=public-test-token-123456' : 'https://recharge.example.invalid/pay/example', expires_at: expiry, refunded_amount: refundStatus === 'succeeded' ? refundAmount : '0.00' })
  const redemption = () => ({ redemption_id: redemptionId, order_id: 'ord_test', status: taskStatus, failure_code: taskStatus === 'failed' ? 'session_invalid' : null, fallback_recharge_available: fallback, retry_allowed: fallback, next_action: fallback ? 'resubmit' : ['queued','running'].includes(taskStatus) ? 'wait' : 'none', account_email_masked: 't***@example.test', finished_at: ['succeeded','failed'].includes(taskStatus) ? new Date().toISOString() : null, message: 'PRIVATE-TOKEN-NOT-FOR-STORAGE' })
  const remoteRefund = () => ({ refund_id: 'rf_test', order_id: 'ord_test', merchant_refund_no: refundNo, amount: refundAmount, status: refundStatus })
  let override: ((path: string, raw: string) => Response | Promise<Response | undefined> | undefined) | undefined
  t.mock.method(globalThis, 'fetch', async (input: string, options: RequestInit) => {
    const url = new URL(input); const headers = new Headers(options.headers); const raw = String(options.body ?? '')
    assert.equal(url.origin, 'https://recharge.example.invalid'); assert.equal(options.redirect, 'manual')
    if (url.pathname.startsWith('/payments/')) {
      assert.equal(url.search, '?token=public-test-token-123456')
      assert.equal(headers.get('X-Signature'), null)
      calls.push({ path: url.pathname, body: raw, key: '', nonce: '' })
      const changed = await override?.(url.pathname, raw); if (changed) return changed
      if (url.pathname.endsWith('/start')) { assert.equal(options.method, 'POST'); qr = 'https://qr.alipay.com/bax01234567890' }
      else assert.equal(options.method, 'GET')
      return Response.json({ status: payment, amount: retail, expires_at: expiry, expired: false, can_start: true, qr_code: qr, delivery_mode: 'auto_recharge', ...quotePatch })
    }
    assert.equal(headers.get('X-Partner-Id'), env.RECHARGE_PARTNER_ID); assert.equal(headers.get('X-Key-Id'), env.RECHARGE_KEY_ID)
    const canonical = [options.method, url.pathname, '', headers.get('X-Timestamp'), headers.get('X-Nonce'), headers.get('X-Key-Id'), headers.get('Idempotency-Key') ?? '', digest(raw)].join('\n')
    assert.equal(headers.get('X-Signature'), mac(canonical))
    calls.push({ path: url.pathname, body: raw, key: headers.get('Idempotency-Key') ?? '', nonce: headers.get('X-Nonce')! })
    const changed = await override?.(url.pathname, raw); if (changed) return changed
    let data: unknown
    if (url.pathname === '/v1/products') data = products
    else if (url.pathname === '/v1/payment-methods') data = [{ code: 'alipay' }]
    else if (url.pathname === '/v1/orders' && options.method === 'POST') { const payload = JSON.parse(raw); merchant = payload.merchant_order_no; retail = payload.sale_amount; data = remoteOrder() }
    else if (url.pathname === '/v1/orders/ord_test') data = remoteOrder()
    else if (url.pathname === '/v1/redemptions' && options.method === 'POST') { const key = headers.get('Idempotency-Key')!; if (!redemptionKeys.has(key)) redemptionKeys.set(key, redemptionKeys.size === 0 ? 'ful_test' : 'ful_test_' + (redemptionKeys.size + 1)); redemptionId = redemptionKeys.get(key)!; data = redemption() }
    else if (url.pathname.startsWith('/v1/redemptions/ful_test')) data = redemption()
    else if (url.pathname === '/v1/orders/ord_test/refunds') { const value = JSON.parse(raw); refundNo = value.merchant_refund_no; refundAmount = value.amount; data = remoteRefund() }
    else if (url.pathname === '/v1/refunds/rf_test') data = remoteRefund()
    else throw new Error('Unexpected mocked endpoint')
    return Response.json({ data })
  })
  const query = (id: string, cookie: string) => call(`/api/orders/${id}/fulfillment`, { action: 'query' }, cookie)
  const submit = (id: string, cookie: string, key = crypto.randomUUID(), value = session) => call(`/api/orders/${id}/fulfillment`, { action: 'submit', key, customerConfirmedEmail: true, credential: { mode: 'session', session: value } }, cookie)
  const partner = () => {
    env.PARTNER_AUTH_SECRET = 'partner-auth-private-secret-of-at-least-32-characters'
    const token = 'a'.repeat(64); const now = Date.now()
    db.prepare('INSERT INTO partners(id,slug,name,email,created_at,updated_at) VALUES (?,?,?,?,?,?)').run('partner-test','partner-test','测试代理','partner@example.test',now,now)
    db.prepare('INSERT INTO partner_prices(partner_id,plan,cost_cents,price_cents) VALUES (?,?,?,?)').run('partner-test','plus',11000,15900)
    db.prepare('INSERT INTO partner_sessions(token_hash,partner_id,auth_version,credential_version,expires_at) VALUES (?,?,?,?,?)').run(digest(token),'partner-test',0,digest(env.PARTNER_AUTH_SECRET),now+3600000)
    return '__Host-ymx_partner=' + token
  }
  const notice = (id: string) => {
    const stamp = String(Math.floor(Date.now()/1000)); const raw = JSON.stringify({ event_id: id, type: 'refund.succeeded' })
    return worker.fetch(new Request('https://shop.example.invalid/api/recharge/webhook', { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-Quefa-Event': 'refund.succeeded', 'X-Quefa-Event-Id': id, 'X-Quefa-Delivery': 'dlv_'+id, 'X-Quefa-Timestamp': stamp, 'X-Quefa-Signature': `t=${stamp},v1=${mac(stamp+'.'+raw,webhookSecret)}` }, body: raw }), env)
  }
  return { db, env, call, buyer, create, query, submit, login, partner, notice, calls, products, remoteOrder, redemption, faceToFace: () => { faceToFace = true }, quote: (patch: Record<string, unknown>) => { quotePatch = patch }, setExpiry: (value: number) => { expiry = new Date(value).toISOString() }, setQr: (value: string | null) => { qr = value }, setPayment: (v: string) => { payment = v }, setTask: (v: string, retry = false) => { taskStatus = v; fallback = retry }, setRefund: (v: string) => { refundStatus = v }, override: (v: typeof override) => { override = v } }
}

test('admin can read and search the stored upstream order without contacting the supplier', async t => {
  const s = setup(t); const buyer = await s.buyer(); const order = await s.create(buyer); const admin = await s.login()
  await s.query(order.id, buyer)
  assert.equal(s.db.prepare('SELECT remote_id FROM recharge_orders WHERE order_id=?').get(order.id)!.remote_id, 'ord_test')
  const before = s.calls.length
  s.override(() => { throw new Error('supplier unavailable') })
  const detail = (await (await s.call(`/api/admin/orders/${order.id}`, undefined, admin)).json()).order
  assert.equal(detail.platformStatus.remoteId, 'ord_test')
  for (const q of ['ord_test', 'ORD_TEST', 'ord_te', order.id, 'buyer@example.test', ' ord_test ']) {
    const response = await s.call('/api/admin/orders?' + new URLSearchParams({ q }), undefined, admin)
    assert.equal(response.status, 200)
    const result = await response.json()
    assert.equal(result.total, 1); assert.equal(result.orders[0].id, order.id)
    assert.equal(result.orders[0].platformStatus.remoteId, 'ord_test')
  }
  for (const filter of ['q=missing', 'q=ord_test&status=success', 'q=ord_test&plan=pro-5x']) {
    const result = await (await s.call('/api/admin/orders?' + filter, undefined, admin)).json()
    assert.equal(result.total, 0); assert.deepEqual(result.orders, [])
  }
  const customer = (await (await s.call(`/api/orders/${order.id}`, undefined, buyer)).json()).order
  assert.equal(customer.platformStatus, undefined)
  assert.equal((await s.call('/api/admin/orders?q=ord_test', undefined, buyer)).status, 401)
  assert.equal(s.calls.length, before)
})

test('documented request errors retain safe codes, retry hints and diagnostics without replaying writes or exposing prose', async t => {
  const s = setup(t)
  for (const [code, status, retryable] of [
    ['invalid_request', 400, false], ['invalid_signature', 401, false], ['timestamp_out_of_range', 401, false],
    ['nonce_replayed', 409, false], ['ip_not_allowed', 403, false], ['product_not_authorized', 403, false],
    ['order_not_found', 404, false], ['idempotency_conflict', 409, false], ['idempotency_in_progress', 409, false],
    ['invalid_state_transition', 409, false], ['provider_refund_reconciliation_required', 409, false],
    ['price_out_of_range', 422, false], ['rate_limited', 429, true], ['temporarily_unavailable', 503, true],
  ] as const) {
    s.override(() => Response.json({ error: { code, retryable: false, request_id: 'req_body', message: session } }, { status, headers: { 'X-Request-Id': 'req_header', 'Retry-After': '23' } }))
    const before = s.calls.length
    await assert.rejects(supplierRequest(s.env, '/redemptions', '{}', 'test-error-key'), e => {
      assert.ok(e instanceof RechargeError)
      assert.equal(e.code, code); assert.equal(e.retryable, retryable); assert.equal(e.upstreamStatus, status)
      assert.equal(e.requestId, 'req_header'); assert.equal(e.retryAfter, 23)
      assert.doesNotMatch(e.message + JSON.stringify(e), /PRIVATE-TOKEN|target@example/)
      return true
    })
    assert.equal(s.calls.length, before + 1)
  }
  s.override(() => Response.json({ error: { code: session, retryable: true, request_id: 'req_body', message: session } }, { status: 500, headers: { 'Retry-After': new Date(Date.now() + 60000).toUTCString() } }))
  await assert.rejects(supplierRequest(s.env, '/products'), e => {
    assert.ok(e instanceof RechargeError); assert.equal(e.code, 'request_rejected'); assert.equal(e.requestId, 'req_body'); assert.equal(e.retryable, true)
    assert.ok(e.retryAfter! >= 59 && e.retryAfter! <= 60); assert.doesNotMatch(JSON.stringify(e), /PRIVATE-TOKEN/); return true
  })
})

test('unreadable and malformed responses remain unknown instead of becoming definitive request rejection', async t => {
  const s = setup(t)
  for (const response of [
    new Response('x'.repeat(256001)), new Response(new Uint8Array([0xff])), new Response('{'), Response.json({ data: null }),
    Response.json({ error: { code: 'invalid_request', retryable: false } }),
    new Response(new ReadableStream({ start(controller) { controller.error(new Error(session)) } })),
  ]) {
    s.override(() => response)
    await assert.rejects(supplierRequest(s.env, '/products'), e => {
      assert.ok(e instanceof RechargeError); assert.equal(e.code, 'invalid_response'); assert.equal(e.retryable, true)
      assert.doesNotMatch(e.message, /PRIVATE-TOKEN/); return true
    })
  }
})

test('in-progress conflicts preserve the original recharge key and return safe retry metadata', async t => {
  const s = setup(t); const buyer = await s.buyer(); const order = await s.create(buyer)
  await s.query(order.id, buyer); s.setPayment('paid'); await s.query(order.id, buyer)
  s.override(path => path === '/v1/redemptions' ? Response.json({ error: { code: 'idempotency_in_progress', retryable: false, message: session, request_id: 'req_private' } }, { status: 409, headers: { 'Retry-After': '15' } }) : undefined)
  const key = crypto.randomUUID(); const response = await s.submit(order.id, buyer, key); const payload = await response.json()
  assert.equal(response.status, 503); assert.equal(response.headers.get('Retry-After'), '15')
  assert.equal(payload.code, 'idempotency_in_progress'); assert.equal(payload.retryAfter, 15); assert.match(payload.error, /原请求仍在处理中/)
  assert.doesNotMatch(JSON.stringify(payload), /PRIVATE-TOKEN|req_private/)
  assert.equal(s.db.prepare('SELECT status FROM recharge_redemptions WHERE id=?').get(key)!.status, 'unknown')
  assert.equal((await s.submit(order.id, buyer)).status, 409)
  assert.equal(s.calls.filter(c => c.path === '/v1/redemptions').length, 1)
})

test('public result codes supply missing stages without announcing premature success or permitting another recharge', async t => {
  const s = setup(t); const buyer = await s.buyer(); const order = await s.create(buyer)
  await s.query(order.id, buyer); s.setPayment('paid'); await s.query(order.id, buyer); await s.submit(order.id, buyer)
  let version = 1
  for (const [code, stage] of [['funding_pending', 'preparing_funds'], ['pending', 'confirming'], ['plus_paid', 'upgrading'], ['review', 'review'], ['completed', null]]) {
    s.override(path => path.startsWith('/v1/redemptions/') ? Response.json({ data: { ...s.redemption(), status: 'running', result_code: code, error_category: 'confirmation', progress_version: version++ } }, { status: 202 }) : undefined)
    const current = (await (await s.query(order.id, buyer)).json()).order
    assert.equal(current.status, 'processing'); assert.equal(current.fulfillment.canSubmit, false)
    assert.equal(current.fulfillment.progressStage, stage); assert.equal(current.fulfillment.resultCode, code)
    assert.equal(current.fulfillment.errorCategory, 'confirmation')
  }
  assert.equal(s.calls.filter(c => c.path === '/v1/redemptions').length, 1)
})

test('contradictory terminal result snapshots cannot mark success or unlock another attempt', async t => {
  const s = setup(t); const buyer = await s.buyer(); const order = await s.create(buyer)
  await s.query(order.id, buyer); s.setPayment('paid'); await s.query(order.id, buyer); await s.submit(order.id, buyer)
  for (const status of ['succeeded', 'failed', 'cancelled']) {
    s.override(path => path.startsWith('/v1/redemptions/') ? Response.json({ data: { ...s.redemption(), status, result_code: 'review', progress_version: 2, retry_allowed: true, fallback_recharge_available: true, next_action: 'resubmit' } }) : undefined)
    assert.equal((await s.query(order.id, buyer)).status, 503)
    assert.equal(s.db.prepare('SELECT status FROM orders WHERE id=?').get(order.id)!.status, 'processing')
    assert.equal(s.db.prepare('SELECT fallback FROM recharge_redemptions WHERE order_id=?').get(order.id)!.fallback, 0)
  }
})

test('legacy unversioned result stages advance and a versioned unified stage takes precedence', async t => {
  const s = setup(t); const buyer = await s.buyer(); const order = await s.create(buyer)
  await s.query(order.id, buyer); s.setPayment('paid'); await s.query(order.id, buyer); await s.submit(order.id, buyer)
  for (const code of ['queued', 'review']) {
    s.override(path => path.startsWith('/v1/redemptions/') ? Response.json({ data: { ...s.redemption(), status: 'running', result_code: code } }) : undefined)
    const current = (await (await s.query(order.id, buyer)).json()).order
    assert.equal(current.fulfillment.progressStage, code)
  }
  s.override(path => path.startsWith('/v1/redemptions/') ? Response.json({ data: { ...s.redemption(), status: 'running', result_code: 'pending', progress_stage: 'review', progress_version: 2 } }) : undefined)
  assert.equal((await (await s.query(order.id, buyer)).json()).order.fulfillment.progressStage, 'review')
  s.override(path => path.startsWith('/v1/redemptions/') ? Response.json({ data: { ...s.redemption(), status: 'running', result_code: 'queued', error_category: 'service', progress_version: 1 } }) : undefined)
  const current = (await (await s.query(order.id, buyer)).json()).order
  assert.equal(current.fulfillment.resultCode, 'pending'); assert.equal(current.fulfillment.errorCategory, null)
  assert.equal(current.fulfillment.progressStage, 'review'); assert.equal(current.fulfillment.progressVersion, 2)
})

test('mailbox failures and public error categories give safe reasons while unknown codes never reach storage', async t => {
  const s = setup(t); const buyer = await s.buyer(); const order = await s.create(buyer)
  await s.query(order.id, buyer); s.setPayment('paid'); await s.query(order.id, buyer); await s.submit(order.id, buyer)
  s.setTask('failed')
  let patch: Record<string, unknown> = { failure_code: 'mailbox_login_failed', error_category: 'credential', result_code: 'declined', progress_version: 3 }
  s.override(path => path.startsWith('/v1/redemptions/') ? Response.json({ data: { ...s.redemption(), ...patch } }) : undefined)
  let current = (await (await s.query(order.id, buyer)).json()).order
  assert.equal(current.fulfillment.failureCode, 'mailbox_login_failed'); assert.match(current.fulfillment.message, /邮箱登录失败/)
  assert.equal(current.fulfillment.canSubmit, false)
  patch = { failure_code: session, error_category: 'account', result_code: session, progress_version: 4 }
  current = (await (await s.query(order.id, buyer)).json()).order
  assert.equal(current.fulfillment.failureCode, 'other'); assert.equal(current.fulfillment.resultCode, null)
  assert.match(current.fulfillment.message, /账号状态需要核对/)
  patch = { failure_code: null, error_category: session, result_code: null, progress_version: 5 }
  current = (await (await s.query(order.id, buyer)).json()).order
  assert.equal(current.fulfillment.errorCategory, null); assert.match(current.fulfillment.message, /联系客服/)
  assert.doesNotMatch(JSON.stringify(current) + JSON.stringify(s.db.prepare('SELECT * FROM recharge_redemptions').all()), /PRIVATE-TOKEN/)
})

test('confirmed recharge progress is versioned per attempt, survives stale or failed queries and never forwards supplier text', async t => {
  const s = setup(t); const buyer = await s.buyer(); const order = await s.create(buyer)
  await s.query(order.id, buyer); s.setPayment('paid'); await s.query(order.id, buyer)
  let version = 2; let stage = 'logging_in'
  const updatedAt = '2026-10-01T02:00:00.000Z'
  s.override(path => path.startsWith('/v1/redemptions') ? Response.json({ data: { ...s.redemption(), status: 'running', progress_stage: stage, progress_version: version, progress_updated_at: updatedAt, attempt_no: 1 } }) : undefined)
  let current = (await (await s.submit(order.id, buyer)).json()).order
  assert.equal(current.status, 'processing')
  assert.equal(current.fulfillment.progressStage, 'logging_in')
  assert.equal(current.fulfillment.progressUpdatedAt, Date.parse(updatedAt))
  assert.equal(current.fulfillment.progressVersion, 2)
  assert.equal(current.fulfillment.attemptNo, 1)
  version = 3; stage = 'upgrading'
  current = (await (await s.query(order.id, buyer)).json()).order
  assert.equal(current.status, 'processing'); assert.equal(current.fulfillment.progressStage, 'upgrading')
  version = 2; stage = 'queued'
  current = (await (await s.query(order.id, buyer)).json()).order
  assert.equal(current.fulfillment.progressStage, 'upgrading'); assert.equal(current.fulfillment.progressVersion, 3)
  s.override(path => path.startsWith('/v1/redemptions') ? Response.json({ error: { code: 'temporarily_unavailable', retryable: true } }, { status: 503 }) : undefined)
  assert.equal((await s.query(order.id, buyer)).status, 503)
  const saved = s.db.prepare('SELECT * FROM recharge_redemptions WHERE order_id=?').get(order.id)!
  assert.equal(saved.status, 'running'); assert.equal(saved.progress_stage, 'upgrading')
  assert.doesNotMatch(JSON.stringify(saved), /PRIVATE-TOKEN/)
  s.db.prepare('UPDATE recharge_orders SET updated_at=0 WHERE order_id=?').run(order.id)
  const cached = (await (await s.call(`/api/orders/${order.id}`, undefined, buyer)).json()).order
  assert.equal(cached.fulfillment.syncError, true)
  assert.equal(cached.status, 'processing'); assert.equal(cached.fulfillment.progressStage, 'upgrading')
  s.override(path => path.startsWith('/v1/redemptions') ? Response.json({ data: { ...s.redemption(), status: 'running', progress_stage: 'PRIVATE-TOKEN', progress_version: 4 } }) : undefined)
  current = (await (await s.query(order.id, buyer)).json()).order
  assert.equal(current.fulfillment.progressStage, null); assert.equal(current.status, 'processing')
  assert.doesNotMatch(JSON.stringify(current), /PRIVATE-TOKEN/)
})

test('recovery requires all platform retry flags, can be revoked at the same progress version and resets progress for a new attempt', async t => {
  const s = setup(t); const buyer = await s.buyer(); const order = await s.create(buyer)
  await s.query(order.id, buyer); s.setPayment('paid'); await s.query(order.id, buyer); await s.submit(order.id, buyer)
  s.setTask('failed', true)
  let allowed = false; let refund = false
  s.override(path => path.startsWith('/v1/redemptions/') ? Response.json({ data: { ...s.redemption(), progress_stage: 'failed', progress_version: 5, retry_allowed: allowed, recovery_action: refund ? 'refund' : 'retry' } }) : undefined)
  let current = (await (await s.query(order.id, buyer)).json()).order
  assert.equal(current.fulfillment.canSubmit, false)
  allowed = true
  current = (await (await s.query(order.id, buyer)).json()).order
  assert.equal(current.fulfillment.canSubmit, true)
  refund = true
  current = (await (await s.query(order.id, buyer)).json()).order
  assert.equal(current.fulfillment.canSubmit, false)
  refund = false
  await s.query(order.id, buyer)
  s.override(undefined); s.setTask('queued')
  current = (await (await s.submit(order.id, buyer)).json()).order
  assert.equal(current.fulfillment.attemptNo, 2)
  assert.equal(current.fulfillment.progressStage, null); assert.equal(current.fulfillment.progressVersion, 0)
  assert.equal(s.calls.filter(c => c.path === '/v1/orders').length, 1)
})

test('fulfillment.updated is verified, deduplicated and reconciles public progress through the original task', async t => {
  const s = setup(t); const buyer = await s.buyer(); const order = await s.create(buyer)
  await s.query(order.id, buyer); s.setPayment('paid'); await s.query(order.id, buyer); await s.submit(order.id, buyer)
  const timestamp = String(Math.floor(Date.now() / 1000))
  const raw = JSON.stringify({ event_id: 'evt_progress', event: 'fulfillment.updated', data: { order_id: 'ord_test', redemption_id: 'ful_test', attempt_no: 1, status: 'succeeded', progress_stage: 'completed', progress_version: 999 } })
  const notify = () => worker.fetch(new Request('https://shop.example.invalid/api/recharge/webhook', { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-Quefa-Event': 'fulfillment.updated', 'X-Quefa-Event-Id': 'evt_progress', 'X-Quefa-Delivery': 'dlv_progress', 'X-Quefa-Timestamp': timestamp, 'X-Quefa-Signature': `t=${timestamp},v1=${mac(timestamp + '.' + raw, webhookSecret)}` }, body: raw }), s.env)
  assert.equal((await notify()).status, 200); assert.equal((await notify()).status, 200)
  assert.equal(s.db.prepare('SELECT notify_version FROM recharge_orders WHERE order_id=?').get(order.id)!.notify_version, 1)
  assert.equal(s.db.prepare('SELECT status FROM orders WHERE id=?').get(order.id)!.status, 'processing')
  s.override(path => path === '/v1/redemptions/ful_test' ? Response.json({ data: { ...s.redemption(), status: 'running', progress_stage: 'dispatching', progress_version: 3 } }) : undefined)
  await reconcileRecharge(s.env)
  const current = (await (await s.call(`/api/orders/${order.id}`, undefined, buyer)).json()).order
  assert.equal(current.status, 'processing'); assert.equal(current.fulfillment.progressStage, 'dispatching')
  assert.equal(current.fulfillment.progressVersion, 3)
  assert.equal(s.calls.filter(c => c.path === '/v1/redemptions').length, 1)
})

test('25x new subscriptions use the confirmed supplier product, verified payment and idempotent automatic recharge', async t => {
  const s = setup(t)
  s.env.RECHARGE_PRODUCT_MAP = JSON.stringify({ 'pro-25x': 'demo_pro_large' })
  Object.assign(s.products[0], { product_code: 'demo_pro_large', supply_price: '3250.00', max_sale_price: '5000.00' })
  const buyer = await s.buyer()
  const payload = { plan: 'pro-25x', expectedPrice: 450000, email: 'buyer@example.test', key: crypto.randomUUID() }
  const catalog = await (await s.call('/api/catalog')).json()
  assert.equal(catalog.plans.find((p: { id: string }) => p.id === 'pro-25x').enabled, 1)
  const created = await s.call('/api/orders', payload, buyer)
  assert.equal(created.status, 201, await created.clone().text())
  const order = (await created.json()).order
  assert.equal(order.mode, 'live'); assert.equal(order.manualFulfillment, false)
  assert.equal(order.planName, 'Pro 25×'); assert.equal(order.fulfillment.canSubmit, false)
  const snapshot = s.db.prepare('SELECT product_code,create_body FROM recharge_orders WHERE order_id=?').get(order.id)!
  assert.equal(snapshot.product_code, 'demo_pro_large')
  const stored = JSON.parse(snapshot.create_body as string)
  assert.equal(stored.delivery_mode, 'auto_recharge'); assert.equal(stored.collection_mode, 'platform_collect')
  assert.equal(stored.sale_amount, '4500.00')
  assert.equal((await (await s.call('/api/orders', payload, buyer)).json()).order.id, order.id)
  assert.equal((await s.call(`/api/orders/${order.id}/payment`, { action: 'create' }, buyer)).status, 409)
  assert.equal((await s.submit(order.id, buyer)).status, 409)
  assert.equal(s.calls.filter(c => c.path === '/v1/redemptions').length, 0)
  await s.query(order.id, buyer); s.setPayment('paid')
  let current = (await (await s.query(order.id, buyer)).json()).order
  assert.equal(current.status, 'session'); assert.equal(current.paymentStatus, 'paid'); assert.equal(current.fulfillment.canSubmit, true)
  const admin = await s.login()
  assert.equal((await s.call(`/api/admin/orders/${order.id}/actions`, { action: 'manual-complete', version: current.version, note: '不得手工完成自动充值订单' }, admin)).status, 409)
  const key = crypto.randomUUID()
  assert.equal((await s.submit(order.id, buyer, key)).status, 200)
  assert.equal((await s.submit(order.id, buyer, key)).status, 200)
  assert.equal(s.calls.filter(c => c.path === '/v1/orders').length, 1)
  assert.equal(s.calls.filter(c => c.path === '/v1/redemptions').length, 1)
  current = (await (await s.call(`/api/orders/${order.id}`, undefined, buyer)).json()).order
  assert.equal(current.status, 'processing'); assert.equal(current.manualFulfillment, false)
  assert.equal((await s.call(`/api/admin/orders/${order.id}/actions`, { action: 'manual-complete', version: current.version, note: '不得手工完成自动充值订单' }, admin)).status, 409)
  s.setTask('succeeded')
  current = (await (await s.query(order.id, buyer)).json()).order
  assert.equal(current.status, 'success'); assert.equal(current.manualFulfillment, false)
  assert.equal(current.fulfillment.accountEmailMasked, 't***@example.test')
  assert.doesNotMatch(JSON.stringify(s.db.prepare('SELECT * FROM recharge_redemptions').all()), /PRIVATE-TOKEN-NOT-FOR-STORAGE/)
})

test('25x automatic purchases require an authorized available product and safe supplier and partner prices', async t => {
  const s = setup(t); const buyer = await s.buyer()
  const payload = () => ({ plan: 'pro-25x', expectedPrice: 450000, email: 'buyer@example.test', key: crypto.randomUUID() })
  const enabled = async () => (await (await s.call('/api/catalog')).json()).plans.find((p: { id: string }) => p.id === 'pro-25x').enabled
  assert.equal(await enabled(), 0)
  assert.equal((await s.call('/api/orders', payload(), buyer)).status, 409)
  s.env.RECHARGE_PRODUCT_MAP = JSON.stringify({ 'pro-25x': 'demo_pro_large' })
  assert.equal(await enabled(), 1)
  assert.equal((await s.call('/api/orders', payload(), buyer)).status, 409)
  Object.assign(s.products[0], { product_code: 'demo_pro_large', supply_price: '3250.00', max_sale_price: '5000.00', available: false })
  assert.equal(await enabled(), 1)
  assert.equal((await s.call('/api/orders', payload(), buyer)).status, 409)
  s.products[0].available = true; s.products[0].delivery_modes = ['cdk']
  assert.equal(await enabled(), 1)
  assert.equal((await s.call('/api/orders', payload(), buyer)).status, 409)
  s.products[0].delivery_modes = ['auto_recharge']; s.products[0].supply_price = '4501.00'
  assert.equal((await s.call('/api/orders', payload(), buyer)).status, 409)
  s.products[0].supply_price = '3250.00'; s.products[0].max_sale_price = '4499.00'
  assert.equal((await s.call('/api/orders', payload(), buyer)).status, 409)
  s.products[0].max_sale_price = '5000.00'; s.partner()
  s.db.prepare('INSERT INTO partner_prices(partner_id,plan,cost_cents,price_cents) VALUES (?,?,?,?)').run('partner-test', 'pro-25x', 324999, 450000)
  assert.equal((await s.call('/api/orders', { ...payload(), partner: 'partner-test' }, buyer)).status, 409)
  s.db.prepare('UPDATE partner_prices SET cost_cents=330000 WHERE partner_id=? AND plan=?').run('partner-test', 'pro-25x')
  assert.equal((await s.call('/api/orders', { ...payload(), partner: 'partner-test' }, buyer)).status, 201)
  assert.equal(s.db.prepare('SELECT COUNT(*) n FROM orders').get()!.n, 1)
  assert.equal(s.calls.filter(c => c.path === '/v1/orders').length, 0)
})

async function approveRefund(s: ReturnType<typeof setup>, id: string, buyer: string, admin: string, amount: number) {
  const current = await getOrder(s.env,id)
  assert.equal((await s.call(`/api/orders/${id}/refunds`, { key: crypto.randomUUID(), version: current.version, reason: '申请售后退款' }, buyer)).status,201)
  const refund = s.db.prepare('SELECT * FROM refund_cases WHERE order_id=? ORDER BY created_at DESC LIMIT 1').get(id)!
  assert.equal((await s.call('/api/admin/refunds/'+refund.id, { action: 'approve', version: 0, amountCents: amount, note: '同意退款' }, admin)).status,200)
  return refund.id
}

async function partnerOrder(s: ReturnType<typeof setup>, buyer: string) {
  const response = await s.call('/api/orders',{ plan: 'plus', partner: 'partner-test', expectedPrice: 15900, email: 'buyer@example.test', key: crypto.randomUUID() },buyer)
  assert.equal(response.status,201,await response.clone().text())
  const order = (await response.json()).order
  await s.query(order.id,buyer); s.setPayment('paid'); await s.query(order.id,buyer); await s.submit(order.id,buyer); s.setTask('succeeded'); await s.query(order.id,buyer)
  return order
}

test('manual marker blocks failed fallback recharges and original-key replay across queries, webhooks and reconciliation', async t => {
  const s = setup(t); const buyer = await s.buyer(); const order = await s.create(buyer); const admin = await s.login()
  await s.query(order.id,buyer); s.setPayment('paid'); await s.query(order.id,buyer)
  const key = crypto.randomUUID(); await s.submit(order.id,buyer,key); s.setTask('failed',true); await s.query(order.id,buyer)
  let current = await getOrder(s.env,order.id)
  assert.equal(current.status,'failed')
  const path = `/api/admin/orders/${order.id}/actions`
  const mark = await s.call(path,{action:'redemption-control',blocked:true,note:'人工退款已处理',version:current.version},admin)
  assert.equal(mark.status,200,await mark.clone().text())
  assert.equal((await mark.json()).order.fulfillment.canSubmit,false)
  const before = s.calls.filter(c => c.path === '/v1/redemptions').length
  for (const retryKey of [key,crypto.randomUUID()]) assert.equal((await s.submit(order.id,buyer,retryKey)).status,409)
  assert.equal((await s.query(order.id,buyer)).status,200)
  await s.notice('blocked-order'); await reconcileRecharge(s.env)
  const visible = (await (await s.call('/api/orders/'+order.id,undefined,buyer)).json()).order
  const listed = (await (await s.call('/api/orders',undefined,buyer)).json()).orders.find((o: {id:string})=>o.id===order.id)
  assert.equal(visible.redemptionBlocked,true); assert.equal(listed.redemptionBlocked,true)
  assert.equal(visible.fulfillment.canSubmit,false); assert.equal(visible.status,'failed')
  assert.equal(s.calls.filter(c => c.path === '/v1/redemptions').length,before)
  assert.equal(s.calls.some(c => c.path.includes('/refunds')),false)
  assert.equal(visible.refundedCents,0)
  current = await getOrder(s.env,order.id)
  assert.equal((await s.call(path,{action:'redemption-control',blocked:false,note:'核对后恢复兑换',version:current.version},admin)).status,200)
  assert.equal((await s.submit(order.id,buyer)).status,200)
  assert.equal(s.calls.filter(c => c.path === '/v1/redemptions').length,before+1)
})

test('manual marker cannot race an in-flight recharge; a completed marker prevents later upstream writes', async t => {
  const s = setup(t); const buyer = await s.buyer(); const order = await s.create(buyer); const admin = await s.login()
  await s.query(order.id,buyer); s.setPayment('paid'); await s.query(order.id,buyer)
  let entered!: () => void; let release!: () => void
  const started = new Promise<void>(resolve => { entered=resolve })
  const gate = new Promise<void>(resolve => { release=resolve }); t.after(()=>release())
  s.override(async path => { if (path==='/v1/redemptions') { entered(); await gate } })
  const pending = s.submit(order.id,buyer)
  await started
  const current = await getOrder(s.env,order.id)
  const path = `/api/admin/orders/${order.id}/actions`
  const blocked = await s.call(path,{action:'redemption-control',blocked:true,note:'停止再次兑换',version:current.version},admin)
  assert.equal(blocked.status,409)
  assert.equal((await getOrder(s.env,order.id)).redemption_blocked,0)
  release(); assert.equal((await pending).status,200)
  const fresh = await getOrder(s.env,order.id)
  assert.equal((await s.call(path,{action:'redemption-control',blocked:true,note:'停止再次兑换',version:fresh.version},admin)).status,200)
  const writes = s.calls.filter(c=>c.path==='/v1/redemptions').length
  assert.equal((await s.submit(order.id,buyer)).status,409)
  assert.equal(s.calls.filter(c=>c.path==='/v1/redemptions').length,writes)
})

test('blocked unknown recharge cannot resend its original credentials or create a fresh attempt', async t => {
  const s = setup(t); const buyer = await s.buyer(); const order = await s.create(buyer); const admin = await s.login()
  await s.query(order.id,buyer); s.setPayment('paid'); await s.query(order.id,buyer)
  s.override(path=>{if(path==='/v1/redemptions') throw new Error('lost response')})
  const key=crypto.randomUUID(); assert.equal((await s.submit(order.id,buyer,key)).status,503)
  const current=await getOrder(s.env,order.id)
  assert.equal((await s.call(`/api/admin/orders/${order.id}/actions`,{action:'redemption-control',blocked:true,note:'人工处理完成',version:current.version},admin)).status,200)
  const writes=s.calls.filter(c=>c.path==='/v1/redemptions').length
  for(const retryKey of [key,crypto.randomUUID()]) assert.equal((await s.submit(order.id,buyer,retryKey)).status,409)
  assert.equal(s.calls.filter(c=>c.path==='/v1/redemptions').length,writes)
  assert.equal(s.db.prepare('SELECT COUNT(*) n FROM recharge_redemptions').get()!.n,1)
})

test('blocked pending orders cannot create or expose payment entrances, while an existing payment can still be reconciled', async t => {
  const s = setup(t); s.faceToFace(); const buyer = await s.buyer(); const order = await s.create(buyer); const admin = await s.login()
  const path=`/api/admin/orders/${order.id}/actions`
  assert.equal((await s.call(path,{action:'redemption-control',blocked:true,note:'停止未付款订单',version:0},admin)).status,200)
  assert.equal((await s.query(order.id,buyer)).status,409)
  assert.equal(s.calls.filter(c=>c.path==='/v1/orders').length,0)
  assert.equal((await s.call(path,{action:'redemption-control',blocked:false,note:'核对后恢复',version:1},admin)).status,200)
  await s.query(order.id,buyer)
  let current=await getOrder(s.env,order.id)
  assert.equal((await s.call(`/api/admin/orders/${order.id}/actions`,{action:'redemption-control',blocked:true,note:'作废不退款',version:current.version},admin)).status,200)
  const starts=s.calls.filter(c=>c.path==='/payments/ord_test/start').length
  const response=await s.query(order.id,buyer)
  assert.equal(response.status,200)
  const visible=(await response.json()).order
  assert.equal(visible.fulfillment.paymentQr,null); assert.equal(visible.fulfillment.paymentUrl,null)
  assert.equal(s.calls.filter(c=>c.path==='/payments/ord_test/start').length,starts)
  s.setPayment('paid'); await s.query(order.id,buyer)
  current=await getOrder(s.env,order.id)
  assert.equal(current.payment_status,'paid'); assert.equal(current.redemption_blocked,1)
  assert.equal((await s.submit(order.id,buyer)).status,409)
})

test('HMAC canonicalization covers empty bodies, repeated query values, RFC3986 characters and exact JSON bytes', async () => {
  const url = new URL('https://recharge.example.invalid/v1/orders?z=a%2Bb&x=%21&x=a%20b')
  assert.equal(canonicalQuery(url.searchParams), 'x=%21&x=a%20b&z=a%2Bb')
  const lines = ['POST', '/v1/orders', 'x=%21&x=a%20b&z=a%2Bb', '1790323200', 'nonce-000000000001', 'key', 'order:001', digest('{"a":1}')].join('\n')
  assert.equal(await signature(secret, 'POST', url, '1790323200', 'nonce-000000000001', 'key', 'order:001', '{"a":1}'), mac(lines))
  assert.notEqual(await signature(secret, 'POST', url, '1790323200', 'nonce-000000000001', 'key', 'order:001', '{ "a":1}'), mac(lines))
})

test('Alipay 当面付 validates and renders only the real Alipay QR payload', () => {
  assert.equal(alipayQr('https://qr.alipay.com/bax01234567890'), 'https://qr.alipay.com/bax01234567890')
  assert.equal(alipayQr(null), null)
  for (const payload of ['https://recharge.example.invalid/payments/ord_test', 'https://qr.alipay.com.attacker.test/bax01234567890', 'http://qr.alipay.com/bax01234567890', 'https://user@qr.alipay.com/bax01234567890', 'https://qr.alipay.com/bax01234567890?redirect=evil', 'https://qr.alipay.com/bax01234567890#evil', 'javascript:alert(1)']) assert.throws(() => alipayQr(payload))
})

test('platform 当面付 creates one QR for the same alipay order and removes it after signed payment', async t => {
  const s = setup(t); s.faceToFace(); const buyer = await s.buyer(); const order = await s.create(buyer)
  let response = await s.query(order.id, buyer); assert.equal(response.status, 200, await response.clone().text())
  let current = (await response.json()).order
  assert.equal(current.fulfillment.paymentQr, 'https://qr.alipay.com/bax01234567890')
  assert.equal(current.fulfillment.paymentUrl, null)
  assert.equal(JSON.stringify(current).includes('recharge.example.invalid'), false)
  assert.equal(JSON.stringify(current).includes('public-test-token'), false)
  assert.equal(current.paymentStatus, 'unpaid'); assert.equal(current.fulfillment.canSubmit, false)
  const snapshot = s.db.prepare('SELECT create_body FROM recharge_orders').get()!.create_body
  await s.query(order.id, buyer)
  assert.equal(s.calls.filter(c => c.path === '/v1/orders').length, 1)
  assert.equal(s.calls.filter(c => c.path === '/payments/ord_test/start').length, 1)
  assert.equal(s.db.prepare('SELECT create_body FROM recharge_orders').get()!.create_body, snapshot)
  s.setPayment('paid'); response = await s.query(order.id, buyer); current = (await response.json()).order
  assert.equal(current.status, 'session'); assert.equal(current.fulfillment.canSubmit, true)
  assert.equal(current.fulfillment.paymentQr, null); assert.equal(current.fulfillment.paymentUrl, null)
  assert.equal(s.db.prepare('SELECT payment_qr FROM recharge_orders').get()!.payment_qr, null)
})

test('customer and partner responses keep supplier cashier private, including legacy and unsafe cached QR data', async t => {
  const s = setup(t); s.faceToFace(); const partner = s.partner(); const buyer = await s.buyer()
  const created = await s.call('/api/orders', { plan: 'plus', partner: 'partner-test', expectedPrice: 15900, email: 'buyer@example.test', key: crypto.randomUUID() }, buyer)
  assert.equal(created.status, 201)
  const order = (await created.json()).order
  assert.equal((await s.query(order.id, buyer)).status, 200)
  assert.match(s.db.prepare('SELECT payment_url FROM recharge_orders WHERE order_id=?').get(order.id)!.payment_url as string, /recharge\.example\.invalid/)
  for (const cached of ['https://qr.alipay.com/bax01234567890', 'https://recharge.example.invalid/payments/ord_test?token=private-cashier-token']) {
    s.db.prepare('UPDATE recharge_orders SET payment_qr=? WHERE order_id=?').run(cached, order.id)
    for (const [path, cookie] of [[`/api/orders/${order.id}`, buyer], ['/api/orders', buyer], ['/api/partner/orders', partner]]) {
      const response = await s.call(path, undefined, cookie)
      assert.equal(response.status, 200)
      const raw = await response.text()
      assert.equal(raw.includes('recharge.example.invalid'), false, path)
      assert.equal(raw.includes('public-test-token'), false, path)
      assert.equal(raw.includes('private-cashier-token'), false, path)
    }
  }
})

test('lost cashier start recovers QR through status without recreating the signed order or starting a second payment', async t => {
  const s = setup(t); s.faceToFace(); const buyer = await s.buyer(); const order = await s.create(buyer)
  s.override(path => { if (path === '/payments/ord_test/start') { s.setQr('https://qr.alipay.com/bax01234567890'); throw new TypeError('network lost after creating QR') } })
  assert.equal((await s.query(order.id, buyer)).status, 503)
  assert.equal(s.db.prepare('SELECT remote_id FROM recharge_orders').get()!.remote_id, 'ord_test')
  s.override(undefined)
  const response = await s.query(order.id, buyer); assert.equal(response.status, 200)
  assert.equal((await response.json()).order.fulfillment.paymentQr, 'https://qr.alipay.com/bax01234567890')
  assert.equal(s.calls.filter(c => c.path === '/v1/orders').length, 1)
  assert.equal(s.calls.filter(c => c.path === '/payments/ord_test/start').length, 1)
  assert.equal(s.calls.filter(c => c.path === '/v1/orders/ord_test').length, 1)
})

test('cashier status never authorizes payment and quote amount/expiry/QR mismatches withhold QR', async t => {
  const s = setup(t); s.faceToFace(); const buyer = await s.buyer(); const order = await s.create(buyer)
  s.quote({ status: 'paid' }); let response = await s.query(order.id, buyer)
  assert.equal(response.status, 200); let current = (await response.json()).order
  assert.equal(current.paymentStatus, 'unpaid'); assert.equal(current.status, 'pending'); assert.equal(current.fulfillment.canSubmit, false); assert.equal(current.fulfillment.paymentQr, null)
  for (const patch of [{ amount: '138.00' }, { expires_at: new Date(Date.now() + 60000).toISOString() }, { delivery_mode: 'cdk' }, { qr_code: 'https://recharge.example.invalid/payments/ord_test' }]) {
    s.quote(patch); response = await s.query(order.id, buyer); assert.equal(response.status, 503)
    assert.equal(s.db.prepare('SELECT remote_id FROM recharge_orders').get()!.remote_id, 'ord_test')
    assert.equal(s.db.prepare('SELECT payment_qr FROM recharge_orders').get()!.payment_qr, null)
    assert.equal((await getOrder(s.env, order.id)).payment_status, 'unpaid')
  }
  assert.equal(s.calls.filter(c => c.path === '/payments/ord_test/start').length, 0)
  s.quote({}); response = await s.query(order.id, buyer); current = (await response.json()).order
  assert.equal(current.fulfillment.paymentQr, 'https://qr.alipay.com/bax01234567890')
})

test('a transient QR status failure retains only a previously validated live QR; invalid replacement clears it', async t => {
  const s = setup(t); s.faceToFace(); const buyer = await s.buyer(); const order = await s.create(buyer)
  await s.query(order.id, buyer)
  s.override(path => path === '/payments/ord_test/status' ? new Response('unavailable', { status: 503 }) : undefined)
  assert.equal((await s.query(order.id, buyer)).status, 503)
  assert.equal(s.db.prepare('SELECT payment_qr FROM recharge_orders').get()!.payment_qr, 'https://qr.alipay.com/bax01234567890')
  s.override(undefined); s.quote({ amount: '1.00' })
  assert.equal((await s.query(order.id, buyer)).status, 503)
  assert.equal(s.db.prepare('SELECT payment_qr FROM recharge_orders').get()!.payment_qr, null)
  assert.equal(s.db.prepare('SELECT remote_id FROM recharge_orders').get()!.remote_id, 'ord_test')
})

test('cashier binding, channel and signed currency must match before QR start', async t => {
  const s = setup(t); s.faceToFace(); const buyer = await s.buyer(); const order = await s.create(buyer)
  for (const patch of [{ qr_payload: 'https://recharge.example.invalid/payments/other?token=public-test-token-123456' }, { qr_payload: 'https://recharge.example.invalid/payments/ord_test?token=public-test-token-123456&redirect=evil' }, { payment_channel: 'usdt' }, { currency: 'USD' }]) {
    s.override(path => path === '/v1/orders' || path === '/v1/orders/ord_test' ? Response.json({ data: { ...s.remoteOrder(), merchant_order_no: order.id, ...patch } }) : undefined)
    assert.equal((await s.query(order.id, buyer)).status, 503)
    assert.equal(s.calls.filter(c => c.path.startsWith('/payments/')).length, 0)
    assert.equal((await getOrder(s.env, order.id)).payment_status, 'unpaid')
  }
})

test('closed, expired and paused orders stop exposing or starting QR; legacy cashier remains a link', async t => {
  const s = setup(t); const buyer = await s.buyer(); const order = await s.create(buyer)
  assert.equal((await (await s.query(order.id, buyer)).json()).order.fulfillment.paymentQr, null)
  assert.equal(s.calls.filter(c => c.path.startsWith('/payments/')).length, 0)
  s.faceToFace(); await s.query(order.id, buyer)
  s.db.prepare('UPDATE orders SET refund_hold=1 WHERE id=?').run(order.id)
  let current = (await (await s.query(order.id, buyer)).json()).order
  assert.equal(current.fulfillment.paymentQr, null); assert.equal(current.fulfillment.paymentUrl, null)
  s.db.prepare('UPDATE orders SET refund_hold=0 WHERE id=?').run(order.id)
  s.quote({ expired: true }); current = (await (await s.query(order.id, buyer)).json()).order
  assert.equal(current.fulfillment.paymentQr, null)
  s.quote({}); await s.query(order.id, buyer); s.setPayment('expired')
  current = (await (await s.query(order.id, buyer)).json()).order
  assert.equal(current.status, 'cancelled'); assert.equal(current.fulfillment.paymentQr, null)
  assert.equal(s.db.prepare('SELECT payment_qr FROM recharge_orders').get()!.payment_qr, null)
})

test('unpaid orders expire at creation +15 minutes even if supplier extends its quote; checking payment never restarts QR', async t => {
  let clock = Date.now(); t.mock.method(Date, 'now', () => clock)
  const s = setup(t); s.faceToFace(); s.setExpiry(clock + 3600000)
  const buyer = await s.buyer(); const order = await s.create(buyer)
  let current = (await (await s.query(order.id, buyer)).json()).order
  const deadline = order.createdAt + 900000
  assert.equal(current.fulfillment.expiresAt, deadline)
  assert.ok(current.fulfillment.paymentQr)
  clock = deadline - 1
  current = (await (await s.query(order.id, buyer)).json()).order
  assert.equal(current.status, 'pending'); assert.equal(current.fulfillment.paymentExpired, false)
  const cashierCalls = s.calls.filter(c => c.path.startsWith('/payments/')).length
  clock = deadline
  for (let i = 0; i < 2; i++) {
    current = (await (await s.query(order.id, buyer)).json()).order
    assert.equal(current.status, 'cancelled'); assert.equal(current.fulfillment.paymentExpired, true)
    assert.equal(current.fulfillment.paymentQr, null); assert.equal(current.fulfillment.paymentUrl, null)
    assert.equal(current.fulfillment.canSubmit, false)
  }
  assert.equal(s.calls.filter(c => c.path.startsWith('/payments/')).length, cashierCalls)
  assert.equal(s.calls.filter(c => c.path === '/v1/orders').length, 1)
  assert.equal(s.db.prepare("SELECT COUNT(*) n FROM order_events WHERE action='payment-expired'").get()!.n, 1)
  assert.equal(s.db.prepare('SELECT payment_qr FROM recharge_orders').get()!.payment_qr, null)
  // Supplier-confirmed funds must remain recoverable after our local cutoff.
  s.setPayment('paid'); current = (await (await s.query(order.id, buyer)).json()).order
  assert.equal(current.status, 'session'); assert.equal(current.paymentStatus, 'paid')
  assert.equal(current.fulfillment.paymentExpired, false); assert.equal(current.fulfillment.canSubmit, true)
  assert.equal((await s.submit(order.id, buyer)).status, 200)
  assert.equal(s.calls.filter(c => c.path.startsWith('/payments/')).length, cashierCalls)
})

test('never-attempted expired orders close on read without creating a supplier payment', async t => {
  let clock = Date.now(); t.mock.method(Date, 'now', () => clock)
  const s = setup(t); s.faceToFace(); const buyer = await s.buyer(); const order = await s.create(buyer)
  clock = order.createdAt + 900000
  const list = await s.call('/api/orders', undefined, buyer)
  assert.equal(list.status, 200)
  const current = (await list.json()).orders.find((o: { id: string }) => o.id === order.id)
  assert.equal(current.status, 'cancelled'); assert.equal(current.fulfillment.paymentExpired, true)
  assert.equal((await s.query(order.id, buyer)).status, 200)
  await reconcileRecharge(s.env)
  assert.equal(s.calls.filter(c => c.path === '/v1/orders' || c.path.startsWith('/payments/')).length, 0)
})

test('cashier status crossing the deadline cannot start a QR or leave an order payable', async t => {
  let clock = Date.now(); t.mock.method(Date, 'now', () => clock)
  const s = setup(t); s.faceToFace(); s.setExpiry(clock + 3600000)
  const buyer = await s.buyer(); const order = await s.create(buyer)
  s.override(path => { if (path.endsWith('/status')) clock = order.createdAt + 900000 })
  const response = await s.query(order.id, buyer); assert.equal(response.status, 200)
  const current = (await response.json()).order
  assert.equal(current.status, 'cancelled'); assert.equal(current.fulfillment.paymentExpired, true)
  assert.equal(current.fulfillment.paymentQr, null); assert.equal(current.fulfillment.paymentUrl, null)
  assert.equal(s.calls.filter(c => c.path.endsWith('/start')).length, 0)
})

test('paid orders retain Session submission after the payment window', async t => {
  let clock = Date.now(); t.mock.method(Date, 'now', () => clock)
  const s = setup(t); const buyer = await s.buyer(); const order = await s.create(buyer)
  await s.query(order.id, buyer); s.setPayment('paid'); await s.query(order.id, buyer)
  clock = order.createdAt + 3600000
  const current = (await (await s.query(order.id, buyer)).json()).order
  assert.equal(current.status, 'session'); assert.equal(current.fulfillment.paymentExpired, false)
  assert.equal(current.fulfillment.canSubmit, true)
  assert.equal((await s.submit(order.id, buyer)).status, 200)
  assert.equal(s.db.prepare("SELECT COUNT(*) n FROM order_events WHERE action='payment-expired'").get()!.n, 0)
})

test('lost supplier creation recovers the same idempotent write after expiry through Cron, never starting a QR', async t => {
  let clock = Date.now(); t.mock.method(Date, 'now', () => clock)
  const s = setup(t); s.faceToFace(); s.setExpiry(clock + 3600000)
  const buyer = await s.buyer(); const order = await s.create(buyer)
  s.override(path => path === '/v1/orders' ? new Response('unavailable', { status: 503 }) : undefined)
  assert.equal((await s.query(order.id, buyer)).status, 503)
  clock = order.createdAt + 900000
  await reconcileRecharge(s.env)
  assert.equal((await getOrder(s.env, order.id)).status, 'cancelled')
  s.override(undefined); s.setPayment('paid'); clock += 60000
  await reconcileRecharge(s.env)
  assert.equal((await getOrder(s.env, order.id)).status, 'session')
  const attempts = s.calls.filter(c => c.path === '/v1/orders')
  assert.equal(attempts.length, 3)
  for (const attempt of attempts) {
    assert.equal(attempt.key, attempts[0].key); assert.equal(attempt.body, attempts[0].body)
  }
  assert.equal(s.calls.filter(c => c.path.startsWith('/payments/')).length, 0)
})

test('configured catalog is independent of supplier prices; orders still enforce live price bounds', async t => {
  const s = setup(t)
  const catalog = (await (await s.call('/api/catalog')).json())
  assert.equal(catalog.checkoutEnabled, true); assert.equal(catalog.testingEnabled, false)
  assert.deepEqual(catalog.plans.map((p: { enabled: number }) => p.enabled), [1,0,0,0,0,0,0,0,0,0])
  s.products[0].supply_price = '140.00'
  assert.equal((await (await s.call('/api/catalog')).json()).plans[0].enabled, 1)
  const buyer = await s.buyer()
  assert.equal((await s.call('/api/orders', { email: 'buyer@example.test', plan: 'plus', expectedPrice: 13900, key: crypto.randomUUID() }, buyer)).status, 409)
  s.env.RECHARGE_CHECKOUT_ENABLED = 'false'
  assert.equal((await (await s.call('/api/catalog')).json()).checkoutEnabled, false)
  s.env.ORDER_TESTING_ENABLED = 'true'
  const demo = await s.create(buyer)
  assert.equal(demo.mode, 'demo'); assert.equal(s.db.prepare('SELECT COUNT(*) n FROM recharge_orders').get()!.n, 0)
})

test('platform collection, actual payment verification and auto recharge stay in own order; no credentials or vendor internals reach storage/customer response', async t => {
  const s = setup(t); const buyer = await s.buyer(); const other = await s.buyer(); const order = await s.create(buyer)
  assert.equal(order.mode, 'live'); assert.equal(order.paymentStatus, 'unpaid')
  assert.equal((await s.submit(order.id, buyer)).status, 409)
  assert.equal((await s.query(order.id, other)).status, 404)
  let current = (await (await s.query(order.id, buyer)).json()).order
  assert.equal(current.fulfillment.paymentUrl, null)
  const sent = JSON.parse(s.calls.find(c => c.path === '/v1/orders')!.body)
  assert.equal(sent.delivery_mode, 'auto_recharge'); assert.equal(sent.collection_mode, 'platform_collect'); assert.equal(sent.payment_channel, 'alipay'); assert.equal(sent.sale_amount, '139.00')
  assert.equal('credential' in sent, false)
  s.setPayment('paid'); current = (await (await s.query(order.id, buyer)).json()).order
  assert.equal(current.paymentStatus, 'paid'); assert.equal(current.status, 'session'); assert.equal(current.fulfillment.paymentUrl, null)
  const response = await s.submit(order.id, buyer); assert.equal(response.status, 200, await response.clone().text())
  current = (await response.json()).order; assert.equal(current.status, 'processing')
  s.setTask('running'); assert.equal((await (await s.query(order.id, buyer)).json()).order.status, 'processing')
  s.setTask('succeeded'); current = (await (await s.query(order.id, buyer)).json()).order
  assert.equal(current.status, 'success'); assert.equal(current.fulfillment.accountEmailMasked, 't***@example.test')
  assert.equal(JSON.stringify(current).includes('PRIVATE-TOKEN'), false); assert.equal(JSON.stringify(current).includes('110.00'), false); assert.equal(JSON.stringify(current).includes('ord_test'), false)
  for (const table of ['orders','order_events','recharge_orders','recharge_redemptions','admin_audit']) assert.equal(JSON.stringify(s.db.prepare(`SELECT * FROM ${table}`).all()).includes('PRIVATE-TOKEN'), false)
  assert.equal(s.db.prepare("SELECT COUNT(*) n FROM order_events WHERE action='payment-confirmed'").get()!.n, 1)
  assert.equal((await s.call(`/api/orders/${order.id}/payment`, { action: 'create' }, buyer)).status, 409)
  assert.equal((await s.call(`/api/orders/${order.id}/demo-actions`, { action: 'pay', version: current.version }, buyer)).status, 403)
  s.setTask('running'); s.setPayment('pending')
  const stale = (await (await s.query(order.id, buyer)).json()).order
  assert.equal(stale.status, 'success'); assert.equal(stale.paymentStatus, 'paid')
})

test('production QF order identifiers work for checkout and polling; unsafe identifiers and replacement orders stay rejected', async t => {
  const s = setup(t); const buyer = await s.buyer(); const order = await s.create(buyer)
  let remoteId: unknown = 'QF0000000008'
  s.override(path => {
    if (path === '/v1/orders' || path === '/v1/orders/QF0000000008') return Response.json({ data: { ...s.remoteOrder(), order_id: remoteId, merchant_order_no: order.id } })
  })
  const checkout = await s.query(order.id, buyer)
  assert.equal(checkout.status, 200, await checkout.clone().text())
  assert.equal((await checkout.json()).order.fulfillment.paymentUrl, null)
  assert.equal(s.db.prepare('SELECT remote_id FROM recharge_orders WHERE order_id=?').get(order.id)!.remote_id, 'QF0000000008')
  s.setPayment('paid')
  assert.equal((await (await s.query(order.id, buyer)).json()).order.paymentStatus, 'paid')
  assert.ok(s.calls.some(c => c.path === '/v1/orders/QF0000000008'))
  for (const invalid of [undefined, null, 8, '', 'a'.repeat(101), '../other', 'QF0000000009']) {
    remoteId = invalid
    assert.equal((await s.query(order.id, buyer)).status, 503)
    assert.equal(s.db.prepare('SELECT remote_id FROM recharge_orders WHERE order_id=?').get(order.id)!.remote_id, 'QF0000000008')
    assert.equal((await getOrder(s.env, order.id)).payment_status, 'paid')
  }
})

test('uncertain order creation retries exact stored body and idempotency with a fresh nonce; gate disable cannot abandon an attempted order', async t => {
  const s = setup(t); const buyer = await s.buyer(); const order = await s.create(buyer)
  let lost = true
  s.override((path, raw) => { if (path === '/v1/orders' && lost) { lost = false; s.db.prepare('UPDATE orders SET updated_at=updated_at WHERE id=?').run(JSON.parse(raw).merchant_order_no); throw new Error('connection lost') } })
  assert.equal((await s.query(order.id, buyer)).status, 503)
  s.env.RECHARGE_CHECKOUT_ENABLED = 'false'
  assert.equal((await s.query(order.id, buyer)).status, 200)
  const calls = s.calls.filter(c => c.path === '/v1/orders')
  assert.equal(calls.length, 2); assert.equal(calls[0].body, calls[1].body); assert.equal(calls[0].key, calls[1].key); assert.notEqual(calls[0].nonce, calls[1].nonce)
  assert.equal(s.db.prepare('SELECT COUNT(*) n FROM recharge_orders').get()!.n, 1)
})

test('expired new credentials never create a redemption or call the supplier', async t => {
  const s = setup(t); const buyer = await s.buyer(); const order = await s.create(buyer)
  await s.query(order.id, buyer); s.setPayment('paid'); await s.query(order.id, buyer)
  const token = 'e30.' + Buffer.from('{"exp":1}').toString('base64url') + '.fixture'
  for (const credential of [
    { mode: 'session', session: JSON.stringify({ user: { email: 'buyer@example.test' }, accessToken: token }) },
    { mode: 'session', session: JSON.stringify({ user: { email: 'buyer@example.test' }, accessToken: 'opaque', expires: '2000-01-01T00:00:00Z' }) },
    { mode: 'access_token', access_token: token },
  ]) {
    const response = await s.call(`/api/orders/${order.id}/fulfillment`, { action: 'submit', key: crypto.randomUUID(), customerConfirmedEmail: true, credential }, buyer)
    assert.equal(response.status, 400)
    assert.match((await response.json()).error, /已过期|已超过有效期/)
  }
  assert.equal(s.calls.filter(c => c.path === '/v1/redemptions').length, 0)
  assert.equal(s.db.prepare('SELECT COUNT(*) n FROM recharge_redemptions').get()!.n, 0)
  assert.equal(s.db.prepare('SELECT status FROM orders WHERE id=?').get(order.id)!.status, 'session')
})

test('unknown recharge recovers identical bytes even after credential expiry', async t => {
  const s = setup(t); const buyer = await s.buyer(); const order = await s.create(buyer)
  await s.query(order.id, buyer); s.setPayment('paid'); await s.query(order.id, buyer)
  const now = Date.now(), raw = JSON.stringify({ ...JSON.parse(session), expires: new Date(now + 1000).toISOString() })
  let lost = true
  s.override(path => { if (path === '/v1/redemptions' && lost) { lost = false; throw new Error('lost response') } })
  const key = crypto.randomUUID()
  assert.equal((await s.submit(order.id, buyer, key, raw)).status, 503)
  t.mock.method(Date, 'now', () => now + 2000)
  assert.equal((await s.submit(order.id, buyer, key, raw)).status, 200)
  const calls = s.calls.filter(c => c.path === '/v1/redemptions')
  assert.equal(calls.length, 2); assert.equal(calls[0].key, calls[1].key); assert.equal(calls[0].body, calls[1].body)
})

test('unknown recharge keeps original key and body digest; other credentials/new attempt cannot duplicate charge', async t => {
  const s = setup(t); const buyer = await s.buyer(); const order = await s.create(buyer); await s.query(order.id, buyer); s.setPayment('paid'); await s.query(order.id, buyer)
  let lost = true
  s.override(path => { if (path === '/v1/redemptions' && lost) { lost = false; throw new Error('lost response') } })
  const key = crypto.randomUUID()
  assert.equal((await s.submit(order.id, buyer, key)).status, 503)
  const current = (await (await s.call('/api/orders/' + order.id, undefined, buyer)).json()).order
  assert.equal(current.status, 'processing'); assert.equal(current.fulfillment.retryKey, key)
  assert.equal((await s.submit(order.id, buyer)).status, 409)
  assert.equal((await s.submit(order.id, buyer, key, session.replace('PRIVATE-TOKEN', 'CHANGED-TOKEN'))).status, 409)
  assert.equal((await s.submit(order.id, buyer, key)).status, 200)
  const calls = s.calls.filter(c => c.path === '/v1/redemptions'); assert.equal(calls.length, 2); assert.equal(calls[0].key, calls[1].key); assert.equal(calls[0].body, calls[1].body)
  assert.equal((await s.submit(order.id, buyer)).status, 409)
})

test('an unknown write beyond the guaranteed deduplication window is blocked for manual reconciliation', async t => {
  const s = setup(t); const buyer = await s.buyer(); const order = await s.create(buyer); await s.query(order.id, buyer); s.setPayment('paid'); await s.query(order.id, buyer)
  s.override(path => { if (path === '/v1/redemptions') throw new Error('lost response') })
  const key = crypto.randomUUID(); assert.equal((await s.submit(order.id, buyer, key)).status, 503)
  s.db.prepare('UPDATE recharge_redemptions SET created_at=?').run(Date.now() - 7 * 86400000)
  const current = (await (await s.call('/api/orders/' + order.id, undefined, buyer)).json()).order
  assert.equal(current.fulfillment.canSubmit, false); assert.equal((await s.submit(order.id, buyer, key)).status, 409)
  assert.equal(s.calls.filter(c => c.path === '/v1/redemptions').length, 1)
})

test('definitive credential rejection permits a new authorized attempt without calling order creation again', async t => {
  const s = setup(t); const buyer = await s.buyer(); const order = await s.create(buyer); await s.query(order.id, buyer); s.setPayment('paid'); await s.query(order.id, buyer)
  s.override(path => path === '/v1/redemptions' ? Response.json({ error: { code: 'invalid_credential', retryable: false, message: 'secret must not be exposed' } }, { status: 400 }) : undefined)
  assert.equal((await s.submit(order.id, buyer)).status, 503)
  const current = (await (await s.call('/api/orders/' + order.id, undefined, buyer)).json()).order
  assert.equal(current.status, 'failed'); assert.equal(current.fulfillment.canSubmit, true)
  s.override(undefined); assert.equal((await s.submit(order.id, buyer)).status, 200)
  assert.equal(s.calls.filter(c => c.path === '/v1/orders').length, 1)
})

test('redemption fallback permits a new attempt without another order/payment, and refunds freeze submission', async t => {
  const s = setup(t); const buyer = await s.buyer(); const order = await s.create(buyer); await s.query(order.id, buyer); s.setPayment('paid'); await s.query(order.id, buyer); await s.submit(order.id, buyer)
  assert.equal((await s.submit(order.id, buyer)).status, 409)
  s.setTask('failed', true); let current = (await (await s.query(order.id, buyer)).json()).order
  assert.equal(current.fulfillment.canSubmit, true)
  s.setTask('queued'); assert.equal((await s.submit(order.id, buyer)).status, 200)
  assert.equal(s.db.prepare('SELECT COUNT(*) n FROM recharge_redemptions').get()!.n, 2)
  current = (await (await s.call('/api/orders/' + order.id, undefined, buyer)).json()).order
  assert.equal((await s.call(`/api/orders/${order.id}/refunds`, { key: crypto.randomUUID(), version: current.version, reason: '取消服务' }, buyer)).status, 201)
  assert.equal((await s.submit(order.id, buyer)).status, 409)
})

test('a failed recharge without explicit fallback cannot submit another attempt', async t => {
  const s = setup(t); const buyer = await s.buyer(); const order = await s.create(buyer); await s.query(order.id, buyer); s.setPayment('paid'); await s.query(order.id, buyer); await s.submit(order.id, buyer)
  s.setTask('failed', false)
  const current = (await (await s.query(order.id, buyer)).json()).order
  assert.equal(current.status, 'failed'); assert.equal(current.fulfillment.canSubmit, false); assert.equal((await s.submit(order.id, buyer)).status, 409)
})

test('concurrent submit obtains one lease and one external request; audit failure rolls back attempt before credential forwarding', async t => {
  const s = setup(t); const buyer = await s.buyer(); const order = await s.create(buyer); await s.query(order.id, buyer); s.setPayment('paid'); await s.query(order.id, buyer)
  s.db.exec("CREATE TRIGGER fail_recharge BEFORE INSERT ON order_events WHEN NEW.action='recharge-submit' BEGIN SELECT RAISE(ABORT,'audit failure'); END")
  assert.equal((await s.submit(order.id, buyer)).status, 503)
  assert.equal(s.db.prepare('SELECT COUNT(*) n FROM recharge_redemptions').get()!.n, 0); assert.equal(s.calls.filter(c => c.path === '/v1/redemptions').length, 0)
  s.db.exec('DROP TRIGGER fail_recharge')
  const key = crypto.randomUUID(); const responses = await Promise.all([s.submit(order.id, buyer, key), s.submit(order.id, buyer, key)])
  assert.deepEqual(responses.map(r => r.status).sort(), [200,409]); assert.equal(s.calls.filter(c => c.path === '/v1/redemptions').length, 1)
})

test('webhooks verify raw HMAC, age, header/body identity and deduplicate; even signed success only schedules GET verification', async t => {
  const s = setup(t); const buyer = await s.buyer(); const order = await s.create(buyer)
  const timestamp = String(Math.floor(Date.now() / 1000)); const raw = JSON.stringify({ event_id: 'evt_test', type: 'order.paid', data: { order_id: 'ord_test', payment_status: 'paid', malicious_secret: 'PRIVATE-TOKEN' } })
  const notice = (text = raw, stamp = timestamp, sig = mac(stamp + '.' + text, webhookSecret)) => worker.fetch(new Request('https://shop.example.invalid/api/recharge/webhook', { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-Quefa-Event': 'order.paid', 'X-Quefa-Event-Id': 'evt_test', 'X-Quefa-Delivery': 'dlv_test', 'X-Quefa-Timestamp': stamp, 'X-Quefa-Signature': `t=${stamp},v1=${sig}` }, body: text }), s.env)
  assert.equal((await notice()).status, 200); assert.equal((await notice()).status, 200)
  assert.equal(s.db.prepare('SELECT COUNT(*) n FROM recharge_webhooks').get()!.n, 1); assert.equal((await getOrder(s.env, order.id)).payment_status, 'unpaid')
  assert.equal(JSON.stringify(s.db.prepare('SELECT * FROM recharge_webhooks').all()).includes('PRIVATE-TOKEN'), false)
  assert.equal((await notice(raw + ' ', timestamp, mac(timestamp + '.' + raw, webhookSecret))).status, 400)
  assert.equal((await notice(raw, String(Number(timestamp) - 301))).status, 400)
  assert.equal((await notice(raw.replace('evt_test','evt_other'))).status, 400)
  await reconcileRecharge(s.env)
  assert.equal((await getOrder(s.env, order.id)).payment_status, 'unpaid')
  s.setPayment('paid'); await reconcileRecharge(s.env); assert.equal((await getOrder(s.env, order.id)).payment_status, 'paid')
})

test('price/order/tenant mismatches and redirects fail closed without leaking vendor replies or forwarding credentials', async t => {
  const s = setup(t); const buyer = await s.buyer(); const order = await s.create(buyer); await s.query(order.id, buyer)
  s.setPayment('paid')
  for (const patch of [{ sale_amount: '1.00' }, { payment_scope: 'procurement' }, { merchant_order_no: 'other' }, { order_id: 'ord_other' }]) {
    s.override(path => path === '/v1/orders/ord_test' ? Response.json({ data: { ...s.remoteOrder(), ...patch } }) : undefined)
    assert.equal((await s.query(order.id, buyer)).status, 503); assert.equal((await getOrder(s.env, order.id)).payment_status, 'unpaid')
  }
  s.override(undefined); s.env.RECHARGE_PARTNER_ID = 'other'
  assert.equal((await s.query(order.id, buyer)).status, 503)
  s.env.RECHARGE_PARTNER_ID = 'pt_test'
  s.override(() => new Response(null, { status: 302, headers: { Location: 'https://evil.test/' } }))
  await assert.rejects(supplierRequest(s.env, '/products'))
  assert.equal((await s.call(`/api/orders/${order.id}/fulfillment`, { action: 'query' }, buyer, 'https://evil.test')).status, 403)
})

test('platform refund is asynchronous, uses fixed case id, and only verified final refund permits local bookkeeping once', async t => {
  const s = setup(t); const buyer = await s.buyer(); const order = await s.create(buyer); await s.query(order.id, buyer); s.setPayment('paid'); let current = (await (await s.query(order.id, buyer)).json()).order
  assert.equal((await s.call(`/api/orders/${order.id}/refunds`, { key: crypto.randomUUID(), version: current.version, reason: '退款' }, buyer)).status, 201)
  const refund = s.db.prepare('SELECT * FROM refund_cases').get()!; const admin = await s.login()
  assert.equal((await s.call('/api/admin/refunds/' + refund.id, { action: 'approve', version: 0, amountCents: 13900, note: '同意退款' }, admin)).status, 200)
  assert.equal((await (await s.call('/api/admin/refunds/' + refund.id, undefined, admin)).json()).paymentProvider, 'recharge')
  const execute = () => s.call('/api/admin/refunds/' + refund.id, { action: 'execute-refund', version: 1, note: '核对退款结果' }, admin)
  assert.equal((await execute()).status, 409)
  assert.equal((await getOrder(s.env, order.id)).refunded_cents, 0); assert.equal((await getOrder(s.env, order.id)).refund_hold, 1)
  s.setRefund('succeeded'); s.setPayment('refunded')
  s.db.exec("CREATE TRIGGER refund_local_failure BEFORE INSERT ON refund_events WHEN NEW.status='refunded' BEGIN SELECT RAISE(ABORT,'audit failure'); END")
  assert.equal((await execute()).status, 503)
  assert.equal((await getOrder(s.env, order.id)).refunded_cents, 0); assert.equal((await getOrder(s.env, order.id)).refund_hold, 1)
  s.db.exec('DROP TRIGGER refund_local_failure')
  assert.equal((await execute()).status, 200)
  assert.equal((await getOrder(s.env, order.id)).refunded_cents, 13900); assert.equal((await getOrder(s.env, order.id)).refund_hold, 0)
  assert.equal((await execute()).status, 409); assert.equal(s.calls.filter(c => c.path.endsWith('/refunds')).length, 1)
})

test('operator reconciles an external full refund with an unknown local request atomically without issuing another refund', async t => {
  const s = setup(t); const buyer = await s.buyer(); const order = await s.create(buyer)
  await s.query(order.id, buyer); s.setPayment('paid'); await s.query(order.id, buyer)
  const admin = await s.login(); const refund = await approveRefund(s, order.id, buyer, admin, 13900)
  s.db.prepare('INSERT INTO recharge_refunds(case_id,order_id,create_body) VALUES (?,?,?)').run(refund, order.id, '{}')
  s.override(path => path === '/v1/orders/ord_test' ? Response.json({ data: { ...s.remoteOrder(), payment_status: 'refunded', refunded_amount: '139.00', merchant_margin: '-110.00' } }) : undefined)
  assert.equal((await s.call(`/api/admin/orders/${order.id}/platform-status`, {}, admin)).status, 200)
  const execute = (version = 1) => s.call('/api/admin/refunds/' + refund, { action: 'reconcile-platform-refund', version, note: '核对上游全额退款' }, admin)
  assert.equal((await execute(0)).status, 409)
  const detail = await (await s.call('/api/admin/refunds/' + refund, undefined, admin)).json()
  assert.equal(detail.platformStatus.refundedCents, 13900)
  assert.equal((await s.call('/api/admin/refunds/' + refund, { action: 'execute-refund', version: 1, note: '执行退款' }, admin)).status, 409)
  s.db.exec("CREATE TRIGGER external_refund_failure BEFORE INSERT ON refund_events WHEN NEW.status='refunded' BEGIN SELECT RAISE(ABORT,'audit failure'); END")
  assert.equal((await execute()).status, 503)
  assert.equal((await getOrder(s.env, order.id)).refunded_cents, 0)
  assert.equal(s.db.prepare('SELECT status FROM refund_cases').get()!.status, 'approved')
  s.db.exec('DROP TRIGGER external_refund_failure')
  assert.equal((await execute()).status, 200)
  const current = await getOrder(s.env, order.id)
  assert.equal(current.refunded_cents, 13900); assert.equal(current.refund_hold, 0)
  assert.equal(s.db.prepare('SELECT financial_hold FROM recharge_orders').get()!.financial_hold, 0)
  assert.equal(s.db.prepare('SELECT reference FROM refund_cases').get()!.reference, 'recharge-order:ord_test:13900')
  assert.equal((await execute()).status, 409)
  assert.equal((await reconcileRechargeRefunds(s.env)).checked, 0)
  assert.equal(s.db.prepare("SELECT COUNT(*) n FROM refund_events WHERE status='refunded'").get()!.n, 1)
  assert.equal(s.calls.some(c => c.path.includes('/refunds')), false)
})

test('external refund reconciliation requires fresh matching identity and exact totals and retains holds on failures', async t => {
  const s = setup(t); const buyer = await s.buyer(); const order = await s.create(buyer)
  await s.query(order.id, buyer); s.setPayment('paid'); await s.query(order.id, buyer)
  const admin = await s.login(); const refund = await approveRefund(s, order.id, buyer, admin, 13900)
  const execute = (cookie = admin) => s.call('/api/admin/refunds/' + refund, { action: 'reconcile-platform-refund', version: 1, note: '核对退款' }, cookie)
  assert.equal((await execute(buyer)).status, 401)
  for (const patch of [
    { payment_status: 'paid', refunded_amount: '0.00' },
    { payment_status: 'partially_refunded', refunded_amount: '10.00' },
    { payment_status: 'refunded', refunded_amount: '139.00', merchant_order_no: 'another-order' },
  ]) {
    s.override(path => path === '/v1/orders/ord_test' ? Response.json({ data: { ...s.remoteOrder(), ...patch } }) : undefined)
    assert.notEqual((await execute()).status, 200)
    assert.equal((await getOrder(s.env, order.id)).refunded_cents, 0)
    assert.equal((await getOrder(s.env, order.id)).refund_hold, 1)
  }
  s.override(() => new Response('unavailable', { status: 503 }))
  assert.equal((await execute()).status, 503)
  assert.equal(s.db.prepare('SELECT status FROM refund_cases').get()!.status, 'approved')
  assert.equal(s.calls.some(c => c.path.includes('/refunds')), false)
})

test('refund commit guard uses this order even when another order has the expected total', async t => {
  const s = setup(t); const buyer = await s.buyer(); const order = await s.create(buyer)
  await s.query(order.id, buyer); s.setPayment('paid'); await s.query(order.id, buyer)
  const admin = await s.login(); const refund = await approveRefund(s, order.id, buyer, admin, 13900)
  const other = await s.create(buyer)
  s.db.prepare("UPDATE recharge_orders SET payment_status='refunded',refunded_cents=13900 WHERE order_id=?").run(other.id)
  s.override(path => path === '/v1/orders/ord_test' ? Response.json({ data: { ...s.remoteOrder(), merchant_order_no: order.id, payment_status: 'refunded', refunded_amount: '139.00', merchant_margin: '0.00' } }) : undefined)
  const prepare = s.env.INTEREST_DB.prepare.bind(s.env.INTEREST_DB)
  s.env.INTEREST_DB.prepare = sql => {
    if (sql.startsWith("UPDATE refund_cases SET status='refunded'")) s.db.prepare("UPDATE recharge_orders SET refunded_cents=13800,payment_status='partially_refunded' WHERE order_id=?").run(order.id)
    return prepare(sql)
  }
  assert.equal((await s.call('/api/admin/refunds/' + refund, { action: 'reconcile-platform-refund', version: 1, note: '核对退款' }, admin)).status, 409)
  assert.equal((await getOrder(s.env, order.id)).refunded_cents, 0)
  assert.equal(s.db.prepare('SELECT status FROM refund_cases').get()!.status, 'approved')
  assert.equal(s.calls.some(c => c.path.includes('/refunds')), false)
})

test('external partial refund reconciles paid partner commission once and blocks unresolved payouts', async t => {
  const s = setup(t); const partner = s.partner(); const buyer = await s.buyer(); const order = await partnerOrder(s, buyer); const admin = await s.login()
  assert.equal((await s.call('/api/partner/payouts', { key: crypto.randomUUID(), recipient: '支付宝测试账户 / 测试人' }, partner)).status, 201)
  const payout = s.db.prepare('SELECT * FROM partner_payouts').get()!
  assert.equal((await s.call('/api/admin/partner-payouts/' + payout.id, { action: 'approve', version: 0, note: '审核提现' }, admin)).status, 200)
  const refund = await approveRefund(s, order.id, buyer, admin, 1000)
  s.override(path => path === '/v1/orders/ord_test' ? Response.json({ data: { ...s.remoteOrder(), payment_status: 'partially_refunded', refunded_amount: '10.00', merchant_margin: '39.00' } }) : undefined)
  const execute = () => s.call('/api/admin/refunds/' + refund, { action: 'reconcile-platform-refund', version: 1, note: '核对部分退款' }, admin)
  assert.equal((await execute()).status, 409)
  assert.equal((await s.call('/api/admin/partner-payouts/' + payout.id, { action: 'paid', version: 1, note: '核对转账', reference: 'MOCK-TRANSFER-ONLY' }, admin)).status, 200)
  assert.equal((await execute()).status, 200)
  assert.equal((await getOrder(s.env, order.id)).refunded_cents, 1000)
  assert.equal(s.db.prepare('SELECT amount_cents FROM partner_debits').get()!.amount_cents, 1000)
  assert.equal((await execute()).status, 409)
  assert.equal(s.db.prepare('SELECT COUNT(*) n FROM partner_debits').get()!.n, 1)
  assert.equal(s.calls.some(c => c.path.includes('/refunds')), false)
})

test('background refund reconciliation only follows initiated refunds and recovers an atomic bookkeeping failure without a second admin action', async t => {
  const s = setup(t); const buyer = await s.buyer(); const order = await s.create(buyer); await s.query(order.id,buyer); s.setPayment('paid'); await s.query(order.id,buyer)
  const admin = await s.login(); const refund = await approveRefund(s,order.id,buyer,admin,13900)
  assert.deepEqual(await reconcileRechargeRefunds(s.env),{ checked: 0, completed: 0, pending: 0 })
  assert.equal(s.calls.filter(c => c.path.endsWith('/refunds')).length,0)
  assert.equal((await s.call('/api/admin/refunds/'+refund,{ action: 'execute-refund', version: 1, note: '执行退款' },admin)).status,409)
  s.setRefund('processing'); assert.equal((await reconcileRechargeRefunds(s.env)).pending,1)
  assert.equal((await getOrder(s.env,order.id)).refund_hold,1)
  s.setRefund('succeeded'); s.setPayment('refunded')
  s.db.exec("CREATE TRIGGER background_refund_failure BEFORE INSERT ON refund_events WHEN NEW.status='refunded' BEGIN SELECT RAISE(ABORT,'audit failure'); END")
  assert.equal((await reconcileRechargeRefunds(s.env)).pending,1)
  assert.equal((await getOrder(s.env,order.id)).refunded_cents,0)
  assert.equal(s.db.prepare('SELECT financial_hold FROM recharge_orders').get()!.financial_hold,1)
  s.db.exec('DROP TRIGGER background_refund_failure')
  await worker.scheduled({},s.env)
  assert.equal((await getOrder(s.env,order.id)).refunded_cents,13900)
  assert.equal((await getOrder(s.env,order.id)).refund_hold,0)
  assert.equal(s.db.prepare('SELECT financial_hold FROM recharge_orders').get()!.financial_hold,0)
  assert.equal((await reconcileRechargeRefunds(s.env)).checked,0)
  assert.equal(s.db.prepare("SELECT COUNT(*) n FROM refund_events WHERE status='refunded'").get()!.n,1)
  assert.equal(s.calls.filter(c => c.path.endsWith('/refunds')).length,1)
})

test('confirmed supplier rejection releases this refund hold and preserves a completed task observed while frozen', async t => {
  const s = setup(t); const buyer = await s.buyer(); const order = await s.create(buyer); await s.query(order.id,buyer); s.setPayment('paid'); await s.query(order.id,buyer); await s.submit(order.id,buyer)
  const admin = await s.login(); const refund = await approveRefund(s,order.id,buyer,admin,13900)
  assert.equal((await s.call('/api/admin/refunds/'+refund,{ action: 'execute-refund', version: 1, note: '执行退款' },admin)).status,409)
  s.setTask('succeeded'); await s.query(order.id,buyer)
  assert.equal((await getOrder(s.env,order.id)).status,'processing')
  s.setRefund('rejected'); assert.equal((await reconcileRechargeRefunds(s.env)).completed,1)
  assert.equal(s.db.prepare('SELECT status FROM refund_cases').get()!.status,'rejected')
  assert.equal((await getOrder(s.env,order.id)).refund_hold,0)
  assert.equal((await getOrder(s.env,order.id)).refunded_cents,0)
  const current = (await (await s.query(order.id,buyer)).json()).order
  assert.equal(current.status,'success'); assert.equal(current.refundHold,false)
  assert.equal(s.db.prepare('SELECT COUNT(*) n FROM partner_debits').get()!.n,0)
  assert.equal(s.calls.filter(c => c.path === '/v1/redemptions').length,1)
})

test('unmatched supplier refund freezes customer operations and its pending payout while admin income uses confirmed upstream refund', async t => {
  const s = setup(t); const partner = s.partner(); const buyer = await s.buyer(); const order = await partnerOrder(s,buyer); const admin = await s.login()
  assert.equal((await s.call('/api/partner/payouts',{ key: crypto.randomUUID(), recipient: '支付宝测试账户 / 测试人' },partner)).status,201)
  const payout = s.db.prepare('SELECT id FROM partner_payouts').get()!.id
  s.override(path => path === '/v1/orders/ord_test' ? Response.json({ data: { ...s.remoteOrder(), payment_status: 'partially_refunded', refunded_amount: '10.00', merchant_margin: '39.00' } }) : undefined)
  const current = (await (await s.query(order.id,buyer)).json()).order
  assert.equal(current.refundHold,true); assert.equal(current.fulfillment.canSubmit,false)
  assert.equal((await getOrder(s.env,order.id)).refunded_cents,0)
  assert.equal((await s.call(`/api/orders/${order.id}/refunds`,{ key: crypto.randomUUID(), version: current.version, reason: '另申请退款' },buyer)).status,409)
  assert.equal((await s.submit(order.id,buyer)).status,409)
  assert.equal((await s.call('/api/admin/partner-payouts/'+payout,{ action: 'approve', version: 0, note: '审核提现' },admin)).status,409)
  const dashboard = (await (await s.call('/api/partner/dashboard',undefined,partner)).json())
  assert.equal(dashboard.stats.withdrawalHeld,false)
  assert.equal(dashboard.stats.available,0)
  assert.ok(dashboard.totals.pending > 0)
  const overview = (await (await s.call('/api/admin/overview',undefined,admin)).json())
  assert.equal(overview.stats.paid_amount,14900); assert.equal(overview.stats.refund_amount,1000); assert.equal(overview.recent[0].refundHold,true)
  const orders = (await (await s.call('/api/partner/orders',undefined,partner)).json()).rows
  assert.equal(orders[0].refund_hold,1); assert.equal(orders[0].profit_cents,3900)
  assert.equal(s.db.prepare('SELECT COUNT(*) n FROM refund_cases').get()!.n,0)
})

test('rejecting this refund cannot clear the separate hold caused by an unmatched upstream refund', async t => {
  const s = setup(t); const buyer = await s.buyer(); const order = await s.create(buyer); await s.query(order.id,buyer); s.setPayment('paid'); await s.query(order.id,buyer)
  const admin = await s.login(); const refund = await approveRefund(s,order.id,buyer,admin,13900)
  assert.equal((await s.call('/api/admin/refunds/'+refund,{ action: 'execute-refund', version: 1, note: '发起退款' },admin)).status,409)
  s.setRefund('rejected')
  s.override(path => path === '/v1/orders/ord_test' ? Response.json({ data: { ...s.remoteOrder(), payment_status: 'partially_refunded', refunded_amount: '10.00', merchant_margin: '19.00' } }) : undefined)
  assert.equal((await reconcileRechargeRefunds(s.env)).completed,1)
  assert.equal((await getOrder(s.env,order.id)).refund_hold,0)
  assert.equal(s.db.prepare('SELECT financial_hold FROM recharge_orders').get()!.financial_hold,1)
  const current = (await (await s.call('/api/orders/'+order.id,undefined,buyer)).json()).order
  assert.equal(current.refundHold,true); assert.equal(current.fulfillment.canSubmit,false)
  assert.equal((await s.submit(order.id,buyer)).status,409)
})

test('partial Recharge refund caps profit at actual supplier margin and claws back the paid commission exactly once', async t => {
  const s = setup(t); const partner = s.partner(); const buyer = await s.buyer(); const order = await partnerOrder(s,buyer); const admin = await s.login()
  assert.equal((await (await s.call('/api/partner/dashboard',undefined,partner)).json()).stats.available,4900)
  assert.equal((await s.call('/api/partner/payouts',{ key: crypto.randomUUID(), recipient: '支付宝测试账户 / 测试人' },partner)).status,201)
  const payout = s.db.prepare('SELECT * FROM partner_payouts').get()!
  assert.equal(payout.amount_cents,4900)
  assert.equal((await s.call('/api/admin/partner-payouts/'+payout.id,{ action: 'approve', version: 0, note: '审核通过' },admin)).status,200)
  assert.equal((await s.call('/api/admin/partner-payouts/'+payout.id,{ action: 'paid', version: 1, note: '已核对转账', reference: 'MOCK-TRANSFER-ONLY' },admin)).status,200)
  const refund = await approveRefund(s,order.id,buyer,admin,1000)
  assert.equal((await s.call('/api/admin/refunds/'+refund,{ action: 'execute-refund', version: 1, note: '执行部分退款' },admin)).status,409)
  assert.equal((await (await s.call('/api/partner/dashboard',undefined,partner)).json()).stats.withdrawalHeld,true)
  s.setRefund('succeeded'); s.setPayment('partially_refunded')
  assert.equal((await reconcileRechargeRefunds(s.env)).completed,1)
  const current = await getOrder(s.env,order.id)
  assert.equal(current.status,'success'); assert.equal(current.refunded_cents,1000); assert.equal(current.refund_hold,0)
  assert.equal(s.db.prepare('SELECT amount_cents FROM partner_debits').get()!.amount_cents,1000)
  const dashboard = (await (await s.call('/api/partner/dashboard',undefined,partner)).json())
  assert.equal(dashboard.stats.debt,1000); assert.equal(dashboard.stats.withdrawalHeld,false)
  assert.equal((await (await s.call('/api/partner/orders',undefined,partner)).json()).rows[0].profit_cents,3900)
  await reconcileRechargeRefunds(s.env)
  assert.equal(s.db.prepare('SELECT COUNT(*) n FROM partner_debits').get()!.n,1)
})

test('a payout reserved at an old supplier margin cannot be approved after the confirmed margin changes', async t => {
  const s = setup(t); const partner = s.partner(); const buyer = await s.buyer(); const order = await partnerOrder(s,buyer); const admin = await s.login()
  assert.equal((await s.call('/api/partner/payouts',{ key: crypto.randomUUID(), recipient: '支付宝测试账户 / 测试人' },partner)).status,201)
  const payout = s.db.prepare('SELECT id FROM partner_payouts').get()!.id
  s.override(path => path === '/v1/orders/ord_test' ? Response.json({ data: { ...s.remoteOrder(), supply_amount: '119.00', merchant_margin: '40.00' } }) : undefined)
  await s.query(order.id,buyer)
  assert.equal((await s.call('/api/admin/partner-payouts/'+payout,{ action: 'approve', version: 0, note: '审核提现' },admin)).status,409)
  assert.equal(s.db.prepare('SELECT reviewed_at FROM partner_payouts').get()!.reviewed_at,null)
  assert.equal((await s.call('/api/admin/partner-payouts/'+payout,{ action: 'rejected', version: 0, note: '重新核算收益' },admin)).status,200)
  assert.equal((await (await s.call('/api/partner/dashboard',undefined,partner)).json()).stats.available,4000)
})

test('partner configured price remains visible while unsafe upstream supply rejects order creation', async t => {
  const s = setup(t); s.partner(); const buyer = await s.buyer()
  s.db.prepare("UPDATE partner_prices SET cost_cents=10000 WHERE partner_id='partner-test'").run()
  assert.equal((await (await s.call('/api/catalog?partner=partner-test')).json()).plans[0].enabled,1)
  assert.equal((await s.call('/api/orders',{ partner: 'partner-test', plan: 'plus', expectedPrice: 15900, email: 'buyer@example.test', key: crypto.randomUUID() },buyer)).status,409)
  assert.equal(s.db.prepare('SELECT COUNT(*) n FROM orders').get()!.n,0)
})

test('payout submission and approval verify current supplier finances even when no webhook or customer refresh ran', async t => {
  const s = setup(t); const partner = s.partner(); const buyer = await s.buyer(); await partnerOrder(s,buyer); const admin = await s.login()
  s.override(path => { if (path === '/v1/orders/ord_test') throw new Error('provider outage') })
  assert.equal((await s.call('/api/partner/payouts',{ key: crypto.randomUUID(), recipient: '支付宝测试账户 / 测试人' },partner)).status,503)
  assert.equal(s.db.prepare('SELECT COUNT(*) n FROM partner_payouts').get()!.n,0)
  s.override(undefined)
  assert.equal((await s.call('/api/partner/payouts',{ key: crypto.randomUUID(), recipient: '支付宝测试账户 / 测试人' },partner)).status,201)
  const payout = s.db.prepare('SELECT id FROM partner_payouts').get()!.id
  s.override(path => path === '/v1/orders/ord_test' ? Response.json({ data: { ...s.remoteOrder(), payment_status: 'partially_refunded', refunded_amount: '10.00', merchant_margin: '39.00' } }) : undefined)
  assert.equal((await s.call('/api/admin/partner-payouts/'+payout,{ action: 'approve', version: 0, note: '审核提现' },admin)).status,409)
  assert.equal(s.db.prepare('SELECT reviewed_at FROM partner_payouts').get()!.reviewed_at,null)
  assert.equal(s.db.prepare('SELECT financial_hold FROM recharge_orders').get()!.financial_hold,1)
  assert.equal((await s.call('/api/admin/partner-payouts/'+payout,{ action: 'rejected', version: 0, note: '收益核对后重新申请' },admin)).status,200)
  assert.equal((await s.call('/api/partner/payouts',{ key: crypto.randomUUID(), recipient: '支付宝测试账户 / 测试人' },partner)).status,409)
  assert.equal(s.db.prepare('SELECT COUNT(*) n FROM partner_payouts').get()!.n,1)
})

test('a webhook arriving during terminal order reconciliation remains due and is verified by the next reconciliation', async t => {
  const s = setup(t); const buyer = await s.buyer(); const order = await s.create(buyer); await s.query(order.id,buyer); s.setPayment('paid'); await s.query(order.id,buyer); await s.submit(order.id,buyer); s.setTask('succeeded'); await s.query(order.id,buyer)
  let first = true
  s.override(async path => {
    if (path !== '/v1/orders/ord_test') return undefined
    if (first) { first = false; const old = s.remoteOrder(); assert.equal((await s.notice('evt_during_query')).status,200); return Response.json({ data: old }) }
    return Response.json({ data: { ...s.remoteOrder(), payment_status: 'partially_refunded', refunded_amount: '10.00', merchant_margin: '19.00' } })
  })
  assert.equal((await s.query(order.id,buyer)).status,200)
  let stored = s.db.prepare('SELECT * FROM recharge_orders').get()!
  assert.equal(stored.notify_version,1); assert.equal(stored.checked_notify_version,0); assert.equal(stored.updated_at,0)
  assert.deepEqual(await reconcileRecharge(s.env),{ checked: 1, failed: 0 })
  stored = s.db.prepare('SELECT * FROM recharge_orders').get()!
  assert.equal(stored.checked_notify_version,1); assert.equal(stored.refunded_cents,1000); assert.equal(stored.financial_hold,1)
})

test('recently closed unpaid orders recover a verified late payment without a webhook', async t => {
  const s = setup(t); const buyer = await s.buyer(); const order = await s.create(buyer); await s.query(order.id,buyer); s.setPayment('expired'); await s.query(order.id,buyer)
  assert.equal((await getOrder(s.env,order.id)).status,'cancelled')
  s.setPayment('paid'); assert.equal((await reconcileRecharge(s.env)).checked,1)
  assert.equal((await getOrder(s.env,order.id)).status,'session'); assert.equal((await getOrder(s.env,order.id)).payment_status,'paid')
  assert.equal(s.db.prepare("SELECT COUNT(*) n FROM order_events WHERE action='payment-confirmed'").get()!.n,1)
})

test('completed orders remain in periodic financial reconciliation when an external refund webhook is missing', async t => {
  const s = setup(t); const buyer = await s.buyer(); const order = await s.create(buyer); await s.query(order.id,buyer); s.setPayment('paid'); await s.query(order.id,buyer); await s.submit(order.id,buyer); s.setTask('succeeded'); await s.query(order.id,buyer)
  s.override(path => path === '/v1/orders/ord_test' ? Response.json({ data: { ...s.remoteOrder(), payment_status: 'partially_refunded', refunded_amount: '10.00', merchant_margin: '19.00' } }) : undefined)
  assert.equal(s.db.prepare('SELECT COUNT(*) n FROM recharge_webhooks').get()!.n,0)
  assert.equal((await reconcileRecharge(s.env)).checked,1)
  assert.equal(s.db.prepare('SELECT financial_hold FROM recharge_orders').get()!.financial_hold,1)
  assert.equal((await getOrder(s.env,order.id)).status,'success')
  assert.equal((await getOrder(s.env,order.id)).refunded_cents,0)
})

test('inconsistent supplier refund totals cannot unlock payment, local refunds, or profits', async t => {
  const s = setup(t); const buyer = await s.buyer(); const order = await s.create(buyer); await s.query(order.id,buyer)
  for (const patch of [{ payment_status: 'refunded', refunded_amount: '0.00', merchant_margin: '0.00' },{ payment_status: 'partially_refunded', refunded_amount: '0.00' },{ payment_status: 'paid', refunded_amount: '1.00' },{ payment_status: 'refunded', refunded_amount: '139.00', merchant_margin: '29.00' }]) {
    s.override(path => path === '/v1/orders/ord_test' ? Response.json({ data: { ...s.remoteOrder(), ...patch } }) : undefined)
    assert.equal((await s.query(order.id,buyer)).status,503)
    assert.equal((await getOrder(s.env,order.id)).payment_status,'unpaid')
    assert.equal((await getOrder(s.env,order.id)).refunded_cents,0)
  }
})

test('platform query confirms manual completion despite failed historical tasks and preserves the no-resubmit marker', async t => {
  const s = setup(t); const buyer = await s.buyer(); const admin = await s.login(); const agent = s.partner(); const order = await s.create(buyer)
  s.db.prepare("UPDATE orders SET partner_id='partner-test',cost_cents=11000 WHERE id=?").run(order.id)
  await s.query(order.id, buyer); s.setPayment('paid'); await s.query(order.id, buyer); await s.submit(order.id, buyer)
  s.setTask('failed', true); await s.query(order.id, buyer)
  s.db.prepare('UPDATE orders SET redemption_blocked=1 WHERE id=?').run(order.id)
  const historical = s.db.prepare('SELECT * FROM recharge_redemptions').get()!
  s.override(path => path === '/v1/orders/ord_test' ? Response.json({ data: { ...s.remoteOrder(), fulfillment_status: 'succeeded' } }) : undefined)
  const before = s.calls.length
  const path = `/api/admin/orders/${order.id}/platform-status`
  assert.equal((await s.call(path, {}, admin)).status, 200)
  assert.deepEqual(s.calls.slice(before).map(c => [c.path,c.body]), [['/v1/orders/ord_test','']])
  const detail = (await (await s.call(`/api/admin/orders/${order.id}`, undefined, admin)).json()).order
  assert.equal(detail.status, 'success'); assert.equal(orderLabel(detail), '已完成'); assert.equal(detail.redemptionBlocked, true)
  assert.equal(detail.fulfillment.rechargeStatus, 'succeeded'); assert.equal(detail.fulfillment.progressStage, 'completed')
  assert.equal(detail.fulfillment.canSubmit, false); assert.equal(detail.fulfillment.finishedAt, null)
  assert.equal(detail.fulfillment.failureCode, null); assert.equal(detail.fulfillment.message, null)
  assert.deepEqual(s.db.prepare('SELECT * FROM recharge_redemptions').get(), historical)
  assert.equal((await s.submit(order.id, buyer)).status, 409)
  const rows = (await (await s.call('/api/partner/orders', undefined, agent)).json()).rows
  assert.equal(rows[0].status, 'success')
  const version = (await getOrder(s.env, order.id)).version
  assert.equal((await s.call(path, {}, admin)).status, 200)
  await reconcileRecharge(s.env)
  assert.equal((await getOrder(s.env, order.id)).version, version)
  assert.equal(s.db.prepare("SELECT COUNT(*) n FROM order_events WHERE action='platform-fulfillment-completed'").get()!.n, 1)
  assert.equal(s.calls.some(c => c.path.includes('/refunds')), false)
  assert.equal(s.calls.filter(c => c.path === '/v1/redemptions').length, 1)
})

test('manual completion cannot bypass payment, refunds or mismatched order identity', async t => {
  const s = setup(t); const buyer = await s.buyer(); const admin = await s.login(); const order = await s.create(buyer)
  await s.query(order.id, buyer)
  s.override(path => path === '/v1/orders/ord_test' ? Response.json({ data: { ...s.remoteOrder(), fulfillment_status: 'succeeded' } }) : undefined)
  const path = `/api/admin/orders/${order.id}/platform-status`
  assert.equal((await s.call(path, {}, admin)).status, 200)
  assert.equal((await getOrder(s.env, order.id)).status, 'pending')
  s.setPayment('paid'); s.db.prepare('UPDATE orders SET refund_hold=1 WHERE id=?').run(order.id)
  assert.equal((await s.call(path, {}, admin)).status, 200)
  assert.equal((await getOrder(s.env, order.id)).status, 'session')
  s.db.prepare('UPDATE orders SET refund_hold=0 WHERE id=?').run(order.id)
  for (const patch of [{ payment_status: 'partially_refunded', refunded_amount: '10.00', merchant_margin: '19.00' }, { merchant_order_no: 'wrong-order' }, { product_code: 'wrong-product' }]) {
    s.override(p => p === '/v1/orders/ord_test' ? Response.json({ data: { ...s.remoteOrder(), fulfillment_status: 'succeeded', ...patch } }) : undefined)
    const response = await s.call(path, {}, admin)
    assert.equal(response.status, Object.hasOwn(patch,'payment_status') ? 200 : 503)
    assert.equal((await getOrder(s.env, order.id)).status, 'session')
  }
  // A stale paid snapshot cannot erase the independently confirmed refund hold.
  s.override(p => p === '/v1/orders/ord_test' ? Response.json({ data: { ...s.remoteOrder(), fulfillment_status: 'succeeded' } }) : undefined)
  assert.equal((await s.call(path, {}, admin)).status, 200)
  assert.equal((await getOrder(s.env, order.id)).status, 'session')
})

test('periodic reconciliation confirms manual fulfillment without changing the failed automatic task', async t => {
  const s = setup(t); const buyer = await s.buyer(); const order = await s.create(buyer)
  await s.query(order.id, buyer); s.setPayment('paid'); await s.query(order.id, buyer); await s.submit(order.id, buyer)
  s.setTask('failed'); await s.query(order.id, buyer)
  s.db.prepare('UPDATE orders SET redemption_blocked=1 WHERE id=?').run(order.id)
  s.override(p => p === '/v1/orders/ord_test' ? Response.json({ data: { ...s.remoteOrder(), fulfillment_status: 'succeeded' } }) : undefined)
  assert.equal((await reconcileRecharge(s.env)).failed, 0)
  assert.equal((await getOrder(s.env, order.id)).status, 'success')
  assert.equal(s.db.prepare('SELECT status FROM recharge_redemptions').get()!.status, 'failed')
})

test('platform query synchronizes the original recharge task and accepts only a newer confirmed terminal correction', async t => {
  const s = setup(t); const buyer = await s.buyer(); const admin = await s.login(); const order = await s.create(buyer)
  await s.query(order.id, buyer); s.setPayment('paid'); await s.query(order.id, buyer); await s.submit(order.id, buyer)
  s.setTask('failed')
  s.override(path => path.startsWith('/v1/redemptions/') ? Response.json({ data: { ...s.redemption(), progress_version: 4 } }) : undefined)
  await s.query(order.id, buyer)
  const path = `/api/admin/orders/${order.id}/platform-status`
  for (const [status, version, expected] of [['succeeded',3,'failed'], ['succeeded',4,'failed'], ['running',5,'failed'], ['succeeded',5,'success']] as const) {
    s.setTask(status)
    s.override(p => p.startsWith('/v1/redemptions/') ? Response.json({ data: { ...s.redemption(), progress_version: version } }) : undefined)
    assert.equal((await s.call(path, {}, admin)).status, 200)
    assert.equal((await getOrder(s.env, order.id)).status, expected)
  }
  assert.equal(s.calls.filter(c => c.path === '/v1/redemptions').length, 1)
})

test('platform query records full refunds with negative margins and shares the confirmed snapshot only with the owning agent', async t => {
  const s = setup(t); const buyer = await s.buyer(); const agent = s.partner(); const order = await s.create(buyer)
  s.db.prepare("UPDATE orders SET partner_id='partner-test',cost_cents=11000 WHERE id=?").run(order.id)
  await s.query(order.id, buyer); s.setPayment('paid'); await s.query(order.id, buyer)
  const admin = await s.login()
  s.override(path => path === '/v1/orders/ord_test' ? Response.json({ data: { ...s.remoteOrder(), payment_status: 'refunded', refunded_amount: '139.00', merchant_margin: '-110.00', qr_payload: null } }) : undefined)
  const before = s.calls.length
  const response = await s.call(`/api/admin/orders/${order.id}/platform-status`, {}, admin)
  assert.equal(response.status, 200, await response.clone().text())
  const snapshot = (await response.json()).platformStatus
  assert.equal(snapshot.paymentStatus, 'refunded'); assert.equal(snapshot.refundedCents, 13900)
  assert.equal(snapshot.financialHold, true); assert.ok(snapshot.checkedAt > 0)
  assert.deepEqual(s.calls.slice(before).map(c => [c.path,c.body]), [['/v1/orders/ord_test','']])
  const stored = s.db.prepare('SELECT * FROM recharge_orders WHERE order_id=?').get(order.id)!
  assert.equal(stored.reported_margin_cents, -11000); assert.equal(stored.merchant_margin_cents, 0)
  const detail = (await (await s.call(`/api/admin/orders/${order.id}`, undefined, admin)).json()).order
  assert.deepEqual(detail.platformStatus, snapshot)
  assert.equal(orderLabel(detail), '已退款')
  assert.equal(detail.refundHold, true)
  const list = (await (await s.call('/api/admin/orders?q=' + order.id, undefined, admin)).json()).orders
  assert.equal(orderLabel(list[0]), '已退款'); assert.deepEqual(list[0].platformStatus, snapshot)
  const recent = (await (await s.call('/api/admin/overview', undefined, admin)).json()).recent
  assert.equal(orderLabel(recent.find(o => o.id === order.id)), '已退款')
  const rows = (await (await s.call('/api/partner/orders', undefined, agent)).json()).rows
  assert.equal(rows.length, 1); assert.equal(rows[0].platform_payment_status, 'refunded')
  assert.equal(rows[0].platform_refunded_cents, 13900); assert.equal(rows[0].platform_checked_at, snapshot.checkedAt)
  assert.equal(rows[0].profit_cents, 0); assert.equal(rows[0].refund_hold, 1)
  assert.equal(confirmedRefundLabel(Math.max(rows[0].refunded_cents, rows[0].platform_refunded_cents), rows[0].price_cents), '已退款')
  assert.equal((await s.call(`/api/admin/orders/${order.id}/platform-status`, {}, agent)).status, 401)
  s.db.prepare("UPDATE orders SET partner_id=NULL WHERE id=?").run(order.id)
  assert.equal((await (await s.call('/api/partner/orders', undefined, agent)).json()).rows.length, 0)
  await s.call(`/api/admin/orders/${order.id}/platform-status`, {}, admin)
  assert.equal(s.db.prepare('SELECT refunded_cents FROM orders WHERE id=?').get(order.id)!.refunded_cents, 0)
  for (const table of ['refund_cases','partner_debits','recharge_refunds']) assert.equal(s.db.prepare(`SELECT COUNT(*) n FROM ${table}`).get()!.n, 0)
  assert.equal(s.db.prepare("SELECT COUNT(*) n FROM admin_audit WHERE action='platform-order-query'").get()!.n, 2)
})

test('platform query never creates a remote order or starts a cashier and enforces admin origin and the order lease', async t => {
  const s = setup(t); const buyer = await s.buyer(); const order = await s.create(buyer); const admin = await s.login()
  const path = `/api/admin/orders/${order.id}/platform-status`
  assert.equal((await s.call(path, {})).status, 401)
  assert.equal((await s.call(path, {}, admin, 'https://foreign.example')).status, 403)
  assert.equal((await s.call(path, {}, admin)).status, 409)
  assert.equal(s.calls.filter(c => c.path === '/v1/orders').length, 0)
  s.faceToFace(); await s.query(order.id, buyer); s.setQr(null)
  assert.equal((await s.notice('evt_order_query')).status, 200)
  const before = s.calls.length
  assert.equal((await s.call(path, {}, admin)).status, 200)
  assert.deepEqual(s.calls.slice(before).map(c => [c.path,c.body]), [['/v1/orders/ord_test','']])
  const notified = s.db.prepare('SELECT notify_version,checked_notify_version,updated_at FROM recharge_orders').get()!
  assert.equal(notified.notify_version, 1); assert.equal(notified.checked_notify_version, 0); assert.equal(notified.updated_at, 0)
  s.db.prepare('UPDATE recharge_orders SET lease_until=? WHERE order_id=?').run(Date.now()+60000, order.id)
  assert.equal((await s.call(path, {}, admin)).status, 409)
  s.db.prepare('UPDATE recharge_orders SET lease_until=0 WHERE order_id=?').run(order.id)
  s.db.prepare('UPDATE orders SET is_test=1 WHERE id=?').run(order.id)
  assert.equal((await s.call(path, {}, admin)).status, 409)
})

test('partial refunds survive failed, malformed and stale platform queries without claiming a new verification', async t => {
  const s = setup(t); const buyer = await s.buyer(); const order = await s.create(buyer); const admin = await s.login()
  await s.query(order.id, buyer); s.setPayment('paid'); await s.query(order.id, buyer)
  const path = `/api/admin/orders/${order.id}/platform-status`
  const remote = { ...s.remoteOrder(), payment_status: 'partially_refunded', refunded_amount: '40.00', merchant_margin: '-11.00' }
  s.override(p => p === '/v1/orders/ord_test' ? Response.json({ data: remote }) : undefined)
  assert.equal((await s.call(path, {}, admin)).status, 200)
  const saved = s.db.prepare('SELECT payment_status,refunded_cents,reported_margin_cents,verified_at FROM recharge_orders').get()!
  assert.equal(saved.refunded_cents, 4000); assert.equal(saved.reported_margin_cents, -1100)
  s.override(p => p === '/v1/orders/ord_test' ? Response.json({ error: { code: 'temporarily_unavailable' } }, { status: 503 }) : undefined)
  assert.equal((await s.call(path, {}, admin)).status, 503)
  assert.deepEqual(s.db.prepare('SELECT payment_status,refunded_cents,reported_margin_cents,verified_at FROM recharge_orders').get(), saved)
  for (const patch of [{ sale_amount: '-139.00' }, { refunded_amount: '-40.00' }, { merchant_margin: '-110.01' }, { merchant_margin: '-11.1' }, { order_id: 'wrong-order' }]) {
    s.override(p => p === '/v1/orders/ord_test' ? Response.json({ data: { ...remote, ...patch } }) : undefined)
    assert.equal((await s.call(path, {}, admin)).status, 503)
    assert.deepEqual(s.db.prepare('SELECT payment_status,refunded_cents,reported_margin_cents,verified_at FROM recharge_orders').get(), saved)
  }
  s.override(p => p === '/v1/orders/ord_test' ? Response.json({ data: { ...remote, refunded_amount: '10.00', merchant_margin: '19.00' } }) : undefined)
  assert.equal((await s.call(path, {}, admin)).status, 200)
  assert.deepEqual(s.db.prepare('SELECT payment_status,refunded_cents,reported_margin_cents,verified_at FROM recharge_orders').get(), saved)
})

test('only platform margins accept signed money, while sale, supply and refunds stay nonnegative', () => {
  assert.equal(marginCents('-110.00'), -11000); assert.equal(marginCents('29.00'), 2900)
  for (const value of ['-110.00','-0.01','+1.00','1.1',1,null]) assert.throws(() => moneyCents(value))
  for (const value of ['--1.00','-1.1','-01.00','-Infinity']) assert.throws(() => marginCents(value))
})
