import { runInNewContext } from 'node:vm'
import { workletSource } from '../src/audio/duplex/worklet-source.ts'
import assert from 'node:assert/strict'
import { test } from 'node:test'
import { capabilities } from '../src/audio/duplex/capabilities.ts'
import { MediaLease } from '../src/audio/duplex/lease.ts'
import { PCMFrames, RingVad } from '../src/audio/duplex/pcm.ts'
import { PreflightCapture } from '../src/audio/duplex/capture.ts'

test('requested capability is never substituted for false or missing effective settings', () => {
  assert.deepEqual(capabilities({ echoCancellation: false, noiseSuppression: true }).map(v => v.effective), ['disabled', 'enabled', 'unknown'])
  assert.deepEqual(capabilities({}).map(v => v.effective), ['unknown', 'unknown', 'unknown'])
  const supported = capabilities({}, { echoCancellation: true }, { echoCancellation: [true, false] })[0]
  assert.deepEqual(supported.available, [true, false]); assert.equal(supported.effective, 'unknown')
})
test('PCM resampling is independent of input callback boundaries and keeps 20ms frames', () => {
  const input = Float32Array.from({ length: 48000 }, (_, i) => Math.sin(i / 40) / 2)
  const full = new PCMFrames(48000).push(input)
  const split = new PCMFrames(48000); const result: Int16Array[] = []
  for (let i = 0; i < input.length; i += 127) result.push(...split.push(input.slice(i, i + 127)))
  assert.deepEqual(result, full); assert.equal(full.length, 50); assert.equal(full[0].length, 320)
  assert.throws(() => new PCMFrames(NaN))
})
test('20ms short sound survives candidate window and endpoint retains 200ms tail', () => {
  const vad = new RingVad(); const quiet = new Int16Array(320)
  for (let i = 0; i < 30; i++) assert.deepEqual(vad.push(quiet), [])
  assert.equal(vad.push(new Int16Array(320).fill(5000))[0].retainedFrames, 15)
  for (let i = 0; i < 29; i++) assert.deepEqual(vad.push(quiet), [])
  assert.deepEqual(vad.push(quiet)[0], { type: 'endpoint', frames: 11, retainedFrames: 15, short: true })
})
test('continuous sound hard limit explicitly overflows rather than submitting truncated answer', () => {
  const vad = new RingVad(); let last
  for (let i = 0; i < 750; i++) last = vad.push(new Int16Array(320).fill(5000))
  assert.equal(last?.at(-1)?.type, 'overflow')
})
test('lease cancellation revokes before reentrant callback and isolates a new generation', () => {
  const lease = new MediaLease(); let old = 0; let revived = false
  old = lease.begin(() => { revived = lease.current(old) }); lease.cancel()
  assert.equal(revived, false); const next = lease.begin(() => {})
  assert.equal(lease.current(old), false); assert.equal(lease.current(next), true)
})
test('late microphone permission after stop closes tracks without publishing capabilities', async () => {
  let release!: (value: unknown) => void; let stopped = 0; let published = 0
  const original = Object.getOwnPropertyDescriptor(globalThis, 'navigator')
  Object.defineProperty(globalThis, 'navigator', { configurable: true, value: { mediaDevices: { getUserMedia: () => new Promise(resolve => { release = resolve }) } } })
  try {
    const capture = new PreflightCapture(); const pending = capture.start(() => published++, () => published++)
    capture.stop(); release({ getTracks: () => [{ stop: () => stopped++ }] }); await pending
    assert.equal(stopped, 1); assert.equal(published, 0)
  } finally { if (original) Object.defineProperty(globalThis, 'navigator', original) }
})

test('separated short impulses do not accumulate into a continuous 100ms candidate', () => {
  const vad = new RingVad(); const events = []
  for (let i = 0; i < 5; i++) { events.push(...vad.push(new Int16Array(320).fill(5000))); events.push(...vad.push(new Int16Array(320))) }
  assert.equal(events.some(event => event.type === 'speech'), false)
  for (let i = 0; i < 30; i++) events.push(...vad.push(new Int16Array(320)))
  assert.equal(events.some(event => event.type === 'endpoint'), true)
})

