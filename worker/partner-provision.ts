import { ApiError, type Env, type Value } from './platform.ts'
import { mailConfigured, type Partner } from './partner-auth.ts'
import { generateReferralCode } from '../shared/partner-link.ts'

type TelegramSource = { userId: number; groupId: number; lockToken: string; wechat: string; qq: string }

/** Admin and Telegram use the same account, referral and default pricing transaction. */
export async function provisionPartner(env: Env, email: string, actor: string, telegram?: TelegramSource): Promise<Partner> {
  if (!mailConfigured(env)) throw new ApiError('请先启用邮件服务，开通后需要向代理发送登录邮件。', 503)
  const db = env.INTEREST_DB
  if (await db.prepare('SELECT id FROM partners WHERE email=?').bind(email).first()) throw new ApiError('该邮箱已有代理账号，请在 https://shop.example.invalid/join 使用邮箱验证码登录，或联系管理员。', 409)
  const id = crypto.randomUUID(), now = Date.now()
  const contactsChangeId = crypto.randomUUID()
  const guard = telegram ? 'EXISTS(SELECT 1 FROM telegram_partner_sessions WHERE user_id=? AND lock_token=? AND lock_until>? AND step=\'confirm\' AND expires_at>?)' : '1=1'
  for (let attempt = 0; attempt < 8; attempt++) {
    const slug = generateReferralCode()
    if (await db.prepare('SELECT id FROM partners WHERE slug=?').bind(slug).first()) continue
    try {
      const values: Value[] = telegram ? [telegram.userId, telegram.lockToken, Date.now(), Date.now()] : []
      await db.batch([
        db.prepare(`INSERT INTO partners(id,slug,name,email,created_at,updated_at) SELECT ?,?,'代理商店',?,?,? WHERE ${guard}`).bind(id, slug, email, now, now, ...values),
        db.prepare('INSERT INTO partner_prices(partner_id,plan,cost_cents,price_cents) SELECT ?,c.id,COALESCE(d.cost_cents,c.price_cents),MAX(c.price_cents,COALESCE(d.cost_cents,c.price_cents)) FROM catalog_plans c LEFT JOIN partner_default_prices d ON d.plan=c.id WHERE EXISTS(SELECT 1 FROM partners WHERE id=?)').bind(id, id),
        db.prepare("INSERT INTO admin_audit(id,target_id,action,note,created_at) SELECT ?,id,'partner-create',?,? FROM partners WHERE id=?").bind(crypto.randomUUID(), actor + ': 开通代理 代理商店', now, id),
        ...(telegram ? [
          db.prepare('INSERT INTO telegram_partner_applications(id,user_id,group_id,partner_id,wechat,qq,email,created_at,next_attempt_at) SELECT ?,?,?,id,?,?,?,?,? FROM partners WHERE id=?').bind(crypto.randomUUID(), telegram.userId, telegram.groupId, telegram.wechat, telegram.qq, email, now, now, id),
          db.prepare("INSERT INTO support_contacts(owner,partner_id,wechat,qq,email,version,change_id,updated_at) SELECT id,id,?,?,'',1,?,? FROM partners WHERE id=?").bind(telegram.wechat, telegram.qq, contactsChangeId, now, id),
          db.prepare("INSERT INTO admin_audit(id,target_id,action,note,created_at) SELECT ?,owner,'support-contacts','Telegram 申请自动设置商店微信和 QQ',? FROM support_contacts WHERE owner=? AND change_id=?").bind(contactsChangeId, now, id, contactsChangeId),
          db.prepare("UPDATE telegram_partner_sessions SET step='submitted',wechat='',qq='',email='' WHERE user_id=? AND lock_token=? AND EXISTS(SELECT 1 FROM partners WHERE id=?)").bind(telegram.userId, telegram.lockToken, id),
        ] : []),
      ])
      const p = await db.prepare('SELECT * FROM partners WHERE id=?').bind(id).first<Partner>()
      if (!p) throw new ApiError('申请状态已更新，请重新申请。', 409)
      return p
    } catch (error) {
      if (await db.prepare('SELECT id FROM partners WHERE email=?').bind(email).first()) throw new ApiError('该邮箱已有代理账号，请在 https://shop.example.invalid/join 使用邮箱验证码登录，或联系管理员。', 409)
      if (!await db.prepare('SELECT id FROM partners WHERE slug=?').bind(slug).first()) throw error
    }
  }
  throw new ApiError('专属链接暂时生成失败，请重试。', 503)
}
