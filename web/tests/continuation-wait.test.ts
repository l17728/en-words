import { test } from 'node:test'
import assert from 'node:assert/strict'
import { PlaybackContinuationWait } from '../src/audio/continuation-wait.ts'
const identity = { session: 1, lesson: '17', epoch: 'e1', turn: 't1', playback: 3 }
const frame = (type: string, seq = 5) => ({ type: 'ui_action', action: { type }, context: { epoch: 'e1', turn: 't2' }, seq })
test('successful local completion waits; only newer valid business progress clears', () => {
  const wait = new PlaybackContinuationWait(); wait.begin(identity, 4); assert.equal(wait.waiting, true)
  for (const f of [{ type: 'heartbeat_ack', seq: 5 }, { type: 'event_processed', seq: 5 }, { type: 'state_snapshot', phase: 'repeating', seq: 5 }, frame('play_tts', 4), { ...frame('play_tts'), context: { epoch: 'old', turn: 't2' } }, frame('show_feedback_ui')]) {
    assert.equal(wait.advance(f), false); assert.equal(wait.waiting, true)
  }
  assert.equal(wait.advance(frame('start_repeat')), true); assert.equal(wait.waiting, false)
})
test('old callbacks cannot clear a newer waiting generation; lifecycle clear invalidates it', () => {
  const wait = new PlaybackContinuationWait(); const old = wait.begin(identity, 4)
  const next = wait.begin({ ...identity, playback: 4, turn: 't2' }, 8)
  assert.notEqual(old, next); assert.equal(wait.clear(old), false); assert.equal(wait.waiting, true)
  assert.equal(wait.advance(frame('play_tts', 5)), false)
  assert.equal(wait.clear(next), true); assert.equal(wait.waiting, false)
  wait.begin({ ...identity, session: 2 }, 9); wait.clear(); assert.equal(wait.waiting, false)
})
test('invalid context or sequence never creates or completes a wait; real phase progress does', () => {
  const wait = new PlaybackContinuationWait(); wait.begin({ ...identity, epoch: '' }, 0); assert.equal(wait.waiting, false)
  wait.begin(identity, 4)
  for (const f of [frame('play_tts', NaN), { type: 'phase_changed', phase: 'tts_seg', context: { epoch: 'e1', turn: 't1' }, seq: 5 }, { ...frame('play_tts'), context: { epoch: 'e1', turn: '' } }]) assert.equal(wait.advance(f), false)
  assert.equal(wait.advance({ type: 'phase_changed', phase: 'repeating', context: { epoch: 'e1', turn: 't2' }, seq: 5 }), true)
})

test('observer logs only real transitions with bounded fixed summaries and never raw identity text', () => {
  const events: any[] = []; const wait = new PlaybackContinuationWait(event => events.push(event))
  wait.begin({ ...identity, epoch: '' }, 4); assert.equal(events.length, 0)
  const first = wait.begin({ ...identity, epoch: 'PRIVATE-EPOCH', turn: 'PRIVATE-TURN' }, 4)
  assert.equal(events.length, 1); wait.advance({ type: 'heartbeat_ack' }); wait.clear(first - 1)
  assert.equal(events.length, 1)
  wait.begin(identity, 7); assert.deepEqual(events.map(e => e.event), ['waiting_started', 'waiting_cleared', 'waiting_started'])
  assert.equal(events[1].reason, 'replaced'); wait.clear(first); assert.equal(events.length, 3)
  wait.advance(frame('start_repeat', 8)); assert.equal(events[3].reason, 'business_progress'); assert.equal(events[3].accepted_seq, 8)
  wait.clear(); assert.equal(events.length, 4)
  assert.equal(JSON.stringify(events).includes('PRIVATE'), false)
  for (const event of events) assert.ok(Object.keys(event).every(key => ['event','module','lesson_id','generation','session_generation','playback_generation','baseline_seq','reason','accepted_seq'].includes(key)))
})
test('observer exceptions cannot prevent waiting cleanup or successful progress', () => {
  const wait = new PlaybackContinuationWait(() => { throw new Error('observer failed') })
  wait.begin(identity, 4); assert.equal(wait.waiting, true)
  assert.equal(wait.advance(frame('start_repeat')), true); assert.equal(wait.waiting, false)
})
