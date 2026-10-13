import test from 'node:test'
import assert from 'node:assert/strict'
import { parseFeePercent, paymentFeeDraft } from '../src/admin/payment-settings-draft.ts'

const channels = ['alipay', 'gatewaya-alipay', 'gatewaya-wxpay', 'gatewayb-alipay', 'gatewayb-wxpay']

test('disable WeChat with a blank unused Alipay draft and save GATEWAYB at 1.5%', () => {
  const result = paymentFeeDraft(channels, ['gatewayb-alipay'], { alipay: 80, 'gatewaya-wxpay': 60 }, {
    alipay: '', 'gatewaya-wxpay': '101', 'gatewayb-alipay': '1.5',
  })
  assert.deepEqual(result.invalidChannels, [])
  assert.deepEqual(result.feeRatesBps, { alipay: 80, 'gatewaya-alipay': 0, 'gatewaya-wxpay': 60, 'gatewayb-alipay': 150, 'gatewayb-wxpay': 0 })
})

test('inactive valid drafts also retain saved fees; re-enabling validates the draft', () => {
  assert.equal(paymentFeeDraft(channels, ['gatewayb-alipay'], { 'gatewaya-wxpay': 60 }, { 'gatewaya-wxpay': '9' }).feeRatesBps['gatewaya-wxpay'], 60)
  const result = paymentFeeDraft(channels, ['gatewayb-alipay', 'gatewaya-wxpay'], {}, { 'gatewayb-alipay': '1.5', 'gatewaya-wxpay': '' })
  assert.deepEqual(result.invalidChannels, ['gatewaya-wxpay'])
  assert.equal(result.feeRatesBps['gatewayb-alipay'], 150)
})

test('enabled fees require a bounded decimal percentage without silently coercing invalid input', () => {
  for (const value of ['', ' ', 'NaN', 'Infinity', '-1', '101', '1.234', '1e2', '1.5%']) assert.equal(parseFeePercent(value), null, value)
  for (const [value, expected] of [['0', 0], ['0.01', 1], ['1.50', 150], [' 1.5 ', 150], ['100', 10000]] as const) assert.equal(parseFeePercent(value), expected)
})
