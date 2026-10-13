import test from 'node:test'
import assert from 'node:assert/strict'
import { profitLabel } from '../src/partner/order-labels.ts'

test('profit labels distinguish payment, fulfillment, zero margins and demo earnings', () => {
  const o = { mode: 'live', status: 'success', payment_status: 'paid', price_cents: 13900, cost_cents: 10000, payout_status: null }
  assert.equal(profitLabel(o), '可结算')
  assert.equal(profitLabel({ ...o, payment_status: 'unpaid' }), '未到账 · 不结算')
  assert.equal(profitLabel({ ...o, status: 'cancelled' }), '已关闭 · 不结算')
  assert.equal(profitLabel({ ...o, cost_cents: 13900 }), '无差价')
  assert.equal(profitLabel({ ...o, mode: 'demo' }), '不计入结算')
  assert.equal(profitLabel({ ...o, mode: 'demo', status: 'pending' }), '不计入结算')
  assert.equal(profitLabel({ ...o, payout_status: 'pending' }), '结算处理中')
  assert.equal(profitLabel({ ...o, profit_cents: 0 }), '无差价')
  assert.equal(profitLabel({ ...o, profit_cents: 2900, refund_hold: 1 }), '售后冻结')
})
