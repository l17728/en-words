import test from 'node:test'
import assert from 'node:assert/strict'
import { ConfigReconciliation } from '../src/interaction/config-reconciliation.ts'

test('unknown delivery remains locked across other targets and reconciles revisions plus values', () => {
  const gate = new ConfigReconciliation()
  const before = { value: [1, 2, 3], revision: 1 }
  gate.begin('student-a', before)
  assert.equal(gate.required('student-a'), true)
  assert.equal(gate.reconcile('student-b', before), null)
  assert.equal(gate.required('student-a'), true)
  assert.equal(gate.reconcile('student-a', { value: [1, 2, 3], revision: 3 }), 'changed')
  assert.equal(gate.required('student-a'), false)
  gate.begin('student-a', before)
  assert.equal(gate.reconcile('student-a', before), 'unchanged')
  gate.begin('student-a', before)
  gate.rejected('student-a')
  assert.equal(gate.required('student-a'), false)
})
