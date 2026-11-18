import test from 'node:test'
import assert from 'node:assert/strict'
import { ExclusiveAction } from '../src/interaction/exclusive-action.ts'

test('one operation spans network completion and the confirmation dialog', async () => {
  const gate = new ExclusiveAction()
  let finishNetwork!: () => void
  let closeDialog!: () => void
  const network = new Promise<void>(resolve => { finishNetwork = resolve })
  const dialog = new Promise<void>(resolve => { closeDialog = resolve })
  let writes = 0
  const first = gate.run(async () => { writes++; await network; await dialog })
  assert.equal(await gate.run(async () => { writes++ }), false)
  finishNetwork()
  await Promise.resolve()
  assert.equal(await gate.run(async () => { writes++ }), false)
  closeDialog()
  assert.equal(await first, true)
  assert.equal(writes, 1)
  assert.equal(await gate.run(async () => { writes++ }), true)
  assert.equal(writes, 2)
})

test('rejected work releases the lock without repeating the write', async () => {
  const gate = new ExclusiveAction()
  let writes = 0
  await assert.rejects(gate.run(async () => { writes++; throw new Error('response lost') }), /response lost/)
  assert.equal(writes, 1)
  assert.equal(await gate.run(async () => { writes++ }), true)
  assert.equal(writes, 2)
})
