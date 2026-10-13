import { test } from 'node:test'
import assert from 'node:assert/strict'
import { makeOrder, orderLabel, ORDER_PLANS, PLANS, transitionOrder, PROCESS_MS, DEMO_SMS_PHONE, readOrders, STORAGE_KEY, type DemoOrder } from '../src/lib/demo-orders.ts'

const order: DemoOrder = { id: 'YMX-A1B2C3D4', plan: 'plus', price: 168, createdAt: 100, status: 'pending' }

test('unconfirmed submissions never claim recharging, including old processing rows', () => {
  const fulfillment = { rechargeStatus: 'unknown' }
  assert.equal(orderLabel({ ...order, status: 'session', fulfillment }), '提交待确认')
  assert.equal(orderLabel({ ...order, status: 'processing', fulfillment }), '提交待确认')
  assert.equal(orderLabel({ ...order, status: 'processing', fulfillment: { rechargeStatus: 'running' } }), '充值中')
  assert.equal(orderLabel({ ...order, status: 'success', fulfillment }), '已完成')
  assert.equal(orderLabel({ ...order, status: 'processing', fulfillment, refundHold: true }), '售后处理中')
})
test('X completion labels follow fulfilled gift payment, retain pending states and refund priority', () => {
  for (const plan of ['x-premium-3m', 'x-premium-6m'] as const) {
    const xOrder = { ...order, plan, price: 55 }
    assert.equal(orderLabel({ ...xOrder, status: 'session' }), '等待充值')
    assert.equal(orderLabel({ ...xOrder, status: 'processing' }), '充值处理中')
    assert.equal(orderLabel({ ...xOrder, status: 'success' }), '已完成')
    assert.equal(orderLabel({ ...xOrder, status: 'failed' }), '充值未完成')
    assert.equal(orderLabel({ ...xOrder, status: 'success', refundHold: true }), '售后处理中')
    assert.equal(orderLabel({ ...xOrder, status: 'success', refundedCents: 5500 }), '已退款')
  }
})

test('completed orders display completion while retaining the no-resubmit marker and refund priority', () => {
  const completed: DemoOrder = { ...order, status: 'success', redemptionBlocked: true }
  assert.equal(orderLabel(completed), '已完成')
  assert.equal(orderLabel({ ...completed, status: 'failed' }), '已禁止兑换')
  assert.equal(orderLabel({ ...completed, refundHold: true }), '售后处理中')
  assert.equal(orderLabel({ ...completed, refundedCents: 16800 }), '已退款')
})

test('duplicate payment events cannot reset a fulfilled order', () => {
  const paid = transitionOrder(order, 'pay')
  const processing = transitionOrder(paid, 'submit-success', 1000)
  const done = transitionOrder(processing, 'settle', 1000 + PROCESS_MS)
  assert.equal(done.status, 'success')
  assert.equal(transitionOrder(done, 'pay'), done)
  assert.equal(transitionOrder(done, 'submit-success'), done)
})
test('unpaid or cancelled orders cannot be fulfilled', () => {
  assert.equal(transitionOrder(order, 'submit-success'), order)
  const cancelled = transitionOrder(order, 'cancel')
  assert.equal(transitionOrder(cancelled, 'pay'), cancelled)
  assert.equal(transitionOrder(cancelled, 'submit-success'), cancelled)
})
test('failed simulation can retry without a second payment', () => {
  const running = transitionOrder(transitionOrder(order, 'pay'), 'submit-failed', 1000)
  assert.equal(transitionOrder(running, 'settle', 1100), running)
  const failed = transitionOrder(running, 'settle', 1000 + PROCESS_MS)
  assert.equal(failed.status, 'failed')
  const retry = transitionOrder(failed, 'retry')
  assert.equal(retry.status, 'session')
  assert.equal(retry.id, order.id)
  assert.equal(retry.price, order.price)
  assert.equal(retry.startedAt, undefined)
})
test('corrupt local history is ignored and interrupted processing recovers', () => {
  let saved = '{broken'
  Object.defineProperty(globalThis, 'localStorage', { configurable: true, value: { getItem: (key: string) => { assert.equal(key, STORAGE_KEY); return saved } } })
  assert.deepEqual(readOrders(), [])
  saved = JSON.stringify([{ ...order, status: 'processing', startedAt: 1, outcome: 'success' }, { ...order, status: 'processing' }, { ...order, id: 'invalid' }])
  assert.equal(readOrders().length, 1)
  assert.equal(readOrders()[0].status, 'success')
})

