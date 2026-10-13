import assert from 'node:assert/strict'
import test from 'node:test'
import { refundCaseLabel } from '../src/lib/refunds.ts'

test('an upstream success receipt remains under reconciliation until local money is verified', () => {
  assert.equal(refundCaseLabel({ status: 'approved', platform_status: 'succeeded' }), '退款结果核对中')
  assert.equal(refundCaseLabel({ status: 'refunded', platform_status: 'succeeded' }), '退款完成')
  assert.equal(refundCaseLabel({ status: 'approved', platform_status: 'requested' }), '退款待审核')
  assert.equal(refundCaseLabel({ status: 'approved', platform_status: 'unknown' }), '提交待确认')
  assert.equal(refundCaseLabel({ status: 'approved', platform_status: 'failed' }), '退款异常，核对中')
  assert.equal(refundCaseLabel({ status: 'rejected', platform_status: 'rejected' }), '未通过')
  assert.equal(refundCaseLabel({ status: 'pending' }), '待审核')
  assert.equal(refundCaseLabel({ status: 'approved' }), '待退款')
})
