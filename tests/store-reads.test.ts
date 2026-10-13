import test from 'node:test'
import assert from 'node:assert/strict'
import { readStoreResources } from '../src/lib/store-reads.ts'

test('prices render before buyer initialization and slow orders; checkout waits for the buyer cookie only', async () => {
  let initialize!: () => void, finishOrders!: (value: string) => void
  const events: string[] = []
  const pending = readStoreResources({
    initialize: () => new Promise<void>(resolve => { initialize = resolve }),
    orders: () => new Promise<string>(resolve => { events.push('orders-request'); finishOrders = resolve }),
    catalog: async () => 'prices',
    onCatalog: value => events.push(value),
    onOrders: value => events.push(value),
    onReady: () => { events.push('ready') },
  })
  await new Promise(resolve => setImmediate(resolve))
  assert.deepEqual(events, ['prices'])
  initialize()
  await new Promise(resolve => setImmediate(resolve))
  assert.deepEqual(events, ['prices', 'orders-request', 'ready'])
  finishOrders('orders')
  assert.ok((await pending).every(result => result.status === 'fulfilled'))
})

test('failed private initialization still allows public prices to render without enabling checkout', async () => {
  const events: string[] = []
  const results = await readStoreResources({
    initialize: async () => { throw new Error('unavailable') }, orders: async () => 'orders', catalog: async () => 'prices',
    onCatalog: value => events.push(value), onOrders: value => events.push(value), onReady: () => { events.push('ready') },
  })
  assert.deepEqual(events, ['prices'])
  assert.deepEqual(results.map(result => result.status), ['rejected', 'fulfilled', 'rejected'])
})
