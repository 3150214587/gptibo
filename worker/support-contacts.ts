import { emptySupportContacts, type SupportContacts, type SupportContactsSettings } from '../shared/support-contacts.ts'
import { ApiError, body, json, type Env } from './platform.ts'
import { emailValue, type Partner } from './partner-auth.ts'

export async function supportContactsSettings(env: Env, owner = 'platform'): Promise<SupportContactsSettings> {
  const row = await env.INTEREST_DB.prepare('SELECT wechat,qq,email,version FROM support_contacts WHERE owner=?').bind(owner).first<SupportContacts & { version: number }>()
  return { contacts: row ? { wechat: row.wechat, qq: row.qq, email: row.email } : { ...emptySupportContacts }, version: row?.version ?? 0 }
}

function contactText(value: unknown, max: number, name: string) {
  if (typeof value !== 'string' || value.length > max || Array.from(value).some(character => character.charCodeAt(0) < 32 || character.charCodeAt(0) === 127)) throw new ApiError(`请填写有效的${name}，或留空。`)
  return value.trim()
}

export async function supportContactsRoute(request: Request, env: Env, partner?: Partner) {
  const owner = partner?.id ?? 'platform'
  if (request.method === 'GET') return json(await supportContactsSettings(env, owner))
  if (request.method !== 'POST') throw new ApiError('请通过联系方式表单保存。', 405)
  const data = await body(request)
  const wechat = contactText(data.wechat, 64, '微信号')
  const qq = contactText(data.qq, 20, 'QQ 号')
  const emailText = contactText(data.email, 254, '邮箱')
  const email = emailText ? emailValue(emailText) : ''
  if (qq && !/^\d{5,20}$/.test(qq)) throw new ApiError('QQ 号需为 5–20 位数字，或留空。')
  if (!Number.isSafeInteger(data.version) || Number(data.version) < 0) throw new ApiError('联系方式版本无效，请刷新重试。')
  const current = await supportContactsSettings(env, owner)
  if (data.version !== current.version) throw new ApiError('联系方式已更新，请刷新后再保存。', 409)
  const id = crypto.randomUUID(); const now = Date.now()
  const columns = 'owner,partner_id,wechat,qq,email,version,change_id,updated_at'
  const conflict = ' ON CONFLICT(owner) DO UPDATE SET wechat=excluded.wechat,qq=excluded.qq,email=excluded.email,version=support_contacts.version+1,change_id=excluded.change_id,updated_at=excluded.updated_at WHERE support_contacts.version=?'
  const write = partner
    ? env.INTEREST_DB.prepare(`INSERT INTO support_contacts(${columns}) SELECT ?,id,?,?,?,1,?,? FROM partners WHERE id=? AND active=1 AND auth_version=?${conflict}`).bind(owner, wechat, qq, email, id, now, partner.id, partner.auth_version, current.version)
    : env.INTEREST_DB.prepare(`INSERT INTO support_contacts(${columns}) VALUES (?,NULL,?,?,?,1,?,?)${conflict}`).bind(owner, wechat, qq, email, id, now, current.version)
  await env.INTEREST_DB.batch([
    write,
    env.INTEREST_DB.prepare("INSERT INTO admin_audit(id,target_id,action,note,created_at) SELECT ?,owner,'support-contacts',?,? FROM support_contacts WHERE owner=? AND change_id=?").bind(id, partner ? '代理修改商店联系方式' : env.ADMIN_USERNAME + '：修改平台联系方式', now, owner, id),
  ])
  if (!await env.INTEREST_DB.prepare('SELECT id FROM admin_audit WHERE id=?').bind(id).first()) throw new ApiError('账号或联系方式已更新，请刷新后再保存。', 409)
  return json(await supportContactsSettings(env, owner))
}
