import test from 'node:test'
import assert from 'node:assert/strict'
import { customerFixture } from './customer-fixture.ts'

async function setup() {
  const s = await customerFixture()
  const a = await s.agent('统计代理'), b = await s.agent('其他代理')
  function order(patch: { partner?: string; plan?: string; price?: number; cost?: number | null; refund?: number; hold?: number; mode?: string; test?: number; payment?: string; status?: string; at?: number } = {}) {
    const id = crypto.randomUUID(), at = patch.at ?? Date.now() - 1000
    s.db.prepare(`INSERT INTO orders(id,owner_hash,idempotency_key,plan,plan_name,price_cents,cost_cents,refunded_cents,refund_hold,mode,is_test,payment_status,status,created_at,updated_at,last_event_id,partner_id)
      VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`).run(id, 'fixture', id, patch.plan ?? 'x-premium-3m', '统计套餐', patch.price ?? 3500, patch.cost === undefined ? 2100 : patch.cost, patch.refund ?? 0, patch.hold ?? 0, patch.mode ?? 'live', patch.test ?? 0, patch.payment ?? 'paid', patch.status ?? 'success', at, at, id, patch.partner ?? a.id)
    return id
  }
  async function summary(period = 'all', cookie = a.cookie) {
    const response = await s.request('/api/partner/order-summary?period=' + period, undefined, cookie)
    assert.equal(response.status, 200)
    return response.json()
  }
  return { ...s, a, b, order, summary }
}

test('product totals combine plans across all pages and isolate partners and real orders', async t => {
  const s = await setup(); t.after(() => s.db.close())
  for (let i = 0; i < 24; i++) s.order()
  s.order({ plan: 'x-premium-6m', price: 6000, cost: 4000 })
  s.order({ plan: 'plus', price: 13900, cost: 11300 })
  s.order({ payment: 'unpaid', status: 'pending' })
  s.order({ status: 'processing' })
  s.order({ partner: s.b.id })
  s.order({ mode: 'demo', payment: 'simulated' })
  s.order({ test: 1 })
  const result = await s.summary()
  assert.deepEqual(result.totals, { orderCount: 28, completedCount: 26, profitCents: 38200, heldCount: 0 })
  assert.deepEqual(result.products, [
    { productId: 'x-premium', orderCount: 27, completedCount: 25, profitCents: 35600, heldCount: 0 },
    { productId: 'chatgpt', orderCount: 1, completedCount: 1, profitCents: 2600, heldCount: 0 },
  ])
  assert.equal((await s.summary('all', s.b.cookie)).totals.orderCount, 1)
  assert.equal((await s.request('/api/partner/orders?page=1', undefined, s.a.cookie).then(r => r.json())).rows.length, 20)
  const preview = await s.request(`/api/admin/partners/${s.a.id}/order-summary`, undefined, s.admin)
  assert.equal(preview.status, 200)
  assert.deepEqual((await preview.json()).products, result.products)
  assert.equal((await s.request('/api/partner/order-summary')).status, 401)
  assert.equal((await s.request(`/api/admin/partners/${s.b.id}/order-summary`, undefined, s.a.cookie)).status, 401)
})

test('confirmed profit follows refunds, supplier margin caps and holds, including settled earnings', async t => {
  const s = await setup(); t.after(() => s.db.close())
  const paid = s.order()
  const payout = crypto.randomUUID()
  s.db.prepare("INSERT INTO partner_payouts(id,partner_id,amount_cents,recipient,created_at,request_key,status) VALUES (?,?,1400,'fixture',?,?,'paid')").run(payout, s.a.id, Date.now(), crypto.randomUUID())
  s.db.prepare('UPDATE orders SET payout_id=? WHERE id=?').run(payout, paid)
  s.order({ refund: 1750 }) // Half refund reduces commission from 1400 to 700.
  s.order({ refund: 3500 })
  s.order({ hold: 1 })
  s.order({ cost: null })
  s.order({ cost: 3500 }) // A completed zero-margin order still counts.
  for (const [margin, hold] of [[300, 0], [900, 1], [null, 0]] as const) {
    const id = s.order({ plan: 'plus' })
    s.db.prepare("INSERT INTO recharge_orders(order_id,account,product_code,collection_mode,create_body,payment_status,updated_at,merchant_margin_cents,financial_hold) VALUES (?,'fixture','plus','platform_collect','{}','paid',?,?,?)").run(id, Date.now(), margin, hold)
  }
  const result = await s.summary()
  assert.deepEqual(result.totals, { orderCount: 9, completedCount: 9, profitCents: 2400, heldCount: 4 })
})

test('periods use Beijing calendar boundaries and invalid ranges are rejected', async t => {
  const s = await setup(); t.after(() => s.db.close())
  const now = Date.parse('2026-10-06T04:00:00Z'), today = Date.parse('2026-10-05T16:00:00Z'), day = 86400000
  t.mock.method(Date, 'now', () => now)
  // Refresh the fixture session to the fixed clock.
  s.db.prepare('UPDATE partner_sessions SET expires_at=?').run(now + day)
  for (const at of [today, today - 1, today - 6 * day, today - 6 * day - 1, today - 29 * day, today - 29 * day - 1, now, now + 1]) s.order({ at })
  for (const [period, expected, since] of [['today', 2, today], ['7', 4, today - 6 * day], ['30', 6, today - 29 * day], ['all', 7, null]] as const) {
    const result = await s.summary(period)
    assert.equal(result.totals.orderCount, expected)
    assert.equal(result.since, since)
  }
  const empty = await s.summary('all', s.b.cookie)
  assert.deepEqual(empty.products, [])
  assert.deepEqual(empty.totals, { orderCount: 0, completedCount: 0, profitCents: 0, heldCount: 0 })
  assert.equal((await s.request('/api/partner/order-summary?period=invalid', undefined, s.a.cookie)).status, 400)
})
