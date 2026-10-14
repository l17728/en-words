import assert from 'node:assert/strict'
import { test } from 'node:test'
import { encodePCM16Wav, recordingToWav } from '../src/audio/wav.ts'

const samples = (...channels: number[][]) => ({
  length: channels[0].length, sampleRate: 16000, numberOfChannels: channels.length,
  getChannelData: (index: number) => new Float32Array(channels[index]),
})
test('WAV is PCM16 mono 16000 Hz with little-endian RIFF lengths and clipped samples', async () => {
  const wav = encodePCM16Wav(samples([-2, -1, 0, 1, 2]))
  assert.equal(wav.type, 'audio/wav')
  const buffer = await wav.arrayBuffer(); const view = new DataView(buffer)
  assert.equal(new TextDecoder().decode(buffer.slice(0, 4)), 'RIFF')
  assert.equal(new TextDecoder().decode(buffer.slice(8, 12)), 'WAVE')
  assert.equal(view.getUint32(4, true), buffer.byteLength - 8)
  assert.equal(view.getUint16(20, true), 1); assert.equal(view.getUint16(22, true), 1)
  assert.equal(view.getUint32(24, true), 16000); assert.equal(view.getUint32(28, true), 32000)
  assert.equal(view.getUint16(32, true), 2); assert.equal(view.getUint16(34, true), 16)
  assert.equal(view.getUint32(40, true), 10)
  assert.deepEqual(Array.from({ length: 5 }, (_, i) => view.getInt16(44 + i * 2, true)), [-32768, -32768, 0, 32767, 32767])
})
test('stereo is averaged before quantization, preserving cancellation and headroom', async () => {
  const data = new DataView(await encodePCM16Wav(samples([1, -1, 1], [-1, 0, 1])).arrayBuffer())
  assert.deepEqual([44, 46, 48].map(offset => data.getInt16(offset, true)), [0, -16384, 32767])
})
test('invalid sample rate, channel geometry, empty or nonfinite samples are rejected', () => {
  assert.throws(() => encodePCM16Wav({ ...samples([0]), sampleRate: 48000 }))
  assert.throws(() => encodePCM16Wav(samples([])))
  assert.throws(() => encodePCM16Wav(samples([0, 1], [0])))
  assert.throws(() => encodePCM16Wav(samples([NaN])))
  assert.throws(() => encodePCM16Wav(samples([Infinity])))
})
test('cancellation after compressed body read prevents decoder construction', async () => {
  let checks = 0
  await assert.rejects(recordingToWav(new Blob(['compressed']), () => { if (++checks === 2) throw new DOMException('Aborted', 'AbortError') }), { name: 'AbortError' })
  assert.equal(checks, 2)
})
test('identity changes while decoding reject the decoded result instead of encoding/uploading it', async () => {
  let release: () => void = () => {}; let active = true
  const old = globalThis.OfflineAudioContext
  class Decoder {
    constructor(channels: number, length: number, rate: number) { assert.deepEqual([channels, length, rate], [1, 1, 16000]) }
    async decodeAudioData() { await new Promise<void>(resolve => { release = resolve }); return samples([0.25]) }
  }
  globalThis.OfflineAudioContext = Decoder as unknown as typeof OfflineAudioContext
  try {
    const result = recordingToWav(new Blob(['compressed']), () => { if (!active) throw new DOMException('Aborted', 'AbortError') })
    await new Promise(resolve => setImmediate(resolve))
    active = false; release()
    await assert.rejects(result, { name: 'AbortError' })
  } finally { globalThis.OfflineAudioContext = old }
})
