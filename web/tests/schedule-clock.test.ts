import assert from 'node:assert/strict'
import { test } from 'node:test'
import { isLessonOpen, watchScheduleClock } from '../src/schedule/clock.ts'

function host() {
  let now = 1000; let visible = true; let nextId = 0
  const timers = new Map<number, () => void>()
  const clock = {
    now: () => now,
    setInterval(callback: () => void, milliseconds: number) { assert.equal(milliseconds, 1000); timers.set(++nextId, callback); return nextId },
    clearInterval(id: number) { timers.delete(id) },
    focusTarget: new EventTarget(), visibilityTarget: new EventTarget(), isVisible: () => visible,
  }
  return { clock, timers, time: (value: number) => { now = value }, visible: (value: boolean) => { visible = value } }
}

test('lesson display gate is open at equality and past, closed for future and invalid clocks', () => {
  const at = '2026-10-14T08:00:00.000Z'; const now = Date.parse(at)
  assert.equal(isLessonOpen(at, now - 1), false)
  assert.equal(isLessonOpen(at, now), true)
  assert.equal(isLessonOpen(at, now + 1), true)
  for (const invalid of ['', 'not-a-date']) assert.equal(isLessonOpen(invalid, now), false)
  for (const invalid of [NaN, Infinity, -Infinity]) assert.equal(isLessonOpen(at, invalid), false)
})

test('schedule clock refreshes on tick and focus, and only visible visibility changes', () => {
  const f = host(); const seen: number[] = []; const stop = watchScheduleClock(now => seen.push(now), f.clock)
  try {
    assert.deepEqual(seen, []) // No synchronous effect update; UI initializes its own clock lazily.
    f.time(2000); [...f.timers.values()][0]()
    f.time(7000); f.clock.focusTarget.dispatchEvent(new Event('focus'))
    f.visible(false); f.time(9000); f.clock.visibilityTarget.dispatchEvent(new Event('visibilitychange'))
    f.visible(true); f.time(12000); f.clock.visibilityTarget.dispatchEvent(new Event('visibilitychange'))
    assert.deepEqual(seen, [2000, 7000, 12000])
  } finally { stop() }
})

test('schedule clock cleanup removes timers and listeners and rejects an already queued callback', () => {
  const f = host(); let calls = 0; const stop = watchScheduleClock(() => calls++, f.clock)
  const queued = [...f.timers.values()][0]; stop(); stop()
  assert.equal(f.timers.size, 0)
  queued(); f.clock.focusTarget.dispatchEvent(new Event('focus')); f.clock.visibilityTarget.dispatchEvent(new Event('visibilitychange'))
  assert.equal(calls, 0)
})

test('unmounting one clock subscriber does not cancel another page subscription', () => {
  const f = host(); let first = 0; let second = 0
  const stopFirst = watchScheduleClock(() => first++, f.clock)
  const stopSecond = watchScheduleClock(() => second++, f.clock)
  stopFirst()
  try { assert.equal(f.timers.size, 1); f.clock.focusTarget.dispatchEvent(new Event('focus')); assert.equal(first, 0); assert.equal(second, 1) }
  finally { stopSecond() }
  assert.equal(f.timers.size, 0)
})
