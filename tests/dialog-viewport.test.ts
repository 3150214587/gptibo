import test from 'node:test'
import assert from 'node:assert/strict'
import { dialogViewport } from '../src/lib/dialog-viewport.ts'

test('keyboard viewport shrink and panning stay within the visible layout', () => {
  assert.deepEqual(dialogViewport(812, { height: 412, offsetTop: 40, scale: 1 }), { height: 412, top: 40 })
  assert.deepEqual(dialogViewport(812, { height: 412, offsetTop: 600, scale: 1 }), { height: 412, top: 400 })
  assert.deepEqual(dialogViewport(812, { height: 900, offsetTop: -20, scale: 1 }), { height: 812, top: 0 })
})
test('missing or invalid viewport measurements fall back, and pinch zoom is not counteracted', () => {
  for (const viewport of [null, { height: 0, offsetTop: 0, scale: 1 }, { height: NaN, offsetTop: 0, scale: 1 }, { height: 400, offsetTop: 100, scale: 2 }]) assert.deepEqual(dialogViewport(812, viewport), { height: 812, top: 0 })
})
