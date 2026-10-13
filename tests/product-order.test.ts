import test from 'node:test'
import assert from 'node:assert/strict'
import { customerFixture } from './customer-fixture.ts'
import { DEFAULT_PRODUCT_ORDER, normalizeProductOrder } from '../shared/product-order.ts'

const endpoint = '/api/partner/product-order'
const reordered = ['gmail', 'codex-sms', ...DEFAULT_PRODUCT_ORDER.filter(id => id !== 'gmail' && id !== 'codex-sms')]

test('saved product order survives reads, reaches only its shop and can be restored', async t => {
  const s = await customerFixture(); t.after(() => s.db.close())
  const a = await s.agent('A'), b = await s.agent('B')
  const read = async (cookie: string) => (await s.request(endpoint, undefined, cookie)).json()
  assert.deepEqual(await read(a.cookie), { productIds: DEFAULT_PRODUCT_ORDER, version: 0, hiddenProductIds: [] })
  const saved = await s.request(endpoint, { productIds: reordered, version: 0, partnerId: b.id }, a.cookie)
  assert.equal(saved.status, 200)
  assert.deepEqual(await saved.json(), { productIds: reordered, version: 1, hiddenProductIds: [] })
  assert.deepEqual(await read(a.cookie), { productIds: reordered, version: 1, hiddenProductIds: [] })
  assert.deepEqual(await read(b.cookie), { productIds: DEFAULT_PRODUCT_ORDER, version: 0, hiddenProductIds: [] })
  const slug = s.db.prepare('SELECT slug FROM partners WHERE id=?').get(a.id)!.slug
  const catalog = await (await s.request('/api/catalog?partner=' + slug)).json()
  assert.deepEqual(catalog.productOrder, reordered)
  assert.equal((await (await s.request('/api/catalog')).json()).productOrder, undefined)
  assert.equal(s.db.prepare("SELECT COUNT(*) n FROM admin_audit WHERE target_id=? AND action='partner-product-order'").get(a.id)!.n, 1)
  assert.equal((await s.request(endpoint, { productIds: DEFAULT_PRODUCT_ORDER, version: 1 }, a.cookie)).status, 200)
  assert.deepEqual(await read(a.cookie), { productIds: DEFAULT_PRODUCT_ORDER, version: 2, hiddenProductIds: [] })
})

test('sort writes require an active partner and reject cross-origin requests; admin preview is read-only', async t => {
  const s = await customerFixture(); t.after(() => s.db.close())
  const a = await s.agent('A')
  const payload = { productIds: reordered, version: 0 }
  assert.equal((await s.request(endpoint, payload)).status, 401)
  assert.equal((await s.request(endpoint, payload, s.admin)).status, 401)
  assert.equal((await s.request(endpoint, payload, a.cookie, 'https://untrusted.test')).status, 403)
  const preview = '/api/admin/partners/' + a.id + '/product-order'
  assert.equal((await s.request(preview, undefined, a.cookie)).status, 401)
  assert.deepEqual(await (await s.request(preview, undefined, s.admin)).json(), { productIds: DEFAULT_PRODUCT_ORDER, version: 0, hiddenProductIds: [] })
  assert.equal((await s.request(preview, payload, s.admin)).status, 405)
  s.db.prepare('UPDATE partners SET active=0 WHERE id=?').run(a.id)
  assert.equal((await s.request(endpoint, payload, a.cookie)).status, 401)
  assert.equal(s.db.prepare('SELECT COUNT(*) n FROM partner_product_order').get()!.n, 0)
})

test('invalid lists and versions never change the saved order', async t => {
  const s = await customerFixture(); t.after(() => s.db.close())
  const a = await s.agent('A')
  for (const productIds of [null, 'gmail', [], [...reordered, 'unknown'], reordered.slice(1), reordered.map((id, i) => i === 1 ? reordered[0] : id), reordered.map((id, i) => i === 0 ? 'unknown' : id), reordered.map((id, i) => i === 0 ? {} : id)]) {
    assert.equal((await s.request(endpoint, { productIds, version: 0 }, a.cookie)).status, 400)
  }
  for (const version of [undefined, null, -1, 0.5, '0', Number.MAX_SAFE_INTEGER + 1]) {
    assert.equal((await s.request(endpoint, { productIds: reordered, version }, a.cookie)).status, 400)
  }
  assert.equal(s.db.prepare('SELECT COUNT(*) n FROM partner_product_order').get()!.n, 0)
})

test('concurrent first saves and subsequent stale saves cannot overwrite another editor', async t => {
  const s = await customerFixture(); t.after(() => s.db.close())
  const a = await s.agent('A')
  const results = await Promise.all([
    s.request(endpoint, { productIds: reordered, version: 0 }, a.cookie),
    s.request(endpoint, { productIds: DEFAULT_PRODUCT_ORDER, version: 0 }, a.cookie),
  ])
  assert.deepEqual(results.map(r => r.status).sort(), [200, 409])
  const current = await (await s.request(endpoint, undefined, a.cookie)).json()
  assert.equal(current.version, 1)
  assert.equal((await s.request(endpoint, { productIds: reordered, version: 0 }, a.cookie)).status, 409)
  assert.deepEqual(await (await s.request(endpoint, undefined, a.cookie)).json(), current)
  assert.equal(s.db.prepare("SELECT COUNT(*) n FROM admin_audit WHERE action='partner-product-order'").get()!.n, 1)
})

test('revoking the session after authentication prevents the atomic write', async t => {
  const s = await customerFixture(); t.after(() => s.db.close())
  const a = await s.agent('A')
  const batch = s.env.INTEREST_DB.batch
  s.env.INTEREST_DB.batch = async statements => {
    s.db.prepare('UPDATE partners SET auth_version=auth_version+1 WHERE id=?').run(a.id)
    return batch(statements)
  }
  assert.equal((await s.request(endpoint, { productIds: reordered, version: 0 }, a.cookie)).status, 409)
  assert.equal(s.db.prepare('SELECT COUNT(*) n FROM partner_product_order').get()!.n, 0)
})

