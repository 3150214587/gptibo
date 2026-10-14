import test from 'node:test'
import assert from 'node:assert/strict'
import { customerFixture } from './customer-fixture.ts'
import { fulfillXGift, recoverXGift } from '../worker/xgift/service.ts'
import { signature } from '../shared/xgift-signature.ts'
import { applyPaymentResult, findPayment } from '../worker/payments/service.ts'
import { getOrder } from '../worker/order-state.ts'
type Context = Parameters<Parameters<typeof test>[1]>[0]

async function fixture(t: Context) {
  const s = await customerFixture(); t.after(() => s.db.close())
  Object.assign(s.env, { ORDER_TESTING_ENABLED: 'false', XGIFT_CHECKOUT_ENABLED: 'true', XGIFT_BACKEND: 'platform',
    XGIFT_PLATFORM_API_BASE: 'https://platform.example.test/v1', XGIFT_PLATFORM_KEY_ID: 'key_platform', XGIFT_PLATFORM_CLIENT_SECRET: 'test-platform-secret',
    XGIFT_API_BASE: 'https://legacy.example.test/v1', XGIFT_USER_ID: 'usr_fixture', XGIFT_KEY_ID: 'key_legacy', XGIFT_CLIENT_SECRET: 'test-legacy-secret' })
  s.db.exec("UPDATE catalog_plans SET price_cents=CASE id WHEN 'x-premium-3m' THEN 3500 ELSE 5500 END WHERE product_id='x-premium'")
  const state = { failed: false, receipt: '', onQuery: null as (() => void) | null, creates: 0, queries: 0, lost: false, missing: false, succeeded: false, mismatch: false, available: true, price: 1700, changedID: false, destinations: [] as string[] }
  let accepted: Record<string, unknown> = {}
  t.mock.method(globalThis, 'fetch', async (input: string | URL, options: RequestInit) => {
    const url = new URL(String(input)), headers = new Headers(options.headers), raw = String(options.body || '')
    const platform = url.origin === 'https://platform.example.test'
    assert.ok(platform || url.origin === 'https://legacy.example.test'); state.destinations.push(url.origin)
    assert.equal(headers.get('X-Key-Id'), platform ? 'key_platform' : 'key_legacy')
    assert.equal(headers.get('X-Signature'), await signature(platform ? 'test-platform-secret' : 'test-legacy-secret', options.method!, url, headers.get('X-Timestamp')!, headers.get('X-Nonce')!, headers.get('X-Key-Id')!, headers.get('Idempotency-Key') || '', raw))
    assert.equal(options.redirect, 'manual')
    if (url.pathname === '/v1/preflight' || url.pathname === '/v1/eligibility') {
      const body = JSON.parse(raw)
      if (platform) assert.ok(['x-3m','x-6m'].includes(body.product_code))
      return Response.json({ data: { [platform ? 'recipient' : 'username']: 'receiver', recipient_id: state.changedID ? '999' : '12345', eligible: true, reason: null } })
    }
    if (url.pathname === '/v1/products') return Response.json({ data: platform
      ? [{ code:'x-3m',business:'x',enabled:true,available:state.available,supply_minor:state.price },{ code:'x-6m',business:'x',enabled:true,available:state.available,supply_minor:3400 }]
      : [{ code:'x-premium-3m',enabled:1,points:1700 },{ code:'x-premium-6m',enabled:1,points:3400 }] })
    if (url.pathname === '/v1/balance') return Response.json({ data: platform ? {currency:'CNY',available_minor:300000000,frozen_minor:0} : {available:3400,frozen:0} })
    if (url.pathname === '/v1/capabilities') return Response.json({ data: { accepts_orders:true } })
    if (options.method === 'POST') {
      assert.equal(url.pathname,'/v1/orders'); state.creates++; accepted=JSON.parse(raw)
      if (platform) {
        assert.equal(headers.get('X-Partner-Id'),null)
        assert.equal(accepted.confirmed_recipient_id,'12345'); assert.equal(accepted.expected_supply_minor,accepted.product_code === 'x-3m' ? 1700 : 3400)
        assert.equal(accepted.recipient_id,undefined); assert.equal(accepted.expected_points,undefined)
      }
      assert.equal(headers.get('Idempotency-Key'),'gptibo:'+accepted.merchant_order_no)
      if (state.lost) throw Error('accepted response lost')
    } else {
      state.queries++; state.onQuery?.()
      if (platform) assert.equal(url.pathname,'/v1/orders/by-merchant/'+encodeURIComponent(String(accepted.merchant_order_no)))
      else assert.equal(url.searchParams.get('merchant_order_no'),accepted.merchant_order_no)
      if(state.missing) return Response.json({error:{code:platform?'order_missing':'not_found',message:'private'}},{status:404})
    }
    const product=String(accepted.product_code), price=platform?Number(accepted.expected_supply_minor):Number(accepted.expected_points)
    const result={id:'ord_'+'a'.repeat(32),merchant_order_no:accepted.merchant_order_no,product_code:product,recipient:state.mismatch?'other':accepted.recipient,
      status:state.failed?'failed':state.succeeded?'succeeded':'running',receipt:state.succeeded?(platform?'rcpt_0123456789abcdef01234567':'cs_live_fixture123'):state.receipt,failure_code:state.failed?'card_wallet_insufficient':'',
      ...(platform ? {business:'x',supply_minor:price,created_at:new Date().toISOString(),updated_at:new Date().toISOString()}
        : {points:price,currency:'bdt',amount_minor:product==='x-premium-3m'?30000:60000,created_at:Date.now(),updated_at:Date.now()})}
    return Response.json({data:result})
  })
  const buyer=(await s.request('/api/orders/client',{})).headers.get('set-cookie')!.split(';')[0]
  async function draft(plan='x-premium-3m') {
    const response=await s.request('/api/orders/xgift/eligibility',{username:'@Receiver',plan},buyer)
    assert.equal(response.status,200,await response.clone().text())
    return {key:crypto.randomUUID(),plan,expectedPrice:plan==='x-premium-3m'?3500:5500,email:'buyer@example.test',username:'@Receiver',preflightToken:(await response.json()).token}
  }
  async function create(plan='x-premium-3m') {
    const data=await draft(plan),r=await s.request('/api/orders',data,buyer)
    assert.equal(r.status,201,await r.clone().text());return (await r.json()).order
  }
  async function pay(id:string) {
    s.db.prepare("INSERT INTO payments(order_id,provider,account,amount_cents,expires_at,created_at,updated_at) SELECT id,'alipay','fixture-alipay',price_cents,?,?,? FROM orders WHERE id=?").run(Date.now()+60000,Date.now(),Date.now(),id)
    const p=(await findPayment(s.env,id))!
    await applyPaymentResult(s.env,p,{orderId:id,amountCents:p.amount_cents,status:'paid',tradeNo:'trade-fixture-'+id})
  }
  return {...s,state,buyer,draft,create,pay}
}
test('platform cutover signs the new contract, uses CNY cost, and recovers a lost reply by the original merchant number',async t=>{
  const s=await fixture(t),o=await s.create('x-premium-6m')
  assert.equal(s.db.prepare('SELECT supplier_backend FROM xgift_purchases').get()!.supplier_backend,'platform')
  await fulfillXGift(s.env,o.id);assert.equal(s.state.creates,0)
  s.state.lost=true;await s.pay(o.id);assert.equal(s.state.creates,1)
  assert.equal(s.db.prepare('SELECT state FROM xgift_purchases').get()!.state,'unknown')
  s.state.succeeded=true;s.env.XGIFT_BACKEND='legacy';s.db.exec('UPDATE xgift_purchases SET next_check=0')
  await fulfillXGift(s.env,o.id);await fulfillXGift(s.env,o.id)
  assert.equal(s.state.creates,1);assert.equal(s.state.queries,1)
  assert.ok(s.state.destinations.every(x=>x==='https://platform.example.test'))
  const order=s.db.prepare('SELECT status,cost_cents FROM orders').get()!
  assert.equal(order.status,'success');assert.equal(order.cost_cents,3400)
  assert.throws(()=>s.db.exec("UPDATE xgift_purchases SET supplier_backend='legacy'"),/immutable_xgift_backend/)
})
test('legacy purchases keep their credentials and supplier after enabling the new backend',async t=>{
  const s=await fixture(t);s.env.XGIFT_BACKEND='legacy';const o=await s.create();s.state.lost=true
  await s.pay(o.id);s.state.destinations=[];s.env.XGIFT_BACKEND='platform';s.state.succeeded=true;s.db.exec('UPDATE xgift_purchases SET next_check=0')
  await fulfillXGift(s.env,o.id)
  assert.equal(s.state.creates,1);assert.equal(s.state.queries,1)
  assert.deepEqual(s.state.destinations,['https://legacy.example.test'])
  assert.equal(s.db.prepare('SELECT status FROM orders').get()!.status,'success')
})
test('cutover invalidates old preflight proofs before payment, and unready or repriced products cannot collect payment',async t=>{
  const s=await fixture(t),data=await s.draft();s.env.XGIFT_BACKEND='legacy'
  assert.equal((await s.request('/api/orders',data,s.buyer)).status,409)
  assert.equal(s.db.prepare('SELECT COUNT(*) n FROM orders').get()!.n,0)
  s.env.XGIFT_BACKEND='platform';s.state.available=false
  assert.equal((await s.request('/api/orders/xgift/eligibility',{username:'Receiver',plan:'x-premium-3m'},s.buyer)).status,503)
  s.state.available=true;s.state.price=1701
  assert.equal((await s.request('/api/orders/xgift/eligibility',{username:'Receiver',plan:'x-premium-3m'},s.buyer)).status,503)
})
test('platform recovery only retries an explicit missing order with the same request, and rejects mismatched evidence',async t=>{
  const s=await fixture(t),o=await s.create();s.state.lost=true;await s.pay(o.id)
  s.state.missing=true;s.state.lost=false;s.state.succeeded=true;s.db.exec('UPDATE xgift_purchases SET next_check=0')
  await fulfillXGift(s.env,o.id);assert.equal(s.state.creates,2);assert.equal(s.state.queries,1)
  assert.equal(s.db.prepare('SELECT status FROM orders').get()!.status,'success')
  const next=await s.create();s.state.mismatch=true;await s.pay(next.id)
  assert.equal(s.db.prepare('SELECT state FROM xgift_purchases WHERE order_id=?').get(next.id)!.state,'unknown')
  assert.notEqual(s.db.prepare('SELECT status FROM orders WHERE id=?').get(next.id)!.status,'success')
})
test('a known supplier order that disappears cannot be recreated during reconciliation',async t=>{
  const s=await fixture(t),o=await s.create();await s.pay(o.id)
  assert.match(String(s.db.prepare('SELECT supplier_order FROM xgift_purchases').get()!.supplier_order),/^ord_/)
  s.state.missing=true;s.db.exec('UPDATE xgift_purchases SET next_check=0')
  await fulfillXGift(s.env,o.id)
  assert.equal(s.state.creates,1);assert.equal(s.state.queries,1)
  assert.equal(s.db.prepare('SELECT state FROM xgift_purchases').get()!.state,'unknown')
  assert.notEqual(s.db.prepare('SELECT status FROM orders').get()!.status,'success')
})

