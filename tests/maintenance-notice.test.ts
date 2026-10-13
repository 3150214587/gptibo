import test from 'node:test'
import assert from 'node:assert/strict'
import { maintenanceNotice } from '../src/lib/maintenance-notice.ts'

test('maintenance notice switches at the exact Beijing maintenance boundaries', () => {
  const before = maintenanceNotice(Date.parse('2026-10-07T22:59:59.999Z'))
  assert.match(before!, /10 月 8 日 07:00–09:00（北京时间）/)
  assert.match(before!, /尽快办理/)
  for (const time of ['2026-10-07T23:00:00Z', '2026-10-08T00:59:59.999Z']) {
    const during = maintenanceNotice(Date.parse(time))
    assert.match(during!, /正在维护/)
    assert.doesNotMatch(during!, /尽快办理/)
  }
  for (const time of ['2026-10-08T01:00:00Z', '2026-10-09T01:00:00Z']) {
    assert.equal(maintenanceNotice(Date.parse(time)), null)
  }
})
