import test from 'node:test'
import assert from 'node:assert/strict'
import { DECK_CYCLE_MS, deckFrame, deckTimeline, fanFrame } from '../src/lib/subscription-motion.ts'

test('copy appears before movement, stays throughout return and hides only after the card docks', () => {
  for (let index = 0; index < 4; index++) {
    const next = (index + 1) % 4
    assert.deepEqual(deckFrame(index, 0, 174).map(p => p.visible), [0, 1, 2, 3].map(i => i === index))
    const rest = deckFrame(index, 0, 174)
    for (const time of [3000, 3179, 3180, 3600, 4400, 5000, 5429, 5430, 5589, 5590, DECK_CYCLE_MS - 1]) {
      const { progress, phase } = deckTimeline(time)
      const frame = deckFrame(index, progress, 174, phase)
      assert.equal(frame[next].visible, true)
      assert.equal(frame[index].visible, time < 5590)
      if (time < 3180) assert.deepEqual(frame.map(p => p.position), rest.map(p => p.position), 'text prepares while cards are still')
      if (!frame[index].visible) assert.equal(progress, 1, 'outgoing text cannot disappear before docking')
    }
    const end = deckFrame(index, 1, 174, 'hide'), start = deckFrame(next, 0, 174)
    end.forEach((p, i) => {
      p.position.forEach((v, axis) => assert.ok(Math.abs(v - start[i].position[axis]) < 1e-6))
      assert.equal(p.visible, start[i].visible)
    })
  }
})

test('expanded tiers fit without overlap inside the unchanged deck at narrow and desktop widths', () => {
  for (const width of [190, 220, 254, 294, 443]) {
    const fan = fanFrame(width, 174)
    assert.equal(fan.filter(p => p.visible).length, 4)
    assert.equal(new Set(fan.map(p => p.position.join(','))).size, 4)
    const bounds = fan.map(({ position: [x, y, rotation], scale }) => {
      const angle = rotation * Math.PI / 180
      const halfWidth = (174 * Math.abs(Math.cos(angle)) + 110 * Math.abs(Math.sin(angle))) * scale / 2
      const halfHeight = (110 * Math.abs(Math.cos(angle)) + 174 * Math.abs(Math.sin(angle))) * scale / 2
      assert.ok(scale > 0 && scale <= .7, 'shrink the whole card, including its original typography')
      const left = width / 2 - 8 + x - halfWidth, top = 185 / 2 + y - halfHeight
      const right = left + 2 * halfWidth, bottom = top + 2 * halfHeight
      assert.ok(left >= 12 && right <= width - 12 + 1e-6, 'cards stay inside horizontal gutters')
      assert.ok(top >= 4 - 1e-6 && bottom <= 181 + 1e-6, 'cards stay inside the original 185px height')
      return { left, right, top, bottom }
    })
    assert.ok(bounds[1].left - bounds[0].right >= 12 - 1e-6, 'columns do not overlap')
    assert.ok(bounds[2].top - bounds[0].bottom >= 12 - 1e-6, 'rows do not overlap')
    assert.equal(deckFrame(0, 0, 174)[0].scale, 1, 'the resting stack retains its original size')
  }
})
