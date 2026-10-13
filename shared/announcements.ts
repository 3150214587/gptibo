export type AnnouncementPlacement = 'home' | 'product' | 'both'
export type Announcement = { enabled: boolean; placement: AnnouncementPlacement; title: string; content: string }
export type AnnouncementSettings = Announcement & { version: number }
export const emptyAnnouncement: AnnouncementSettings = { enabled: false, placement: 'home', title: '站点公告', content: '', version: 0 }
export const announcementTitleLimit = 80
export const announcementContentLimit = 5000

export function announcementApplies(announcement: Announcement, page: string) {
  if (!announcement.enabled || !announcement.content.trim()) return false
  return page === 'store' ? announcement.placement !== 'product'
    : page.startsWith('product/') && announcement.placement !== 'home'
}

export function announcementDay(now = new Date()) {
  return `${now.getFullYear()}-${String(now.getMonth() + 1).padStart(2, '0')}-${String(now.getDate()).padStart(2, '0')}`
}

export type AnnouncementStorage = { getItem: (key: string) => string | null; setItem: (key: string, value: string) => void }
const storageKey = (shop: string) => 'ymx-announcement-hidden:' + encodeURIComponent(shop)
export function announcementHiddenToday(storage: AnnouncementStorage, shop: string, now = new Date()) {
  try { return storage.getItem(storageKey(shop)) === announcementDay(now) } catch { return false }
}
export function hideAnnouncementToday(storage: AnnouncementStorage, shop: string, now = new Date()) {
  try { storage.setItem(storageKey(shop), announcementDay(now)) } catch { /* Closing still works when storage is unavailable. */ }
}
