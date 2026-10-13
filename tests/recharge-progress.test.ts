import test from 'node:test'
import assert from 'node:assert/strict'
import { RECHARGE_STAGES, rechargeStage } from '../shared/recharge-progress.ts'

test('progress accepts only public business stages and treats upgrading as unfinished', () => {
  assert.equal(rechargeStage('logging_in'), 'logging_in')
  assert.equal(rechargeStage('upgrading'), 'upgrading')
  assert.match(RECHARGE_STAGES.upgrading.description, /仍在处理中/)
  for (const input of [null, undefined, 'PRIVATE-TOKEN', 'toString', '__proto__', 1, {}]) assert.equal(rechargeStage(input), null)
})
