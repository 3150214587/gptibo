import test from 'node:test'
import assert from 'node:assert/strict'
import { failedQuery, mutationError } from '../src/lib/query-state.ts'
import { RequestError } from '../src/lib/api.ts'

test('a transient refresh failure can retain only the same query and never authorization-denied data', () => {
  const previous = { path: '/api/orders/one/refunds', revision: 0, data: { cases: ['pending'] }, error: '' }
  for (const error of [new TypeError('offline'), new RequestError('unavailable', 503), new RequestError('slow', 429)]) {
    assert.deepEqual(failedQuery(previous, previous.path, 1, error, true).data, previous.data)
    assert.equal(failedQuery(previous, '/api/orders/two/refunds', 1, error, true).data, undefined)
    assert.equal(failedQuery(previous, previous.path, 1, error, false).data, undefined)
  }
  for (const status of [400, 401, 403, 404, 409]) assert.equal(failedQuery(previous, previous.path, 1, new RequestError('denied', status), true).data, undefined)
  assert.equal(failedQuery(undefined, previous.path, 1, new TypeError(), true).data, undefined)
})
test('transport failures ask for reconciliation while known business errors retain their original message', () => {
  assert.match(mutationError(new TypeError('offline')), /结果尚未确认.*刷新进度/)
  assert.match(mutationError(new RequestError('timeout', 504)), /结果尚未确认/)
  assert.equal(mutationError(new RequestError('金额不能超过已付款金额', 400)), '金额不能超过已付款金额')
})
