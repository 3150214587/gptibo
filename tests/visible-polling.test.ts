import test from 'node:test'
import assert from 'node:assert/strict'
import { visiblePolling } from '../src/lib/visible-polling.ts'

const flush = () => new Promise<void>(resolve => setImmediate(resolve))
class Page extends EventTarget { hidden = false }

test('slow polling never overlaps; errors retry and closing stops scheduled work', async t => {
  t.mock.timers.enable({ apis: ['setTimeout'] })
  const doc = new Page(), win = new EventTarget()
  let calls = 0
  let finish!: () => void
  const stop = visiblePolling(async () => {
    calls++
    if (calls === 1) await new Promise<void>(resolve => { finish = resolve })
    if (calls === 2) throw Error('temporary upstream failure')
  }, 5000, doc, win)
  t.after(stop)
  assert.equal(calls, 1)
  t.mock.timers.tick(30000)
  win.dispatchEvent(new Event('focus'))
  assert.equal(calls, 1)
  finish(); await flush()
  t.mock.timers.tick(5000); await flush()
  assert.equal(calls, 2)
  t.mock.timers.tick(5000); await flush()
  assert.equal(calls, 3)
  stop()
  t.mock.timers.tick(30000)
  win.dispatchEvent(new Event('focus'))
  assert.equal(calls, 3)
})

test('hidden checkout pauses and resumes immediately on visibility or focus', async t => {
  t.mock.timers.enable({ apis: ['setTimeout'] })
  const doc = new Page(), win = new EventTarget()
  doc.hidden = true
  let calls = 0
  const stop = visiblePolling(async () => { calls++ }, 5000, doc, win)
  t.after(stop)
  t.mock.timers.tick(30000)
  assert.equal(calls, 0)
  doc.hidden = false
  doc.dispatchEvent(new Event('visibilitychange')); await flush()
  assert.equal(calls, 1)
  doc.hidden = true
  t.mock.timers.tick(5000); await flush()
  assert.equal(calls, 1)
  doc.hidden = false
  win.dispatchEvent(new Event('focus')); await flush()
  assert.equal(calls, 2)
})

test('closing during an outstanding response never restarts the poll', async t => {
  t.mock.timers.enable({ apis: ['setTimeout'] })
  const doc = new Page(), win = new EventTarget()
  let calls = 0
  let finish!: () => void
  const stop = visiblePolling(async () => { calls++; await new Promise<void>(resolve => { finish = resolve }) }, 5000, doc, win)
  stop(); finish(); await flush()
  t.mock.timers.tick(30000)
  doc.dispatchEvent(new Event('visibilitychange'))
  assert.equal(calls, 1)
})