test('new products are appended and retired or repeated products are discarded', () => {
  assert.deepEqual(normalizeProductOrder(['gmail', 'retired', 'gmail', 'chatgpt']), ['gmail', 'chatgpt', ...DEFAULT_PRODUCT_ORDER.filter(id => id !== 'gmail' && id !== 'chatgpt')])
  assert.deepEqual(normalizeProductOrder(), DEFAULT_PRODUCT_ORDER)
})

test('visibility hides every package only in its shop, supports relisting and preserves old orders', async t => {
  const s = await customerFixture(); t.after(() => s.db.close())
  const a = await s.agent('A'), b = await s.agent('B')
  for (const agent of [a, b]) s.db.prepare('INSERT INTO partner_prices(partner_id,plan,price_cents,cost_cents) SELECT ?,id,price_cents,100 FROM catalog_plans').run(agent.id)
  s.db.exec("UPDATE catalog_plans SET enabled=1,partner_enabled=1 WHERE product_id='chatgpt'")
  const slug = (id: string) => String(s.db.prepare('SELECT slug FROM partners WHERE id=?').get(id)!.slug)
  const buyer = (await s.request('/api/orders/client', {})).headers.get('Set-Cookie')!.split(';')[0]
  const payload = { plan: 'plus', expectedPrice: 13900, key: crypto.randomUUID(), partner: slug(a.id), email: 'buyer@example.test' }
  const created = await s.request('/api/orders', payload, buyer)
  assert.equal(created.status, 201, await created.clone().text())
  const order = (await created.json()).order
  const save = (hiddenProductIds: unknown, version: number) => s.request(endpoint, { productIds: reordered, hiddenProductIds, version }, a.cookie)
  assert.equal((await save(['chatgpt', 'claude'], 0)).status, 200)
  const catalog = async (shop = '') => (await s.request('/api/catalog' + (shop ? '?partner=' + shop : ''))).json()
  const own = await catalog(slug(a.id))
  assert.deepEqual(own.hiddenProductIds, ['chatgpt', 'claude'])
  assert.equal(own.plans.some((p: {product_id: string}) => p.product_id === 'chatgpt'), false)
  for (const shop of ['', slug(b.id)]) assert.equal((await catalog(shop)).plans.some((p: {product_id: string}) => p.product_id === 'chatgpt'), true)
  for (const plan of ['plus', 'pro-5x']) {
    const expectedPrice = s.db.prepare('SELECT price_cents FROM catalog_plans WHERE id=?').get(plan)!.price_cents
    assert.equal((await s.request('/api/orders', { ...payload, key: crypto.randomUUID(), plan, expectedPrice }, buyer)).status, 409)
  }
  assert.equal((await s.request('/api/orders', payload, buyer)).status, 200)
  assert.equal((await s.request('/api/orders/' + order.id, undefined, buyer)).status, 200)
  assert.equal((await s.request(endpoint, { productIds: DEFAULT_PRODUCT_ORDER, version: 1 }, a.cookie)).status, 200)
  assert.deepEqual((await (await s.request(endpoint, undefined, a.cookie)).json()).hiddenProductIds, ['chatgpt', 'claude'])
  for (const hidden of ['chatgpt', ['unknown'], ['chatgpt', 'chatgpt'], [{}]]) assert.equal((await save(hidden, 2)).status, 400)
  assert.equal((await save([], 1)).status, 409)
  assert.equal((await save(DEFAULT_PRODUCT_ORDER, 2)).status, 200)
  assert.deepEqual((await catalog(slug(a.id))).plans, [])
  assert.equal((await save([], 3)).status, 200)
  assert.equal((await s.request('/api/orders', { ...payload, key: crypto.randomUUID() }, buyer)).status, 201)
  s.db.exec("UPDATE catalog_plans SET partner_enabled=0 WHERE product_id='chatgpt'")
  assert.equal((await s.request('/api/orders', { ...payload, key: crypto.randomUUID() }, buyer)).status, 409)
})

test('delisting between order precheck and insert prevents an order from being saved', async t => {
  const s = await customerFixture(); t.after(() => s.db.close())
  const a = await s.agent('A')
  s.db.prepare('INSERT INTO partner_prices(partner_id,plan,price_cents,cost_cents) VALUES(?,?,?,?)').run(a.id, 'plus', 13900, 100)
  s.db.exec("UPDATE catalog_plans SET enabled=1,partner_enabled=1 WHERE id='plus'")
  const slug = s.db.prepare('SELECT slug FROM partners WHERE id=?').get(a.id)!.slug
  const buyer = (await s.request('/api/orders/client', {})).headers.get('Set-Cookie')!.split(';')[0]
  const batch = s.env.INTEREST_DB.batch
  s.env.INTEREST_DB.batch = async statements => {
    s.db.prepare('INSERT INTO partner_product_order(partner_id,product_ids,hidden_product_ids,version,change_id,updated_at) VALUES(?,?,?,?,?,?)').run(a.id, JSON.stringify(DEFAULT_PRODUCT_ORDER), '["chatgpt"]', 1, 'fixture', Date.now())
    return batch(statements)
  }
  const response = await s.request('/api/orders', { key: crypto.randomUUID(), plan: 'plus', expectedPrice: 13900, partner: slug, email: 'buyer@example.test' }, buyer)
  assert.equal(response.status, 409, await response.clone().text())
  assert.equal(s.db.prepare('SELECT count(*) n FROM orders').get()!.n, 0)
})
