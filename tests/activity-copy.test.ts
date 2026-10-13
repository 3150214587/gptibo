import test from 'node:test'
import assert from 'node:assert/strict'
import { activityCopy } from '../src/lib/activity-copy.ts'

test('legacy system messages retain nonfinancial facts in current presentation', () => {
  assert.equal(activityCopy('模拟付款已确认'), '测试状态已更新（无实际收款）')
  assert.equal(activityCopy('演示结果，无真实支付或履约'), '历史测试记录已结束，无支付或履约')
  assert.equal(activityCopy('模拟退款完成：核对历史记录'), '历史售后记录已结束（无资金往来）：核对历史记录')
})

test('actual payment records and arbitrary administrator notes remain unchanged', () => {
  for (const note of ['支付宝付款已确认', '已登记实际退款：流水已核实', '客户反馈演示文档打不开', 'toString', '']) assert.equal(activityCopy(note), note)
})
