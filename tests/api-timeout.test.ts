import test from 'node:test'
import assert from 'node:assert/strict'
import { api } from '../src/lib/api.ts'

test('supplier operations have time for chained calls while order reads stay bounded', async t => {
  const budgets: number[] = []
  t.mock.method(AbortSignal, 'timeout', (ms: number) => { budgets.push(ms); return new AbortController().signal })
  t.mock.method(globalThis, 'fetch', async () => Response.json({ ok: true }))
  await api('/api/orders', { key: 'original-order' })
  await api('/api/orders/original/fulfillment', { action: 'poll' })
  await api('/api/orders/original/fulfillment', { action: 'submit' })
  await api('/api/admin/refunds/original', { action: 'approve-submit-platform' })
  await api('/api/orders')
  await api('/api/catalog')
  assert.deepEqual(budgets, [50000, 50000, 50000, 50000, 15000, 15000])
})

test('closing the checkout still aborts a supplier operation before its timeout', async t => {
  const controller = new AbortController()
  t.mock.method(globalThis, 'fetch', async (_url: string, options: RequestInit) => {
    controller.abort()
    options.signal!.throwIfAborted()
  })
  await assert.rejects(api('/api/orders/original/fulfillment', { action: 'poll' }, controller.signal), { name: 'AbortError' })
})
