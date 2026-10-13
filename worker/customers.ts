import { ApiError, body, json, type Env, type Value } from './platform.ts'
import { requirePartner } from './partner-auth.ts'
import { MEMBERSHIP_PLANS,monthAfter,type Customer,type CustomerAccount,type CustomerSubscription } from '../shared/customers.ts'

const uuid = (value: unknown): value is string => typeof value==='string' && /^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$/.test(value)
function textValue(value:unknown,max:number,required=false) {
  if (value===undefined && !required) return ''
  if (typeof value!=='string' || value.trim().length>max || (required && !value.trim())) throw new ApiError('资料格式无效或文字过长。')
  return value.trim()
}
export async function customerRoutes(request:Request,env:Env,url:URL,admin=false) {
  const db=env.INTEREST_DB; const partner=admin?null:await requirePartner(request,env)
  const root=admin?'/api/admin/customers':'/api/partner/customers'; const path=url.pathname.slice(root.length)
  const scope=partner?'c.partner_id=?':'1=1'; const ownerValues:Value[]=partner?[partner.id]:[]
  async function customer(id:string) {
    const row=await db.prepare(`SELECT c.*,p.name partner_name FROM crm_customers c LEFT JOIN partners p ON p.id=c.partner_id WHERE c.id=? AND ${scope}`).bind(id,...ownerValues).first<Customer>()
    if (!row) throw new ApiError('未找到客户。',404)
    return row
  }
  async function account(id:string) {
    const row=await db.prepare(`SELECT a.*,s.plan,s.expires_at,s.opened_at FROM crm_accounts a JOIN crm_customers c ON c.id=a.customer_id LEFT JOIN crm_subscriptions s ON s.id=a.current_subscription_id WHERE a.id=? AND ${scope}`).bind(id,...ownerValues).first<CustomerAccount>()
    if (!row) throw new ApiError('未找到客户账号。',404)
    return row
  }
  const now=Date.now(); const soon=now+7*86400000
  if ((!path || path==='/export') && request.method==='GET') {
    const q=url.searchParams.get('q')?.trim()??''; const plan=url.searchParams.get('plan')??'all'; const expiry=url.searchParams.get('expiry')??'all'; const owner=url.searchParams.get('partner')??'all'; const page=Number(url.searchParams.get('page')??1)
    if (q.length>100 || !['all',...MEMBERSHIP_PLANS].includes(plan) || !['all','active','soon','expired','none'].includes(expiry) || !Number.isInteger(page) || page<1 || page>10000) throw new ApiError('筛选条件无效。')
    if (!admin && owner!=='all') throw new ApiError('不能筛选其他代理的客户。',403)
    const clauses=[scope]; const values=[...ownerValues]
    if (owner!=='all') { if(owner==='direct') clauses.push('c.partner_id IS NULL'); else {if(!uuid(owner)) throw new ApiError('代理无效。');clauses.push('c.partner_id=?');values.push(owner)} }
    if(q) {clauses.push('(instr(lower(c.name||c.wechat||c.qq||c.email||c.note||c.source),lower(?))>0 OR EXISTS(SELECT 1 FROM crm_accounts a WHERE a.customer_id=c.id AND instr(lower(a.account||a.note),lower(?))>0))');values.push(q,q)}
    if(plan!=='all' || expiry!=='all') {
      const parts=['a.customer_id=c.id']
      if(plan!=='all'){parts.push('s.plan=?');values.push(plan)}
      if(expiry==='active'){parts.push('s.expires_at>?');values.push(now)}
      if(expiry==='soon'){parts.push('s.expires_at>? AND s.expires_at<=?');values.push(now,soon)}
      if(expiry==='expired'){parts.push('s.expires_at<=?');values.push(now)}
      if(expiry==='none')parts.push('s.id IS NULL')
      clauses.push(`EXISTS(SELECT 1 FROM crm_accounts a LEFT JOIN crm_subscriptions s ON s.id=a.current_subscription_id WHERE ${parts.join(' AND ')})`)
      if(expiry==='none'&&plan==='all')clauses[clauses.length-1]=`(${clauses.at(-1)} OR NOT EXISTS(SELECT 1 FROM crm_accounts a WHERE a.customer_id=c.id))`
    }
    const filtered=`SELECT c.*,p.name partner_name FROM crm_customers c LEFT JOIN partners p ON p.id=c.partner_id WHERE ${clauses.join(' AND ')}`
    if(path==='/export') {
      const count=await db.prepare(`SELECT COUNT(*) n,(SELECT COUNT(*) FROM crm_accounts a JOIN (${filtered}) f ON f.id=a.customer_id) accounts,(SELECT COUNT(*) FROM crm_subscriptions s JOIN crm_accounts a ON a.id=s.account_id JOIN (${filtered}) f ON f.id=a.customer_id) records FROM (${filtered})`).bind(...values,...values,...values).first<{n:number;accounts:number;records:number}>()
      if((count?.n??0)>5000||(count?.accounts??0)>10000||(count?.records??0)>50000)throw new ApiError('每次最多导出 5000 位客户、10000 个账号及 50000 条历史记录，请缩小筛选范围。',409)
      const exported=await db.batch([
        db.prepare(filtered+' ORDER BY c.created_at DESC,c.id LIMIT 5001').bind(...values),
        db.prepare(`SELECT a.*,s.plan_name,s.opened_at,s.expires_at,s.paid_cents FROM crm_accounts a JOIN (${filtered}) f ON f.id=a.customer_id LEFT JOIN crm_subscriptions s ON s.id=a.current_subscription_id ORDER BY a.created_at,a.id LIMIT 10001`).bind(...values),
        db.prepare(`SELECT s.*,a.account,a.customer_id FROM crm_subscriptions s JOIN crm_accounts a ON a.id=s.account_id JOIN (${filtered}) f ON f.id=a.customer_id ORDER BY s.created_at,s.id LIMIT 50001`).bind(...values),
      ]) as {results:unknown[]}[]
      const [customers,accounts,subscriptions]=exported.map(r=>r.results)
      if(customers.length>5000||accounts.length>10000||subscriptions.length>50000)throw new ApiError('导出记录已增加，请缩小筛选范围后重试。',409)
      return json({customers,accounts,subscriptions,asOf:now})
    }
    const result=await db.batch([
      db.prepare(`SELECT f.*,(SELECT GROUP_CONCAT(DISTINCT s.plan_name) FROM crm_accounts a JOIN crm_subscriptions s ON s.id=a.current_subscription_id WHERE a.customer_id=f.id) plan_names,(SELECT MIN(s.expires_at) FROM crm_accounts a JOIN crm_subscriptions s ON s.id=a.current_subscription_id WHERE a.customer_id=f.id) next_expiry,(SELECT COUNT(*) FROM crm_accounts a WHERE a.customer_id=f.id) account_count,(SELECT COALESCE(SUM(s.paid_cents),0) FROM crm_subscriptions s JOIN crm_accounts a ON a.id=s.account_id WHERE a.customer_id=f.id) paid_cents,(SELECT COUNT(*) FROM crm_accounts a JOIN crm_subscriptions s ON s.id=a.current_subscription_id WHERE a.customer_id=f.id AND s.expires_at>? AND s.expires_at<=?) soon_count,(SELECT COUNT(*) FROM crm_accounts a JOIN crm_subscriptions s ON s.id=a.current_subscription_id WHERE a.customer_id=f.id AND s.expires_at<=?) expired_count FROM (${filtered}) f ORDER BY f.created_at DESC,f.id LIMIT 20 OFFSET ?`).bind(now,soon,now,...values,(page-1)*20),
      db.prepare(`SELECT COUNT(*) total FROM (${filtered})`).bind(...values),
      db.prepare(`SELECT COUNT(*) customers,(SELECT COUNT(*) FROM crm_accounts a JOIN (${filtered}) f ON f.id=a.customer_id) accounts,(SELECT COUNT(*) FROM crm_accounts a JOIN (${filtered}) f ON f.id=a.customer_id JOIN crm_subscriptions s ON s.id=a.current_subscription_id WHERE s.expires_at>? AND s.expires_at<=?) soon,(SELECT COUNT(*) FROM crm_accounts a JOIN (${filtered}) f ON f.id=a.customer_id JOIN crm_subscriptions s ON s.id=a.current_subscription_id WHERE s.expires_at<=?) expired,(SELECT COALESCE(SUM(s.paid_cents),0) FROM crm_subscriptions s JOIN crm_accounts a ON a.id=s.account_id JOIN (${filtered}) f ON f.id=a.customer_id) paid_cents FROM (${filtered})`).bind(...values,...values,now,soon,...values,now,...values,...values),
    ]) as {results:unknown[]}[]
    const plans=(await db.prepare("SELECT id,name FROM catalog_plans WHERE product_id='chatgpt' AND id IN ('plus','pro-5x','pro-20x','pro-25x') ORDER BY rowid").all()).results
    const partners=admin?(await db.prepare('SELECT id,name FROM partners ORDER BY name').all()).results:[]
    return json({customers:result[0].results,total:(result[1].results[0] as {total:number}).total,stats:result[2].results[0],plans,partners,page,asOf:now})
  }
  if(!path && request.method==='POST') {
    const data=await body(request,16384); if(!uuid(data.key))throw new ApiError('请求编号无效。')
    const name=textValue(data.name,100,true); const wechat=textValue(data.wechat,100); const qq=textValue(data.qq,30); const email=textValue(data.email,254).toLowerCase();const note=textValue(data.note,2000);const source=textValue(data.source,100)
    if(!wechat&&!qq&&!email)throw new ApiError('至少填写微信、QQ、邮箱中的一种。')
    if(email&&!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email))throw new ApiError('邮箱格式无效。')
    let owner:string|null=partner?.id??null
    if(admin&&data.partnerId!==undefined&&data.partnerId!==null&&data.partnerId!=='direct'){if(!uuid(data.partnerId)||!await db.prepare('SELECT id FROM partners WHERE id=?').bind(data.partnerId).first())throw new ApiError('代理不存在。');owner=data.partnerId}
    if(!admin&&data.partnerId!==undefined&&data.partnerId!==partner?.id)throw new ApiError('不能为其他代理添加客户。',403)
    await db.batch([
      db.prepare('INSERT INTO crm_customers(id,partner_id,name,wechat,qq,email,note,source,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?) ON CONFLICT DO NOTHING').bind(data.key,owner,name,wechat,qq,email,note,source,now,now),
      db.prepare("INSERT INTO admin_audit(id,action,target_id,note,created_at) SELECT id,'customer-create',id,?,created_at FROM crm_customers WHERE id=? AND partner_id IS ? ON CONFLICT DO NOTHING").bind(admin?'管理员创建客户':'代理创建自己的客户',data.key,owner),
    ])
    const row=await customer(data.key); if(row.partner_id!==owner||row.name!==name||row.email!==email||row.wechat!==wechat||row.qq!==qq||row.note!==note||row.source!==source)throw new ApiError('请求编号已被使用。',409)
    return json({customer:row},201)
  }
  const match=path.match(/^\/([a-f0-9-]{36})(\/edit|\/accounts)?$/)
  if(match) {
    const c=await customer(match[1])
    if(!match[2]&&request.method==='GET') {
      const accounts=(await db.prepare('SELECT a.*,s.plan,s.plan_name,s.opened_at,s.expires_at,s.paid_cents FROM crm_accounts a LEFT JOIN crm_subscriptions s ON s.id=a.current_subscription_id WHERE a.customer_id=? ORDER BY a.created_at').bind(c.id).all()).results
      const subscriptions=(await db.prepare('SELECT s.* FROM crm_subscriptions s JOIN crm_accounts a ON a.id=s.account_id WHERE a.customer_id=? ORDER BY s.created_at DESC,s.id DESC').bind(c.id).all()).results
      return json({customer:c,accounts,subscriptions,asOf:now})
    }
    if(match[2]==='/edit'&&request.method==='POST') {
      const data=await body(request,16384);const name=textValue(data.name,100,true);const wechat=textValue(data.wechat,100);const qq=textValue(data.qq,30);const email=textValue(data.email,254).toLowerCase();const note=textValue(data.note,2000);const source=textValue(data.source,100)
      if(!Number.isSafeInteger(data.version)||(!wechat&&!qq&&!email)||(email&&!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email)))throw new ApiError('请核对资料并至少填写一种联系方式。')
      const token=crypto.randomUUID()
      await db.batch([
        db.prepare("INSERT INTO admin_audit(id,action,target_id,note,created_at) SELECT ?,'customer-edit',id,?,? FROM crm_customers WHERE id=? AND version=?").bind(token,admin?'管理员修改客户资料':'代理修改客户资料',now,c.id,data.version as number),
        db.prepare('UPDATE crm_customers SET name=?,wechat=?,qq=?,email=?,note=?,source=?,version=version+1,updated_at=? WHERE id=? AND EXISTS(SELECT 1 FROM admin_audit WHERE id=?)').bind(name,wechat,qq,email,note,source,now,c.id,token),
      ])
      if(!await db.prepare('SELECT id FROM admin_audit WHERE id=?').bind(token).first())throw new ApiError('资料已更新，请刷新后重试。',409)
      return json({ok:true})
    }
    if(match[2]==='/accounts'&&request.method==='POST') {
      const data=await body(request,16384);if(!uuid(data.key))throw new ApiError('请求编号无效。')
      const value=textValue(data.account,254,true);const note=textValue(data.note,2000)
      await db.batch([
        db.prepare('INSERT INTO crm_accounts(id,customer_id,account,note,created_at,updated_at) VALUES (?,?,?,?,?,?) ON CONFLICT DO NOTHING').bind(data.key,c.id,value,note,now,now),
        db.prepare("INSERT INTO admin_audit(id,action,target_id,note,created_at) SELECT id,'customer-account-create',id,?,created_at FROM crm_accounts WHERE id=? AND customer_id=? ON CONFLICT DO NOTHING").bind(admin?'管理员新增客户账号':'代理新增客户账号',data.key,c.id),
      ])
      const inserted=await db.prepare('SELECT id FROM crm_accounts WHERE id=? AND customer_id=?').bind(data.key,c.id).first()
      if(!inserted)throw new ApiError('该客户已有同名账号，请打开现有账号。',409)
      const row=await account(data.key);if(row.customer_id!==c.id||row.account!==value||row.note!==note)throw new ApiError('账号已存在或请求编号已使用。',409)
      return json({account:row},201)
    }
  }
  const accountMatch=path.match(/^\/accounts\/([a-f0-9-]{36})(\/edit|\/subscriptions)$/)
  if(accountMatch&&request.method==='POST') {
    const a=await account(accountMatch[1]);const data=await body(request,16384)
    if(accountMatch[2]==='/edit') {
      const value=textValue(data.account,254,true);const note=textValue(data.note,2000);if(!Number.isSafeInteger(data.version))throw new ApiError('资料版本无效。')
      const duplicate=await db.prepare('SELECT id FROM crm_accounts WHERE customer_id=? AND account=? AND id<>?').bind(a.customer_id,value,a.id).first();if(duplicate)throw new ApiError('该客户已有同名账号。',409)
      const token=crypto.randomUUID()
      await db.batch([
        db.prepare("INSERT INTO admin_audit(id,action,target_id,note,created_at) SELECT ?,'customer-account-edit',id,?,? FROM crm_accounts WHERE id=? AND version=?").bind(token,admin?'管理员修改客户账号':'代理修改客户账号',now,a.id,data.version as number),
        db.prepare('UPDATE crm_accounts SET account=?,note=?,version=version+1,updated_at=? WHERE id=? AND EXISTS(SELECT 1 FROM admin_audit WHERE id=?)').bind(value,note,now,a.id,token),
      ])
      if(!await db.prepare('SELECT id FROM admin_audit WHERE id=?').bind(token).first())throw new ApiError('账号已更新，请刷新后重试。',409)
      return json({ok:true})
    }
    if(!uuid(data.key)||!Number.isSafeInteger(data.version)||typeof data.plan!=='string'||!MEMBERSHIP_PLANS.includes(data.plan as typeof MEMBERSHIP_PLANS[number])||!['open','renew','upgrade'].includes(String(data.kind))||!Number.isSafeInteger(data.paidCents)||(data.paidCents as number)<0||(data.paidCents as number)>10000000||!Number.isSafeInteger(data.openedAt)||(data.openedAt as number)<946656000000||(data.openedAt as number)>now+86400000)throw new ApiError('请核对套餐、开通时间和付款金额。')
    const note=textValue(data.note,2000);const opened=data.openedAt as number;const expires=monthAfter(opened)
    const existing=await db.prepare('SELECT * FROM crm_subscriptions WHERE id=? AND account_id=?').bind(data.key,a.id).first<CustomerSubscription>()
    if(existing){if(existing.plan!==data.plan||existing.paid_cents!==data.paidCents||existing.opened_at!==opened||existing.kind!==data.kind||existing.note!==note)throw new ApiError('请求编号已被使用。',409);return json({subscription:existing})}
    if(a.current_subscription_id) {
      if(data.kind==='open')throw new ApiError('该账号已有开通记录，请选择续费或升级。',409)
      if(data.kind==='renew'&&(now<a.expires_at!||opened<a.expires_at!))throw new ApiError('会员未到期，不支持提前续费。',409)
      if(data.kind==='upgrade'&&MEMBERSHIP_PLANS.indexOf(data.plan as typeof MEMBERSHIP_PLANS[number])<=MEMBERSHIP_PLANS.indexOf(a.plan as typeof MEMBERSHIP_PLANS[number]))throw new ApiError('升级需要选择更高档的套餐。',409)
      if(opened<a.opened_at!)throw new ApiError('新开通时间不能早于上一条记录。')
    }else if(data.kind!=='open')throw new ApiError('首次请录入开通记录。',409)
    const plan=await db.prepare('SELECT name FROM catalog_plans WHERE id=?').bind(data.plan).first<{name:string}>();if(!plan)throw new ApiError('套餐不存在。')
    await db.batch([
      db.prepare('INSERT INTO crm_subscriptions(id,account_id,previous_id,plan,plan_name,kind,paid_cents,opened_at,expires_at,note,created_at) SELECT ?,id,current_subscription_id,?,?,?,?,?,?,?,? FROM crm_accounts WHERE id=? AND version=? ON CONFLICT DO NOTHING').bind(data.key,data.plan,plan.name,String(data.kind),data.paidCents as number,opened,expires,note,now,a.id,data.version as number),
      db.prepare('UPDATE crm_accounts SET current_subscription_id=?,version=version+1,updated_at=? WHERE id=? AND version=? AND EXISTS(SELECT 1 FROM crm_subscriptions WHERE id=? AND account_id=?)').bind(data.key,now,a.id,data.version as number,data.key,a.id),
      db.prepare("INSERT INTO admin_audit(id,action,target_id,note,created_at) SELECT id,'customer-subscription',account_id,?,created_at FROM crm_subscriptions WHERE id=? ON CONFLICT DO NOTHING").bind((admin?'管理员':'代理')+'录入 '+String(data.kind)+' 会员记录',data.key),
    ])
    const saved=await db.prepare('SELECT * FROM crm_subscriptions WHERE id=? AND account_id=?').bind(data.key,a.id).first()
    if(!saved)throw new ApiError('账号已更新，请刷新后重试。',409)
    return json({subscription:saved},201)
  }
  throw new ApiError('未找到客户管理接口。',404)
}