test('SMS orders retain their product and price through timeout and retry', () => {
  const sms = makeOrder('codex-sms-once')
  assert.equal(sms.price, 10)
  const paid = transitionOrder(sms, 'pay')
  assert.equal(orderLabel(paid), '待获取手机号')
  const numbered = transitionOrder(paid, 'acquire-phone')
  assert.equal(numbered.smsPhone, DEMO_SMS_PHONE)
  assert.equal(orderLabel(numbered), '待查询结果')
  const timeout = transitionOrder(transitionOrder(numbered, 'submit-failed', 1000), 'settle', 1000 + PROCESS_MS)
  assert.equal(orderLabel(timeout), '接码超时')
  const retry = transitionOrder(timeout, 'retry')
  const done = transitionOrder(transitionOrder(retry, 'submit-success', 2000), 'settle', 2000 + PROCESS_MS)
  assert.equal(done.id, sms.id)
  assert.equal(done.price, 10)
  assert.equal(done.plan, 'codex-sms-once')
  assert.equal(done.smsPhone, DEMO_SMS_PHONE)
  assert.equal(orderLabel(done), '已收到验证码')
})

test('SMS phone acquisition requires payment and queries require a phone', () => {
  const sms = makeOrder('codex-sms-once')
  assert.equal(transitionOrder(sms, 'acquire-phone'), sms)
  assert.equal(transitionOrder(order, 'acquire-phone'), order)
  const paid = transitionOrder(sms, 'pay')
  assert.equal(transitionOrder(paid, 'submit-success'), paid)
  assert.equal(transitionOrder(paid, 'submit-failed'), paid)
  const numbered = transitionOrder(paid, 'acquire-phone')
  assert.equal(transitionOrder(numbered, 'acquire-phone'), numbered)
  assert.equal(transitionOrder(numbered, 'pay'), numbered)
  Object.defineProperty(globalThis, 'localStorage', { configurable: true, value: { getItem: () => JSON.stringify([numbered]) } })
  assert.deepEqual(readOrders(), [numbered])
})

test('legacy SMS results gain a demo phone while invalid phone history is rejected', () => {
  const sms = makeOrder('codex-sms-once')
  const legacy = { ...sms, status: 'failed' }
  Object.defineProperty(globalThis, 'localStorage', { configurable: true, value: { getItem: () => JSON.stringify([legacy, { ...sms, smsPhone: 'unexpected value' }]) } })
  const restored = readOrders()
  assert.equal(restored.length, 1)
  assert.equal(restored[0].smsPhone, DEMO_SMS_PHONE)
  assert.equal(restored[0].price, 10)
  const retry = transitionOrder(restored[0], 'retry')
  assert.equal(retry.smsPhone, DEMO_SMS_PHONE)
  assert.equal(transitionOrder(retry, 'submit-success').status, 'processing')
})

test('mixed product history keeps old ChatGPT orders and new SMS orders', () => {
  const sms = makeOrder('codex-sms-once')
  Object.defineProperty(globalThis, 'localStorage', { configurable: true, value: { getItem: () => JSON.stringify([order, sms, { ...order, plan: 'unknown-product' }]) } })
  assert.deepEqual(readOrders().map(item => item.plan), ['plus', 'codex-sms-once'])
})

test('new prices do not rewrite historical Plus or retired Pro orders', () => {
  const oldPro: DemoOrder = { ...order, id: 'YMX-1234ABCD', plan: 'pro', price: 800 }
  Object.defineProperty(globalThis, 'localStorage', { configurable: true, value: { getItem: () => JSON.stringify([order, oldPro]) } })
  assert.deepEqual(readOrders().map(item => [item.plan, item.price]), [['plus', 168], ['pro', 800]])
  assert.equal(makeOrder('plus').price, 139)
  assert.equal(ORDER_PLANS.find(plan => plan.id === oldPro.plan)?.name, 'Pro')
  assert.throws(() => makeOrder('pro'), /no longer available/)
})

test('four ChatGPT tiers retain their prices and allow 10x and 25x new subscriptions', () => {
  assert.deepEqual(PLANS.filter(plan => plan.productId === 'chatgpt').map(plan => [plan.id, plan.price, plan.renewalOnly]), [['plus', 139, false], ['pro-5x', 699, false], ['pro-20x', 1400, false], ['pro-25x', 4500, false]])
  for (const id of ['pro-5x', 'pro-20x', 'pro-25x'] as const) {
    const purchase = makeOrder(id)
    const done = transitionOrder(transitionOrder(transitionOrder(purchase, 'pay'), 'submit-success', 1000), 'settle', 1000 + PROCESS_MS)
    assert.equal(done.plan, id)
    assert.equal(done.price, purchase.price)
    assert.equal(done.status, 'success')
    Object.defineProperty(globalThis, 'localStorage', { configurable: true, value: { getItem: () => JSON.stringify([done]) } })
    assert.deepEqual(readOrders(), [done])
  }
})