test('paid failed recovery keeps the original purchase history and completes the same customer order once',async t=>{
  const s=await fixture(t),o=await s.create();s.state.failed=true;await s.pay(o.id)
  const original=await getOrder(s.env,o.id)
  const request={action:'xgift-recovery',version:original.version,note:'修复余额精度后补发，原供货订单确认未付款'}
  assert.equal((await s.request(`/api/admin/orders/${o.id}/actions`,request,s.buyer)).status,401)
  // Prepare recovery, then simulate a lost create response before cron reconciliation.
  const recovered=await recoverXGift(s.env,original,request.note)
  assert.equal(recovered.merchantOrderNo,o.id+':retry:1')
  await assert.rejects(recoverXGift(s.env,original,request.note))
  const history=s.db.prepare('SELECT * FROM xgift_recovery_attempts').get()!
  assert.equal(history.previous_merchant_order_no,o.id)
  assert.equal(history.previous_supplier_order,'ord_'+'a'.repeat(32))
  assert.throws(()=>s.db.exec('DELETE FROM xgift_recovery_attempts'),/immutable_xgift_recovery/)
  s.state.failed=false;s.state.lost=true;await fulfillXGift(s.env,o.id)
  assert.equal(s.state.creates,2)
  s.state.lost=false;s.state.succeeded=true;s.db.exec('UPDATE xgift_purchases SET next_check=0')
  await fulfillXGift(s.env,o.id);await fulfillXGift(s.env,o.id)
  assert.equal(s.state.creates,2)
  assert.equal((await getOrder(s.env,o.id)).status,'success')
  assert.equal((await getOrder(s.env,o.id)).cost_cents,1700)
  assert.equal(s.db.prepare('SELECT COUNT(*) n FROM payments').get()!.n,1)
  assert.equal(s.db.prepare("SELECT COUNT(*) n FROM order_events WHERE action='xgift-recovery'").get()!.n,1)
})

