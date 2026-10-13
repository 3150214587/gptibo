import { announcementContentLimit, announcementTitleLimit, emptyAnnouncement, type AnnouncementSettings } from '../shared/announcements.ts'
import { ApiError, body, json, type Env } from './platform.ts'
import type { Partner } from './partner-auth.ts'

export async function announcementSettings(env: Env, partnerId: string): Promise<AnnouncementSettings> {
  const row = await env.INTEREST_DB.prepare('SELECT enabled,placement,title,content,version FROM partner_announcements WHERE partner_id=?').bind(partnerId).first<Omit<AnnouncementSettings, 'enabled'> & { enabled: number }>()
  return row ? { ...row, enabled: row.enabled === 1 } : { ...emptyAnnouncement }
}

export async function publicAnnouncement(env: Env, partnerId: string) {
  const settings = await announcementSettings(env, partnerId)
  return settings.enabled ? settings : null
}

function announcementText(value: unknown, limit: number, label: string) {
  if (typeof value !== 'string' || value.length > limit || Array.from(value).some(character => {
    const code = character.charCodeAt(0)
    return (code < 32 && ![9, 10, 13].includes(code)) || code === 127
  })) throw new ApiError(`${label}最多 ${limit} 字，请填写有效文本。`)
  return value.trim()
}

export async function announcementRoute(request: Request, env: Env, partner: Partner) {
  if (request.method === 'GET') return json(await announcementSettings(env, partner.id))
  if (request.method !== 'POST') throw new ApiError('请通过公告表单保存。', 405)
  const data = await body(request, 32768)
  if (typeof data.enabled !== 'boolean' || typeof data.placement !== 'string' || !['home', 'product', 'both'].includes(data.placement)) throw new ApiError('请选择有效的公告状态与弹出位置。')
  const title = announcementText(data.title, announcementTitleLimit, '公告标题')
  const content = announcementText(data.content, announcementContentLimit, '公告内容')
  if (data.enabled && (!title || !content)) throw new ApiError('启用公告前，请填写标题和内容。')
  if (!Number.isSafeInteger(data.version) || Number(data.version) < 0) throw new ApiError('公告版本无效，请刷新重试。')
  const current = await announcementSettings(env, partner.id)
  if (current.version !== data.version) throw new ApiError('公告已更新，请刷新后再保存。', 409)
  const id = crypto.randomUUID(); const now = Date.now()
  await env.INTEREST_DB.batch([
    env.INTEREST_DB.prepare(`INSERT INTO partner_announcements(partner_id,enabled,placement,title,content,version,change_id,updated_at)
      SELECT id,?,?,?,?,1,?,? FROM partners WHERE id=? AND active=1 AND auth_version=?
      ON CONFLICT(partner_id) DO UPDATE SET enabled=excluded.enabled,placement=excluded.placement,title=excluded.title,content=excluded.content,version=partner_announcements.version+1,change_id=excluded.change_id,updated_at=excluded.updated_at WHERE partner_announcements.version=?`)
      .bind(Number(data.enabled), data.placement as string, title, content, id, now, partner.id, partner.auth_version, current.version),
    env.INTEREST_DB.prepare("INSERT INTO admin_audit(id,target_id,action,note,created_at) SELECT ?,partner_id,'partner-announcement',?,? FROM partner_announcements WHERE partner_id=? AND change_id=?")
      .bind(id, data.enabled ? '代理保存并启用站点公告' : '代理保存并关闭站点公告', now, partner.id, id),
  ])
  if (!await env.INTEREST_DB.prepare('SELECT id FROM admin_audit WHERE id=?').bind(id).first()) throw new ApiError('账号或公告已更新，请刷新后重试。', 409)
  return json(await announcementSettings(env, partner.id))
}
