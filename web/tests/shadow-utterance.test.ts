import test from 'node:test'
import assert from 'node:assert/strict'
import { ShadowUtterance, encodeWav } from '../src/audio/duplex/shadow.ts'

const frame = (value = 1) => new Int16Array(320).fill(value)

test('shadow utterance bounds frames and creates a PCM16 WAV', () => {
  const stream = new ShadowUtterance(); stream.start(0); stream.append(frame(7), 20)
  const wav = stream.end(); assert.equal(stream.status, 'ended'); assert.equal(stream.sampleCount, 320)
  assert.equal(wav.type, 'audio/wav'); assert.equal(wav.size, 44 + 640)
})

test('shadow utterance rejects stale lifecycle and overflow without retaining audio', () => {
  const stream = new ShadowUtterance(); stream.start(0)
  assert.throws(() => stream.append(new Int16Array(319), 20), /shadow_invalid_frame/)
  assert.throws(() => stream.append(frame(), 15_001), /shadow_stream_overflow/)
  assert.equal(stream.status, 'overflowed'); stream.cancel(); assert.equal(stream.sampleCount, 0)
  assert.throws(() => stream.end(), /shadow_utterance_not_capturing/)
})

test('encodeWav preserves frame order and little-endian samples', async () => {
  const blob = encodeWav([new Int16Array([0x1234, -2])], 16_000)
  const bytes = new Uint8Array(await blob.arrayBuffer())
  assert.deepEqual([...bytes.slice(44)], [0x34, 0x12, 0xfe, 0xff])
})
