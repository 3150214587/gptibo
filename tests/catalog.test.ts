import test from 'node:test'
import assert from 'node:assert/strict'
import { customerFixture } from './customer-fixture.ts'

test('homepage and referrals read their own current prices without supplier requests or KV', async t => {
  const s = await customerFixture(); t.after(() => s.db.close())
  const a = await s.agent('A'), b = await s.agent('B')
  for (const [id, price] of [[a.id, 15500], [b.id, 16600]] as const) s.db.prepare("INSERT INTO partner_prices(partner_id,plan,cost_cents,price_cents) VALUES (?,'plus',12000,?)").run(id, price)
  let calls = 0, kvCalls = 0
  t.mock.method(globalThis, 'fetch', async () => { calls++; throw new Error('supplier offline') })
  // Even a stale binding left on a deployed environment must never be used.
  Object.assign(s.env, { CATALOG_CACHE: { async get() { kvCalls++; throw new Error('quota exceeded') }, async put() { kvCalls++; throw new Error('quota exceeded') } }, ORDER_TESTING_ENABLED: 'false', RECHARGE_CHECKOUT_ENABLED: 'true', RECHARGE_PARTNER_ID: 'fixture', RECHARGE_KEY_ID: 'key', RECHARGE_CLIENT_SECRET: 'secret', RECHARGE_WEBHOOK_SECRET: 'hook', RECHARGE_NOTIFY_URL: 'https://shop.example.invalid/api/recharge/webhook', RECHARGE_PRODUCT_MAP: '{"plus":"plus"}' })
  for (const [path, price] of [['/api/catalog', 13900], ['/api/catalog?partner=' + a.id.slice(0,6), 15500], ['/api/catalog?partner=' + b.id.slice(0,6), 16600]] as const) {
    const response = await s.request(path)
    assert.equal(response.status, 200); assert.equal(response.headers.get('Cache-Control'), 'no-store')
    assert.equal(response.headers.get('X-Catalog-Cache'), null)
    assert.match(response.headers.get('Server-Timing')!, /^catalog;dur=\d+(\.\d+)?$/)
    const value = await response.json()
    assert.equal(value.plans[0].price_cents, price); assert.equal(value.plans[0].enabled, 1)
    assert.doesNotMatch(JSON.stringify(value), /cost_cents|secret|customer_email/)
  }
  s.db.prepare("UPDATE catalog_plans SET price_cents=14900,version=version+1 WHERE id='plus'").run()
  s.db.prepare("UPDATE partner_prices SET price_cents=16500,version=version+1 WHERE partner_id=? AND plan='plus'").run(a.id)
  assert.equal((await (await s.request('/api/catalog')).json()).plans[0].price_cents, 14900)
  assert.equal((await (await s.request('/api/catalog?partner=' + a.id.slice(0,6))).json()).plans[0].price_cents, 16500)
  assert.equal((await (await s.request('/api/catalog?partner=' + b.id.slice(0,6))).json()).plans[0].price_cents, 16600)
  s.db.prepare("UPDATE catalog_plans SET enabled=0 WHERE id='plus'").run()
  assert.equal((await (await s.request('/api/catalog')).json()).plans[0].enabled, 0)
  assert.equal((await (await s.request('/api/catalog?partner=' + a.id.slice(0,6))).json()).plans[0].enabled, 0)
  s.db.prepare('UPDATE partners SET active=0 WHERE id=?').run(a.id)
  assert.equal((await s.request('/api/catalog?partner=' + a.id.slice(0,6))).status, 404)
  assert.equal(calls, 0); assert.equal(kvCalls, 0)
})

test('catalog immediately reflects checkout configuration changes and fails closed on D1 errors', async t => {
  const s = await customerFixture(); t.after(() => s.db.close())
  assert.equal((await (await s.request('/api/catalog')).json()).checkoutEnabled, true)
  s.env.ORDER_TESTING_ENABLED = 'false'
  assert.equal((await (await s.request('/api/catalog')).json()).checkoutEnabled, false)
  t.mock.method(s.env.INTEREST_DB, 'prepare', () => { throw new Error('D1 unavailable') })
  assert.equal((await s.request('/api/catalog')).status, 503)
})
