import test from 'node:test'
import assert from 'node:assert/strict'
import { requestLane } from '../src/lib/request-lane.ts'

test('a user action during a payment poll waits and runs once; extra polls yield', async () => {
  const lane = requestLane(); const calls: string[] = []
  let release!: () => void
  const blocked = new Promise<void>(resolve => { release = resolve })
  const poll = lane.run(async () => { calls.push('poll'); await blocked }, true)
  const click = lane.run(async () => { calls.push('query'); return 'pending' })
  const skipped = await lane.run(async () => { calls.push('extra-poll') }, true)
  assert.equal(skipped, undefined); assert.deepEqual(calls, ['poll'])
  release(); await poll
  assert.equal(await click, 'pending'); assert.deepEqual(calls, ['poll', 'query'])
})

test('failed background requests release queued user actions without parallel execution', async () => {
  const lane = requestLane(); const calls: string[] = []
  const failed = lane.run(async () => { calls.push('failed'); throw new Error('offline') }, true)
  const recover = lane.run(async () => { calls.push('retry'); return 'paid' })
  await assert.rejects(failed, /offline/)
  assert.equal(await recover, 'paid'); assert.deepEqual(calls, ['failed', 'retry'])
})