test('recovery rejects unsafe local or upstream states and a concurrent refund',async t=>{
  const s=await fixture(t),o=await s.create();s.state.failed=true;await s.pay(o.id)
  const original=await getOrder(s.env,o.id)
  for (const patch of [{refunded_cents:1},{refund_hold:1},{redemption_blocked:1},{status:'success' as const},{payment_status:'unpaid' as const},{is_test:1}]) {
    await assert.rejects(recoverXGift(s.env,{...original,...patch},'补发'))
  }
  s.state.failed=false;await assert.rejects(recoverXGift(s.env,original,'补发'))
  s.state.failed=true;s.state.receipt='rcpt_0123456789abcdef01234567'
  await assert.rejects(recoverXGift(s.env,original,'补发'));s.state.receipt=''
  s.state.onQuery=()=>{s.db.prepare('UPDATE orders SET refund_hold=1 WHERE id=?').run(o.id)}
  await assert.rejects(recoverXGift(s.env,original,'补发'),/订单已更新/)
  assert.equal(s.db.prepare('SELECT COUNT(*) n FROM xgift_recovery_attempts').get()!.n,0)
  assert.equal(s.db.prepare('SELECT state FROM xgift_purchases').get()!.state,'failed')
  assert.equal(s.state.creates,1)
})

test('admin recovery checks the expected version and preserves partner settlement cost',async t=>{
  const s=await fixture(t),o=await s.create();s.state.failed=true;await s.pay(o.id)
  const partner=await s.agent('partner')
  s.db.prepare('UPDATE orders SET partner_id=?,cost_cents=2100 WHERE id=?').run(partner.id,o.id)
  const original=await getOrder(s.env,o.id)
  assert.equal((await s.request(`/api/admin/orders/${o.id}/actions`,{action:'xgift-recovery',version:-1,note:'补发'},s.admin)).status,409)
  const response=await s.request(`/api/admin/orders/${o.id}/actions`,{action:'xgift-recovery',version:original.version,note:'补发'},s.admin)
  assert.equal(response.status,200,await response.clone().text())
  s.state.failed=false;s.state.succeeded=true
  // The first recovery is already failed in this fixture; its immutable history is retained.
  const current=await getOrder(s.env,o.id)
  assert.equal(current.cost_cents,2100)
  assert.equal(s.db.prepare('SELECT COUNT(*) n FROM xgift_recovery_attempts').get()!.n,1)
})
