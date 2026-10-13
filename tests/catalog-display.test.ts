import test from 'node:test'
import assert from 'node:assert/strict'
import { catalogStartingPrice, catalogSubscriptionPlans, formatCatalogPrice } from '../src/lib/catalog-display.ts'
import type { CatalogPlan } from '../src/lib/api.ts'

const offer = (id: string, price: number, enabled = 1): CatalogPlan => ({ id, price_cents: price, enabled, product_id: 'chatgpt', name: id, renewal_only: 0, version: 1 })
test('homepage uses shop prices, including the legacy Pro 10x identifier', () => {
  const offers = [offer('plus', 15950), offer('pro-5x', 75000), offer('pro-20x', 145000), offer('pro-25x', 460000)]
  assert.deepEqual(catalogSubscriptionPlans(offers).map(p => p.price), [159.5, 750, 1450, 4600])
  assert.equal(catalogSubscriptionPlans(offers)[2].name, 'Pro 10×')
  assert.equal(catalogStartingPrice(offers), 159.5)
  assert.equal(formatCatalogPrice(159.5), '159.50')
})
test('starting price uses purchasable tiers, retains configured prices for paused shops and never invents a price', () => {
  assert.equal(catalogStartingPrice([offer('plus', 13900, 0), offer('pro-5x', 69900)]), 699)
  assert.equal(catalogStartingPrice([]), null)
  assert.equal(catalogStartingPrice([offer('plus', 13900, 0)]), 139)
  assert.equal(catalogStartingPrice([offer('plus', -1)]), null)
  assert.equal(catalogSubscriptionPlans([])[0].price, null)
})
