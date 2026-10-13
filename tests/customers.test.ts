import test from 'node:test'
import assert from 'node:assert/strict'
import { customerFixture } from './customer-fixture.ts'
import { monthAfter } from '../shared/customers.ts'
import { customerWorkbook } from '../src/lib/customer-export.ts'

async function success(response:Response,status=200){assert.equal(response.status,status,await response.clone().text());return response.json()}
const root='/api/partner/customers'
async function client(s:Awaited<ReturnType<typeof customerFixture>>,cookie:string,name='张先生') {
  const key=crypto.randomUUID();await success(await s.request(root,{key,name,wechat:'wx-client',qq:'12345',email:'Contact@example.test',source:'朋友介绍',note:'咨询过套餐'},cookie),201);return key
}
async function account(s:Awaited<ReturnType<typeof customerFixture>>,cookie:string,id:string,value='account@example.test') {
  const key=crypto.randomUUID();await success(await s.request(root+'/'+id+'/accounts',{key,account:value,note:'个人使用'},cookie),201);return key
}
test('calendar-month expiry uses Beijing time, clamps month end and preserves time',()=>{
  for(const [from,to] of [['2025-01-31T18:32:15+08:00','2025-02-28T18:32:15+08:00'],['2024-01-31T23:59:59+08:00','2024-02-29T23:59:59+08:00'],['2026-12-31T09:00:00+08:00','2027-01-31T09:00:00+08:00'],['2026-09-30T08:00:00+08:00','2026-10-30T08:00:00+08:00']])assert.equal(monthAfter(Date.parse(from)),Date.parse(to))
})
test('customer permissions isolate every read, write and export between agents; admin can review and edit',async t=>{
  const s=await customerFixture();t.after(()=>s.db.close());const a=await s.agent('甲代理');const b=await s.agent('乙代理');const id=await client(s,a.cookie);const aid=await account(s,a.cookie,id)
  assert.equal((await s.request(root)).status,401);assert.equal((await s.request('/api/admin/customers',undefined,a.cookie)).status,401)
  assert.equal((await s.request(root+'/'+id,undefined,b.cookie)).status,404)
  assert.equal((await s.request(root+'/'+id+'/edit',{version:0,name:'偷看',qq:'123'},b.cookie)).status,404)
  assert.equal((await s.request(root+'/'+id+'/accounts',{key:crypto.randomUUID(),account:'steal'},b.cookie)).status,404)
  for(const path of ['/edit','/subscriptions'])assert.equal((await s.request(root+'/accounts/'+aid+path,{},b.cookie)).status,404)
  assert.equal((await s.request(root,{key:crypto.randomUUID(),name:'代录',qq:'123',partnerId:b.id},a.cookie)).status,403)
  assert.equal((await s.request(root+'?partner='+a.id,undefined,b.cookie)).status,403)
  const other=await success(await s.request(root,undefined,b.cookie));assert.equal(other.total,0);assert.equal(other.stats.accounts,0)
  const exportOther=await success(await s.request(root+'/export',undefined,b.cookie));assert.deepEqual(exportOther.customers,[]);assert.deepEqual(exportOther.accounts,[]);assert.deepEqual(exportOther.subscriptions,[])
  const detail=await success(await s.request('/api/admin/customers/'+id,undefined,s.admin));assert.equal(detail.customer.email,'contact@example.test');assert.equal(detail.accounts.length,1)
  await success(await s.request('/api/admin/customers/'+id+'/edit',{version:0,name:'张先生（已核对）',qq:'678',source:'客服转介',note:'管理员核对'},s.admin))
  const own=await success(await s.request(root+'/'+id,undefined,a.cookie));assert.equal(own.customer.source,'客服转介');assert.equal(own.customer.version,1)
  assert.equal((await s.request(root+'/'+id+'/edit',{version:0,name:'旧资料',qq:'123'},a.cookie)).status,409)
  assert.equal((await s.request(root,{key:crypto.randomUUID(),name:'跨站',qq:'123'},a.cookie,'https://evil.test')).status,403)
  s.db.prepare('UPDATE partners SET active=0 WHERE id=?').run(a.id);assert.equal((await s.request(root,undefined,a.cookie)).status,401)
})
test('subscriptions reject early renewals and invalid upgrades, retain paid history and protect current version',async t=>{
  const s=await customerFixture();t.after(()=>s.db.close());const a=await s.agent('代理');const id=await client(s,a.cookie);const aid=await account(s,a.cookie,id)
  const data={key:crypto.randomUUID(),version:0,plan:'plus',kind:'open',paidCents:16900,openedAt:Date.now()-86400000,note:'首次付款'}
  const open=await success(await s.request(root+'/accounts/'+aid+'/subscriptions',data,a.cookie),201);assert.equal(open.subscription.expires_at,monthAfter(data.openedAt))
  await success(await s.request(root+'/accounts/'+aid+'/subscriptions',data,a.cookie))
  assert.equal((await s.request(root+'/accounts/'+aid+'/subscriptions',{...data,paidCents:1},a.cookie)).status,409)
  assert.equal((await s.request(root+'/accounts/'+aid+'/subscriptions',{...data,key:crypto.randomUUID(),version:1,kind:'renew',openedAt:Date.now()},a.cookie)).status,409)
  assert.equal((await s.request(root+'/accounts/'+aid+'/subscriptions',{...data,key:crypto.randomUUID(),version:1,kind:'upgrade',openedAt:Date.now()},a.cookie)).status,409)
  const upgrade={...data,key:crypto.randomUUID(),version:1,plan:'pro-5x',kind:'upgrade',paidCents:79900,openedAt:Date.now(),note:'升级补款'}
  await success(await s.request(root+'/accounts/'+aid+'/subscriptions',upgrade,a.cookie),201)
  assert.equal((await s.request(root+'/accounts/'+aid+'/subscriptions',{...upgrade,key:crypto.randomUUID(),plan:'pro-20x'},a.cookie)).status,409)
  assert.equal((await s.request(root+'/accounts/'+aid+'/subscriptions',{...upgrade,key:crypto.randomUUID(),version:2,plan:'codex-sms-once'},a.cookie)).status,400)
  const detail=await success(await s.request(root+'/'+id,undefined,a.cookie));assert.equal(detail.accounts[0].plan,'pro-5x');assert.equal(detail.accounts[0].version,2);assert.equal(detail.subscriptions.length,2);assert.equal(detail.subscriptions.find(r=>r.id===data.key).paid_cents,16900);assert.equal(detail.subscriptions.find(r=>r.id===upgrade.key).previous_id,data.key)
  const list=await success(await s.request(root,undefined,a.cookie));assert.equal(list.stats.paid_cents,96800);assert.equal(list.customers[0].paid_cents,96800);assert.match(list.customers[0].plan_names,/5/)
})
test('expired accounts renew from actual opening; filtered stats, paging and exports include all matching accounts and history',async t=>{
  const s=await customerFixture();t.after(()=>s.db.close());const a=await s.agent('代理');const id=await client(s,a.cookie);const aid=await account(s,a.cookie,id)
  const past=Date.now()-70*86400000
  await success(await s.request(root+'/accounts/'+aid+'/subscriptions',{key:crypto.randomUUID(),version:0,plan:'plus',kind:'open',paidCents:13900,openedAt:past},a.cookie),201)
  const expired=await success(await s.request(root+'?expiry=expired&plan=plus',undefined,a.cookie));assert.equal(expired.stats.expired,1);assert.equal(expired.total,1)
  const now=Date.now();await success(await s.request(root+'/accounts/'+aid+'/subscriptions',{key:crypto.randomUUID(),version:1,plan:'plus',kind:'renew',paidCents:16900,openedAt:now},a.cookie),201)
  const second=await account(s,a.cookie,id,'second@example.test')
  await success(await s.request(root+'/accounts/'+second+'/subscriptions',{key:crypto.randomUUID(),version:0,plan:'pro-20x',kind:'open',paidCents:169900,openedAt:Date.now()-28*86400000},a.cookie),201)
  for(let i=0;i<21;i++)await client(s,a.cookie,'空客户'+i)
  const all=await success(await s.request(root,undefined,a.cookie));assert.equal(all.total,22);assert.equal(all.customers.length,20);assert.equal(all.stats.accounts,2);assert.equal(all.stats.soon,1);assert.equal(all.stats.expired,0)
  const page=await success(await s.request(root+'?page=2',undefined,a.cookie));assert.equal(page.customers.length,2)
  const filtered=await success(await s.request(root+'?q=second&plan=pro-20x&expiry=soon',undefined,a.cookie));assert.equal(filtered.total,1);assert.equal(filtered.stats.accounts,2);assert.equal(filtered.stats.paid_cents,200700)
  const records=await success(await s.request(root+'/export?q=second&plan=pro-20x&expiry=soon',undefined,a.cookie));assert.equal(records.customers.length,1);assert.equal(records.accounts.length,2);assert.equal(records.subscriptions.length,3)
  const workbook=await customerWorkbook(records);const {default:ExcelJS}=await import('exceljs');const parsed=new ExcelJS.Workbook();await parsed.xlsx.load(workbook as never);assert.equal(parsed.worksheets.length,3);assert.equal(parsed.getWorksheet('当前会员')!.rowCount,3);assert.equal(parsed.getWorksheet('开通续费升级记录')!.rowCount,4);assert.equal(parsed.getWorksheet('客户资料')!.getCell('D2').value,'朋友介绍')
  records.customers[0].name='=HYPERLINK("https://evil.test")';const literal=new ExcelJS.Workbook();await literal.xlsx.load(await customerWorkbook(records) as never);assert.equal(literal.getWorksheet('客户资料')!.getCell('B2').type,ExcelJS.ValueType.String)
  for(const q of ['plan=codex-sms-once','expiry=no','page=0','partner=invalid'])assert.equal((await s.request(root+'?'+q,undefined,a.cookie)).status,q.startsWith('partner')?403:400)
})
test('invalid contacts, duplicate accounts and stale edits fail; audit failure rolls back a subscription transaction',async t=>{
  const s=await customerFixture();t.after(()=>s.db.close());const a=await s.agent('代理')
  assert.equal((await s.request(root,{key:crypto.randomUUID(),name:'无联系方式'},a.cookie)).status,400)
  assert.equal((await s.request(root,{key:crypto.randomUUID(),name:'错误邮箱',email:'bad'},a.cookie)).status,400)
  const id=await client(s,a.cookie);const aid=await account(s,a.cookie,id)
  assert.equal((await s.request(root+'/'+id+'/accounts',{key:crypto.randomUUID(),account:'account@example.test'},a.cookie)).status,409)
  await success(await s.request(root+'/accounts/'+aid+'/edit',{version:0,account:'changed@example.test',note:'已修改'},a.cookie))
  assert.equal((await s.request(root+'/accounts/'+aid+'/edit',{version:0,account:'stale'},a.cookie)).status,409)
  s.db.exec("CREATE TRIGGER fail_crm_audit BEFORE INSERT ON admin_audit WHEN NEW.action='customer-subscription' BEGIN SELECT RAISE(ABORT,'fixture-audit-failure'); END")
  const result=await s.request(root+'/accounts/'+aid+'/subscriptions',{key:crypto.randomUUID(),version:1,plan:'plus',kind:'open',openedAt:Date.now(),paidCents:16900},a.cookie)
  assert.equal(result.status,503);assert.equal(s.db.prepare('SELECT COUNT(*) n FROM crm_subscriptions').get()!.n,0)
  const row=s.db.prepare('SELECT * FROM crm_accounts WHERE id=?').get(aid)!;assert.equal(row.current_subscription_id,null);assert.equal(row.version,1)
  s.db.exec('DROP TRIGGER fail_crm_audit')
})
