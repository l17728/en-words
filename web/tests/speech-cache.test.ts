import assert from 'node:assert/strict'
import { test } from 'node:test'
import { SpeechCache, completeWav } from '../src/audio/speech-cache.ts'
import { encodePCM16Wav } from '../src/audio/wav.ts'

const wav = () => encodePCM16Wav({ length: 8, sampleRate: 16000, numberOfChannels: 1, getChannelData: () => new Float32Array(8) })
const check = () => {}
const signal = () => new AbortController().signal

test('three repeats reuse successful WAV but play and finish every segment', async () => {
  const cache = new SpeechCache(); let loads = 0, starts = 0, ends = 0
  const load = async () => { loads++; return wav() }
  const play = async () => { starts++; ends++ }
  for (let i = 0; i < 3; i++) await cache.play('account:lesson', '中文', signal(), load, play, check)
  assert.deepEqual([loads, starts, ends], [1, 3, 3])
})

test('scope changes and explicit clear revoke late loads before playback or caching', async () => {
  const cache = new SpeechCache(); let release!: (blob: Blob) => void, oldPlays = 0, loads = 0
  const pending = cache.play('a:1', '中文', signal(), () => new Promise(resolve => { release = resolve }), async () => { oldPlays++ }, check)
  await cache.play('b:1', '中文', signal(), async () => { loads++; return wav() }, async () => {}, check)
  release(wav()); await assert.rejects(pending, { name: 'AbortError' }); assert.equal(oldPlays, 0)
  await cache.play('b:2', '中文', signal(), async () => { loads++; return wav() }, async () => {}, check)
  cache.clear()
  await cache.play('b:2', '中文', signal(), async () => { loads++; return wav() }, async () => {}, check)
  assert.equal(loads, 3)
})

test('abort after loading or during playback never becomes a cache success', async () => {
  for (const phase of ['load', 'play']) {
    const cache = new SpeechCache(), controller = new AbortController(); let loads = 0
    await assert.rejects(cache.play('a', 'x', controller.signal,
      async () => { loads++; if (phase === 'load') controller.abort(); return wav() },
      async () => { if (phase === 'play') controller.abort() }, check), { name: 'AbortError' })
    await cache.play('a', 'x', signal(), async () => { loads++; return wav() }, async () => {}, check)
    assert.equal(loads, 2)
  }
})

test('transport errors and invalid WAV are not cached; playback failure evicts a hit', async () => {
  const cache = new SpeechCache(); let loads = 0
  await assert.rejects(cache.play('a', 'x', signal(), async () => { throw new Error('503') }, async () => {}, check))
  for (let i = 0; i < 2; i++) await cache.play('a', 'x', signal(), async () => { loads++; return new Blob(['bad'], { type: 'audio/wav' }) }, async () => {}, check)
  assert.equal(loads, 2)
  const load = async () => { loads++; return wav() }
  await cache.play('a', 'x', signal(), load, async () => {}, check)
  await assert.rejects(cache.play('a', 'x', signal(), load, async () => { throw new Error('decode') }, check))
  await cache.play('a', 'x', signal(), load, async () => {}, check)
  assert.equal(loads, 4)
})

test('LRU obeys byte and item caps; hits do not extend TTL', async () => {
  let now = 0, loads = 0
  const cache = new SpeechCache(wav().size * 2, 2, 100, () => now)
  const run = (key: string) => cache.play('a', key, signal(), async () => { loads++; return wav() }, async () => {}, check)
  await run('a'); await run('b'); now = 50; await run('a'); await run('c'); await run('b')
  assert.equal(loads, 4) // b was evicted, not the recently played a.
  now = 101; await run('a'); assert.equal(loads, 5)
  now = 0
  const expiring = new SpeechCache(1024, 2, 100, () => now)
  const replay = () => expiring.play('a', 'ttl', signal(), async () => { loads++; return wav() }, async () => {}, check)
  await replay(); now = 50; await replay(); now = 101; await replay()
  assert.equal(loads, 7) // TTL expires even when the entry is never evicted and was recently hit.
  const tiny = new SpeechCache(wav().size - 1)
  for (let i = 0; i < 2; i++) await tiny.play('a', 'x', signal(), async () => { loads++; return wav() }, async () => {}, check)
  assert.equal(loads, 9)
})

test('complete WAV validation rejects truncation, MIME mismatch and corrupt block alignment', async () => {
  const good = wav(); assert.equal(await completeWav(good), true)
  assert.equal(await completeWav(good.slice(0, good.size - 1, 'audio/wav')), false)
  assert.equal(await completeWav(new Blob([good], { type: 'text/html' })), false)
  const bad = await good.arrayBuffer(); new DataView(bad).setUint16(32, 7, true)
  assert.equal(await completeWav(new Blob([bad], { type: 'audio/wav' })), false)
})

test('cache hit still checks current identity and concurrent eviction cannot exceed the byte cap', async () => {
  const cache = new SpeechCache(wav().size, 1); let loads = 0, release!: () => void
  const load = async () => { loads++; return wav() }
  await cache.play('a', 'x', signal(), load, async () => {}, check)
  await assert.rejects(cache.play('a', 'x', signal(), load, async () => {}, () => { throw new Error('session changed') }))
  const pending = cache.play('a', 'x', signal(), load, () => new Promise<void>(resolve => { release = resolve }), check)
  await cache.play('a', 'y', signal(), load, async () => {}, check)
  release(); await pending
  await cache.play('a', 'x', signal(), load, async () => {}, check)
  assert.equal(loads, 3)
})
