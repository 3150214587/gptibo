const startsAt = Date.parse('2026-10-08T07:00:00+08:00')
export const maintenanceEndsAt = Date.parse('2026-10-08T09:00:00+08:00')

export function maintenanceNotice(now = Date.now()) {
  if (now >= maintenanceEndsAt) return null
  return now < startsAt
    ? '系统将于 10 月 8 日 07:00–09:00（北京时间）维护，有业务需求请尽快办理。'
    : '系统正在维护，预计 10 月 8 日 09:00（北京时间）结束，请稍后办理业务。'
}
