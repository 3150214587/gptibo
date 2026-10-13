import test from 'node:test'
import assert from 'node:assert/strict'
import { announcementApplies, announcementDay, announcementHiddenToday, emptyAnnouncement, hideAnnouncementToday } from '../shared/announcements.ts'

test('announcements appear only on configured storefront pages and require enabled content', () => {
  for (const placement of ['home', 'product', 'both'] as const) {
    const announcement = { ...emptyAnnouncement, enabled: true, content: '通知', placement }
    assert.equal(announcementApplies(announcement, 'store'), placement !== 'product')
    assert.equal(announcementApplies(announcement, 'product/chatgpt'), placement !== 'home')
    assert.equal(announcementApplies(announcement, 'product/codex-sms'), placement !== 'home')
    for (const page of ['orders', 'admin', 'join']) assert.equal(announcementApplies(announcement, page), false)
    assert.equal(announcementApplies({ ...announcement, enabled: false }, 'store'), false)
    assert.equal(announcementApplies({ ...announcement, content: ' \n ' }, 'product/chatgpt'), false)
  }
})

test('today dismissal survives reload, isolates shops and expires at local midnight', () => {
  const values = new Map<string, string>()
  const storage = { getItem: (key: string) => values.get(key) ?? null, setItem: (key: string, value: string) => { values.set(key, value) } }
  const today = new Date(2026, 9, 1, 23, 59, 59); const tomorrow = new Date(2026, 9, 2, 0, 0, 0)
  assert.equal(announcementDay(today), '2026-10-01')
  assert.equal(announcementHiddenToday(storage, 'ABCDEF', today), false)
  hideAnnouncementToday(storage, 'ABCDEF', today)
  assert.equal(announcementHiddenToday({ ...storage }, 'ABCDEF', today), true)
  assert.equal(announcementHiddenToday(storage, 'GHIJKL', today), false)
  assert.equal(announcementHiddenToday(storage, 'ABCDEF', tomorrow), false)
  assert.equal(announcementHiddenToday(storage, 'ABCDEF', new Date(2027, 9, 1)), false)
  values.set('ymx-announcement-hidden:ABCDEF', 'invalid')
  assert.equal(announcementHiddenToday(storage, 'ABCDEF', today), false)
})

test('blocked browser storage never prevents reading or closing an announcement', () => {
  const storage = { getItem: () => { throw new Error('blocked') }, setItem: () => { throw new Error('blocked') } }
  assert.equal(announcementHiddenToday(storage, 'ABCDEF'), false)
  assert.doesNotThrow(() => hideAnnouncementToday(storage, 'ABCDEF'))
})
