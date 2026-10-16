import assert from 'node:assert/strict'
import { EventEmitter } from 'node:events'
import { test } from 'node:test'
import { stopOwned, cleanupOwned, detachOwnedOutput } from '../e2e/owned-process.ts'

test('already signal-exited owned process does not await a past exit event', async () => {
  const child = Object.assign(new EventEmitter(), { exitCode: null, signalCode: 'SIGTERM', kill() { throw new Error('Already exited child was killed again') } })
  await stopOwned(child as any)
})

test('failed stop still closes stream and platform and preserves failure', async () => {
  const cleaned: string[] = []
  await assert.rejects(cleanupOwned([
    () => { throw new Error('stop failed') },
    () => { cleaned.push('stream') },
    () => { cleaned.push('platform'); throw new Error('platform failed') },
    () => { cleaned.push('evidence') },
  ]))
  assert.deepEqual(cleaned, ['stream', 'platform', 'evidence'])
})


test('real owned Node child can be stopped repeatedly after signal termination', async () => {
  const { spawn } = await import('node:child_process')
  const { once } = await import('node:events')
  const child = spawn(process.execPath, ['-e', 'setInterval(() => {}, 1000)'], { windowsHide: true, stdio: 'ignore' })
  await once(child, 'spawn')
  try {
    await stopOwned(child)
    assert.equal(child.signalCode, 'SIGTERM')
    await stopOwned(child)
    assert.equal(child.listenerCount('exit'), 0)
  } finally { if (child.exitCode === null && child.signalCode === null) child.kill() }
})


test('failed stop detaches and drains late stdout before ending the log destination', async () => {
  const { PassThrough, Writable } = await import('node:stream')
  const stdout = new PassThrough(), stderr = new PassThrough()
  const errors: Error[] = []
  const out = new Writable({ write(_chunk, _encoding, done) { done() } })
  out.on('error', error => errors.push(error))
  stdout.pipe(out, { end: false }); stderr.pipe(out, { end: false })
  const child = { stdout, stderr } as any
  await assert.rejects(cleanupOwned([
    () => { throw new Error('stop timed out; child may still be alive') },
    () => { detachOwnedOutput(child); out.end() },
  ]))
  stdout.write('late stdout'); stderr.write('late stderr')
  await new Promise<void>(done => setImmediate(done))
  stdout.destroy(); stderr.destroy()
  assert.equal(errors.length, 0, 'late output wrote into the ended destination')
  assert.equal(out.listenerCount('unpipe'), 0)
})
