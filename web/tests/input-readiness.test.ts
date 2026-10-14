import assert from 'node:assert/strict'
import { test } from 'node:test'
import { InputReadiness, dispatchClassroom } from '../src/ws/input-readiness.ts'

const context = { epoch: 'room-1', turn: 'question-1' }
test('closed transport before onclose rejects classroom input but preserves independent control fallback', () => {
  let rest = 0; let socketState = 'CLOSED'
  const send = () => socketState === 'OPEN'
  const fallback = () => { rest++ }
  dispatchClassroom(send, fallback, true); assert.equal(rest, 0)
  dispatchClassroom(send, fallback, false); assert.equal(rest, 1)
  socketState = 'OPEN'; dispatchClassroom(send, fallback, true); assert.equal(rest, 1)
})
test('snapshot and online alone cannot authorize input; business action can', () => {
  const gate = new InputReadiness()
  assert.equal(gate.answer(context, true, '猫'), false)
  assert.equal(gate.grant(context, 'wait_answer'), true)
  assert.equal(gate.answer(context, true, '猫'), true)
  assert.equal(gate.answer(context, false, '猫'), false)
  assert.equal(gate.answer(context, true, '  '), false)
  assert.equal(gate.answer(context, true, '不会'), true)
})
test('same business turn replay preserves draft and capture identity', () => {
  const gate = new InputReadiness(); gate.grant(context, 'wait_answer'); const capture = gate.ready
  assert.equal(gate.grant({ ...context }, 'wait_answer'), false)
  assert.equal(gate.ready, capture)
  assert.equal(gate.accepts(context, 'wait_answer', true, capture), true)
})
test('same word new turn or room needs a new draft and rejects old capture', () => {
  const gate = new InputReadiness(); gate.grant(context, 'wait_answer'); const capture = gate.ready
  const next = { ...context, turn: 'question-2' }
  assert.equal(gate.grant(next, 'wait_answer'), true)
  assert.equal(gate.accepts(next, 'wait_answer', true, capture), false)
  assert.equal(gate.answer(context, true, '猫'), false)
  assert.equal(gate.grant({ ...next, epoch: 'room-2' }, 'wait_answer'), true)
})
test('close or phase invalidation preserves draft but invalidates callbacks even after same-turn regrant', () => {
  const gate = new InputReadiness(); gate.grant(context, 'wait_answer'); const capture = gate.ready
  gate.revoke(); assert.equal(gate.answer(context, true, '猫'), false)
  assert.equal(gate.grant(context, 'wait_answer'), false)
  assert.equal(gate.accepts(context, 'wait_answer', true, capture), false)
  gate.reset(); assert.equal(gate.grant(context, 'wait_answer'), true)
})
test('repeat authority does not authorize typed quiz answers or a different phase', () => {
  const gate = new InputReadiness(); gate.grant(context, 'repeating')
  assert.equal(gate.accepts(context, 'repeating', true), true)
  assert.equal(gate.answer(context, true, '猫'), false)
  assert.equal(gate.accepts(context, 'wait_answer', true), false)
  gate.revoke(); gate.grant({ epoch: '', turn: '' }, 'repeating'); assert.equal(gate.ready, null)
})
