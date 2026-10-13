import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { customerFixture } from './customer-fixture.ts'

const migration = readFileSync(new URL('../migrations/0035_xgift_partner_prices.sql', import.meta.url), 'utf8')

test('X price backfill reaches existing partners, preserves custom prices and never changes sale switches', async t => {
  const s = await customerFixture(); t.after(() => s.db.close())
  const a = await s.agent('existing'), b = await s.agent('inactive')
  s.db.prepare('UPDATE partners SET active=0 WHERE id=?').run(b.id)
  s.db.exec("UPDATE catalog_plans SET price_cents=CASE id WHEN 'x-premium-3m' THEN 5900 ELSE 9900 END,enabled=0 WHERE product_id='x-premium'; DELETE FROM partner_default_prices WHERE plan='x-premium-3m'; UPDATE partner_default_prices SET cost_cents=5000 WHERE plan='x-premium-6m'")
  s.db.prepare("INSERT INTO partner_prices(partner_id,plan,cost_cents,price_cents,version) VALUES (?,'x-premium-3m',4000,6500,7)").run(a.id)
  const beforeCatalog = s.db.prepare('SELECT * FROM catalog_plans').all()
  const beforeVersion = s.db.prepare('SELECT version FROM partner_default_price_settings').get()!.version as number
  s.db.exec(migration)
  assert.deepEqual(s.db.prepare('SELECT * FROM catalog_plans').all(), beforeCatalog)
  assert.equal(s.db.prepare('SELECT version FROM partner_default_price_settings').get()!.version, beforeVersion + 1)
  assert.deepEqual(s.db.prepare('SELECT plan,cost_cents,price_cents,version FROM partner_prices WHERE partner_id=? ORDER BY plan').all(a.id).map(row => ({ ...row })), [
    { plan: 'x-premium-3m', cost_cents: 4000, price_cents: 6500, version: 7 },
    { plan: 'x-premium-6m', cost_cents: 5000, price_cents: 9900, version: 0 },
  ])
  assert.deepEqual(s.db.prepare('SELECT plan,cost_cents,price_cents FROM partner_prices WHERE partner_id=? ORDER BY plan').all(b.id).map(row => ({ ...row })), [
    { plan: 'x-premium-3m', cost_cents: 5900, price_cents: 5900 },
    { plan: 'x-premium-6m', cost_cents: 5000, price_cents: 9900 },
  ])
  const before = s.db.prepare('SELECT * FROM partner_prices ORDER BY partner_id,plan').all()
  s.db.exec(migration)
  assert.deepEqual(s.db.prepare('SELECT * FROM partner_prices ORDER BY partner_id,plan').all(), before)
  assert.equal(s.db.prepare('SELECT version FROM partner_default_price_settings').get()!.version, beforeVersion + 1)
})

test('backfilled X plans allow admin supply and partner retail editing while disabled for sale', async t => {
  const s = await customerFixture(); t.after(() => s.db.close())
  const a = await s.agent('existing'), b = await s.agent('other')
  s.db.exec("UPDATE catalog_plans SET enabled=0 WHERE product_id='x-premium'")
  s.db.exec(migration)
  const dashboard = await (await s.request('/api/partner/dashboard', undefined, a.cookie)).json()
  assert.equal(dashboard.plans.length, 2)
  assert.ok(dashboard.plans.every((plan: { enabled: number }) => plan.enabled === 0))
  const row = dashboard.plans.find((plan: { id: string }) => plan.id === 'x-premium-3m')
  const path = '/api/partner/prices/' + row.id
  assert.equal((await s.request(path, { priceCents: 6500, version: 0 })).status, 401)
  assert.equal((await s.request(`/api/admin/partners/${a.id}/prices/${row.id}`, { costCents: 1500, version: 0 }, s.admin)).status, 200)
  assert.equal((await s.request(path, { priceCents: 6500, version: 0 }, a.cookie)).status, 409)
  assert.equal((await s.request(path, { priceCents: 1499, version: 1 }, a.cookie)).status, 400)
  assert.equal((await s.request(path, { priceCents: 6500, version: 1 }, a.cookie)).status, 200)
  const saved = s.db.prepare('SELECT cost_cents,price_cents,version FROM partner_prices WHERE partner_id=? AND plan=?').get(a.id, row.id)!
  assert.deepEqual({ ...saved }, { cost_cents: 1500, price_cents: 6500, version: 2 })
  assert.equal(s.db.prepare('SELECT price_cents FROM partner_prices WHERE partner_id=? AND plan=?').get(b.id, row.id)!.price_cents, row.price_cents)
  const catalog = await (await s.request('/api/catalog?partner=' + a.id.slice(0, 6))).json()
  const offer = catalog.plans.find((plan: { id: string }) => plan.id === row.id)
  assert.equal(offer.price_cents, 6500); assert.equal(offer.enabled, 0)
  assert.equal('cost_cents' in offer, false)
  const buyer = (await s.request('/api/orders/client', {})).headers.get('set-cookie')!.split(';')[0]
  assert.equal((await s.request('/api/orders', { key: crypto.randomUUID(), plan: row.id, partner: a.id.slice(0, 6), expectedPrice: 6500, email: 'buyer@example.test' }, buyer)).status, 409)
  assert.equal(s.db.prepare('SELECT COUNT(*) n FROM orders').get()!.n, 0)
})
