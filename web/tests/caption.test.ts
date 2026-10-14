import assert from 'node:assert/strict'
import { test } from 'node:test'
import { PlaybackCaption, shouldCancelPlaybackPhase } from '../src/audio/caption.ts'
import { playAudio } from '../src/audio/playback.ts'

test('caption stays empty while loading and repeats identical text with separate leases', () => {
  const shown: string[] = []; const caption = new PlaybackCaption(text => shown.push(text))
  const first = caption.begin(); assert.deepEqual(shown, [])
  caption.show(first, 'word'); caption.clear(first)
  const second = caption.begin(); assert.notEqual(first, second)
  caption.show(second, 'word'); caption.clear(second)
  assert.deepEqual(shown, ['word', '', 'word', ''])
})
test('snapshot and phase changes share the same playback cancellation boundary', () => {
  for (const phase of ['tts_seg', 'reteach_show']) {
    assert.equal(shouldCancelPlaybackPhase(phase), false)
    assert.equal(shouldCancelPlaybackPhase(phase, true), true)
  }
  for (const phase of ['wait_answer', 'show_word', 'repeating', 'lesson_end', undefined]) {
    assert.equal(shouldCancelPlaybackPhase(phase), true)
  }
})
test('old start/end and reset cannot restore old text or clear a new attempt', () => {
  const shown: string[] = []; const caption = new PlaybackCaption(text => shown.push(text))
  const old = caption.begin(); caption.show(old, 'old'); caption.reset()
  const next = caption.begin(); caption.show(next, 'new')
  caption.show(old, 'stale'); caption.clear(old)
  assert.deepEqual(shown, ['old', '', 'new'])
  caption.clear(next); caption.show(next, 'late'); assert.equal(shown.at(-1), '')
})
test('caption invalidates its lease before synchronous cleanup reentry and isolates instances', () => {
  const shown: string[] = []; let lease = 0
  const caption = new PlaybackCaption(text => { shown.push(text); if (!text) caption.show(lease, 'stale') })
  lease = caption.begin(); caption.show(lease, 'A')
  const other: string[] = []; const b = new PlaybackCaption(text => other.push(text))
  b.show(b.begin(), 'B'); caption.reset()
  assert.deepEqual(shown, ['A', '']); assert.deepEqual(other, ['B'])
})

function fakePlayer() {
  return { onplaying: null as any, onended: null as any, onerror: null as any,
    pauses: 0, play: async () => {}, pause() { this.pauses++ } }
}
test('Audio loading/play resolution never announces speech; only playing does, ended cleans callbacks', async () => {
  const p = fakePlayer(); const signal = new AbortController().signal; let starts = 0
  const promise = playAudio(p as any, signal, () => {}, () => starts++)
  await Promise.resolve(); assert.equal(starts, 0)
  const late = p.onplaying; p.onplaying(); assert.equal(starts, 1)
  p.onended(); await promise; late(); assert.equal(starts, 1)
  assert.equal(p.onplaying, null); assert.equal(p.onended, null); assert.equal(p.onerror, null)
})
test('aborted Audio rejects and retained callbacks cannot announce late speech', async () => {
  const p = fakePlayer(); const abort = new AbortController(); let starts = 0
  const promise = playAudio(p as any, abort.signal, () => {}, () => starts++)
  const late = p.onplaying; abort.abort(); late()
  await assert.rejects(promise, { name: 'AbortError' }); assert.equal(starts, 0); assert.equal(p.pauses, 1)
})
test('Audio observer exceptions and changed identity fail without successful completion', async () => {
  for (const changed of [false, true]) {
    const p = fakePlayer(); let valid = true
    const promise = playAudio(p as any, new AbortController().signal,
      () => { if (!valid) throw new Error('identity') }, () => { throw new Error('observer') })
    valid = !changed; const end = p.onended; p.onplaying(); end()
    await assert.rejects(promise, new RegExp(changed ? 'identity' : 'observer'))
    assert.equal(p.onplaying, null); assert.equal(p.pauses, 1)
  }
})
test('Audio error cleans its observer and rejects', async () => {
  const p = fakePlayer(); let starts = 0
  const promise = playAudio(p as any, new AbortController().signal, () => {}, () => starts++)
  const late = p.onplaying; p.onerror(); late()
  await assert.rejects(promise, /播放失败/); assert.equal(starts, 0)
})
test('Audio timeout clears observers and never resolves successfully', async context => {
  context.mock.timers.enable({ apis: ['setTimeout'] })
  const p = fakePlayer(); let starts = 0
  const promise = playAudio(p as any, new AbortController().signal, () => {}, () => starts++)
  const late = p.onplaying; context.mock.timers.tick(20000); late()
  await assert.rejects(promise, /超时/); assert.equal(starts, 0); assert.equal(p.pauses, 1)
})
test('falsy playback rejections and observer throws are still failures', async () => {
  for (const value of [undefined, null, '', false, 0]) {
    const rejected = fakePlayer(); rejected.play = () => Promise.reject(value)
    await assert.rejects(playAudio(rejected as any, new AbortController().signal, () => {}))
    assert.equal(rejected.pauses, 1)
    const observer = fakePlayer()
    const promise = playAudio(observer as any, new AbortController().signal, () => {}, () => { throw value })
    observer.onplaying()
    await assert.rejects(promise); assert.equal(observer.pauses, 1)
  }
})