test('restarting capture closes prior resources and processorerror closes the current generation', async () => {
  const names = ['navigator', 'AudioContext', 'AudioWorkletNode'] as const
  const saved = names.map(name => [name, Object.getOwnPropertyDescriptor(globalThis, name)] as const)
  let stopped = 0; let closed = 0; const nodes: any[] = []; const events: any[] = []
  const track = () => ({ readyState: 'live', onended: null, stop: () => stopped++, getSettings: () => ({}) })
  Object.defineProperty(globalThis, 'navigator', { configurable: true, value: { mediaDevices: {
    getUserMedia: async () => { const t = track(); return { getTracks: () => [t], getAudioTracks: () => [t] } }, getSupportedConstraints: () => ({}),
  } } })
  class Context { sampleRate = 16000; destination = {}; audioWorklet = { addModule: async () => {} }; createMediaStreamSource() { return { connect() {}, disconnect() {} } }; async resume() {}; async close() { closed++ } }
  class Node { port = { onmessage: null, close() {}, postMessage() {} }; onprocessorerror: any; constructor() { nodes.push(this) }; connect() {}; disconnect() {} }
  Object.defineProperty(globalThis, 'AudioContext', { configurable: true, value: Context })
  Object.defineProperty(globalThis, 'AudioWorkletNode', { configurable: true, value: Node })
  const capture = new PreflightCapture()
  try {
    await capture.start(() => {}, value => events.push(value)); const oldError = nodes[0].onprocessorerror
    await capture.start(() => {}, value => events.push(value)); assert.equal(stopped, 1)
    oldError(); assert.equal(stopped, 1); assert.equal(events.length, 0)
    nodes[1].onprocessorerror(); assert.equal(stopped, 2); assert.equal(closed, 2)
    assert.equal(events.at(-1).failure, 'processor_error')
  } finally { capture.stop(); for (const [name, descriptor] of saved) { if (descriptor) Object.defineProperty(globalThis, name, descriptor); else Reflect.deleteProperty(globalThis, name) } }
})

test('authorization loss plus initialization rejection releases resources; capability callback can synchronously stop', async () => {
  const names = ['navigator', 'AudioContext'] as const
  const saved = names.map(name => [name, Object.getOwnPropertyDescriptor(globalThis, name)] as const)
  let stopped = 0; let closed = 0; let created = 0; let authorized = true
  let rejectModule!: (error: Error) => void
  Object.defineProperty(globalThis, 'navigator', { configurable: true, value: { mediaDevices: {
    getUserMedia: async () => { const t = { readyState: 'live', onended: null, stop: () => stopped++, getSettings: () => ({}) }; return { getTracks: () => [t], getAudioTracks: () => [t] } }, getSupportedConstraints: () => ({}),
  } } })
  class Context { constructor() { created++ }; audioWorklet = { addModule: () => new Promise<void>((_, reject) => { rejectModule = reject }) }; async close() { closed++ } }
  Object.defineProperty(globalThis, 'AudioContext', { configurable: true, value: Context })
  const capture = new PreflightCapture()
  try {
    const pending = capture.start(() => {}, () => {}, () => authorized)
    await Promise.resolve(); authorized = false; rejectModule(new Error('fixture module failure'))
    await assert.rejects(pending); assert.equal(stopped, 1); assert.equal(closed, 1)
    authorized = true
    await capture.start(() => capture.stop(), () => {}, () => authorized)
    assert.equal(created, 1); assert.equal(stopped, 2)
  } finally { capture.stop(); for (const [name, descriptor] of saved) { if (descriptor) Object.defineProperty(globalThis, name, descriptor); else Reflect.deleteProperty(globalThis, name) } }
})

test('worklet batches 80ms and bounds stalled consumer to two seconds with explicit overflow', () => {
  let Processor: any; const messages: any[] = []
  class Base { port = { onmessage: null, postMessage: (value: any) => messages.push(value) } }
  runInNewContext(workletSource, { AudioWorkletProcessor: Base, sampleRate: 16000, Float32Array, registerProcessor: (_: string, value: any) => { Processor = value } })
  const processor = new Processor(); const frame = [[new Float32Array(320).fill(0.1)]]
  for (let i = 0; i < 4; i++) processor.process(frame)
  assert.equal(messages.length, 1); assert.equal(messages[0].length, 1280)
  for (let i = 0; i < 101; i++) processor.process(frame)
  assert.equal(messages.length, 1); assert.equal(processor.pending.length, 0)
  processor.port.onmessage(); assert.equal(messages[1].overflow, true)
})
