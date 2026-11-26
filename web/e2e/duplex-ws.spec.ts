import { test, expect } from './recovery-server'
import { api, fixture, scheduled, login } from './isolated-fixtures'

test.use({ localModel: true, duplexPilot: true, duplexShadow: true })

test('WS duplex shadow requires explicit confirmation before classroom progress', async ({ page, request }) => {
  test.setTimeout(120_000)
  const f = await fixture(request, 'duplex-ws', [{ word: 'cat', meanings: ['猫'] }])
  await api(request, '/config/set', f.admin.token,
    { scope: 'student', studentId: f.student.user_id, key: 'repeatTimes', value: 1, reason: 'single confirmed repeat for duplex effect boundary' })
  await api(request, '/student/playback-settings', f.student.token,
    { ttsEnRepeat: 1, ttsZhRepeat: 1, ttsIntervalMs: 0, ttsZhIntervalMs: 0, ttsSwitchIntervalMs: 0, showWordLeadMs: 250 })
  const lessonId = await scheduled(request, f)
  // This E2E isolates the confirmed duplex transaction; use the explicit
  // test-only gate bypass so the TTS pre-generation workflow stays out of scope.
  const entered = await api(request, `/classroom/lessons/${lessonId}/enter?e2e=1`, f.student.token, {})
  await login(page, f.username)
  await page.goto('/dashboard')

  const result = await page.evaluate(async ({ lessonId, ticket, context }) => {
    const frames: any[] = []
    const acceptedAfterSnapshot: any[] = []
    const staleAfterSnapshot: any[] = []
    let hasSnapshot = false
    let lastAccepted = { epoch: '', seq: 0 }
    let liveContext = context
    const queued = new Map<string, any[]>()
    const waiters = new Map<string, Array<{ resolve: (frame: any) => void; reject: (error: Error) => void; timer: ReturnType<typeof setTimeout> }>>()
    const url = `/ws/classroom?lessonId=${lessonId}&ticket=${encodeURIComponent(ticket)}`
    const ws = new WebSocket(url)
    ws.addEventListener('message', event => {
      const frame = JSON.parse(String(event.data)); frames.push(frame)
      if (frame.type === 'state_snapshot' && frame.context && typeof frame.seq === 'number') {
        hasSnapshot = true
        lastAccepted = { epoch: frame.context.epoch, seq: frame.seq }
      } else if (hasSnapshot && frame.context?.epoch && typeof frame.seq === 'number') {
        if (frame.context.epoch === lastAccepted.epoch && frame.seq <= lastAccepted.seq) {
          staleAfterSnapshot.push(frame.type)
          return
        }
        lastAccepted = { epoch: frame.context.epoch, seq: frame.seq }
        acceptedAfterSnapshot.push(frame)
      }
      if (frame.context) liveContext = frame.context
      if (frame.snapshot?.context) liveContext = frame.snapshot.context
      const pending = frame.type === 'voice_stream_error'
        ? Array.from(waiters.entries()).flatMap(([type, entries]) => entries.map(entry => ({ type, entry })))
        : (waiters.get(frame.type) || []).map(entry => ({ type: frame.type, entry }))
      for (const { type, entry } of pending) {
        clearTimeout(entry.timer)
        const entries = waiters.get(type) || []
        waiters.set(type, entries.filter(candidate => candidate !== entry))
        if (frame.type === 'voice_stream_error') entry.reject(new Error(`stream_error:${frame.reason}:waiting:${type}`))
        else entry.resolve(frame)
      }
      if (pending.length === 0) queued.set(frame.type, [...(queued.get(frame.type) || []), frame])
    })
    const waitFor = (type: string) => {
      const backlog = queued.get(type) || []
      if (backlog.length) {
        queued.set(type, backlog.slice(1))
        return Promise.resolve(backlog[0])
      }
      return new Promise<any>((resolve, reject) => {
        const entries = waiters.get(type) || []
        const entry = { resolve, reject, timer: setTimeout(() => reject(new Error(`timeout:${type}`)), 30_000) }
        waiters.set(type, [...entries, entry])
      })
    }
    await new Promise<void>((resolve, reject) => { ws.onopen = () => resolve(); ws.onerror = () => reject(new Error('ws_error')) })
    await waitFor('state_snapshot')
    // The server must finish Join recovery before exposing its first snapshot;
    // sending immediately from that snapshot catches stale-turn initialization.
    let eventIndex = 0
    const sendClassroomEvent = async (event: string) => {
      for (let attempt = 0; attempt < 4; attempt++) {
      const eventId = `duplex-class-${++eventIndex}`
      const processed = new Promise<void>((resolve, reject) => {
        const timer = setTimeout(() => {
          ws.removeEventListener('message', handler)
          reject(new Error(`timeout:event_processed:${event}:${frames.map(frame => frame.type).slice(-12).join(',')}`))
        }, 10_000)
        const handler = (message: MessageEvent) => {
          const frame = JSON.parse(String(message.data))
          if (frame.event_id !== eventId) return
          if (frame.type === 'event_processed') {
            clearTimeout(timer)
            ws.removeEventListener('message', handler)
            resolve()
          } else if (frame.type === 'event_rejected') {
            clearTimeout(timer)
            ws.removeEventListener('message', handler)
            reject(Object.assign(new Error(`event_rejected:${frame.reason || 'unknown'}:${event}`), { context: frame.context }))
          }
        }
        ws.addEventListener('message', handler)
      })
      ws.send(JSON.stringify({ type: 'student_event', event, context: liveContext, event_id: eventId }))
      try { await processed; return } catch (error: any) {
        if (!error.context || attempt === 3) throw error
        liveContext = error.context
      }
      }
    }
    await sendClassroomEvent('word_clicked')
    await sendClassroomEvent('tts_finished')
    await sendClassroomEvent('tts_finished')

    const envelope = (id: string) => ({ context: liveContext, event_id: id })
    const recognizeAndConfirm = async (index: number) => {
      const prefix = `duplex-${index}`
      const startFrameIndex = frames.length
      ws.send(JSON.stringify({ type: 'voice_stream_start', language: 'en', sample_rate: 16000, channels: 1, mode: 'shadow', ...envelope(`${prefix}-start`) }))
      const start = await waitFor('voice_stream_start_ack')
      const pcm = new Uint8Array(640)
      let binary = ''
      for (const byte of pcm) binary += String.fromCharCode(byte)
      ws.send(JSON.stringify({ ...start, type: 'voice_stream_chunk', audio: btoa(binary), chunk_seq: 0, ...envelope(`${prefix}-chunk`) }))
      await waitFor('voice_stream_ack')
      ws.send(JSON.stringify({ ...start, type: 'voice_stream_end', ...envelope(`${prefix}-end`) }))
      const final = await waitFor('voice_confirmation_required')
      const previewIndex = frames.findIndex((frame, frameIndex) => frameIndex >= startFrameIndex && frame.type === 'voice_confirmation_required')
      const streamEffectsBeforeConfirmation = frames.slice(startFrameIndex, previewIndex)
        .filter(frame => frame.type === 'ui_action' || frame.type === 'teacher_say' || frame.type === 'event_processed')
      const actionCountBeforeConfirm = frames.filter(frame => frame.type === 'ui_action' || frame.type === 'teacher_say').length
      const bytes = new TextEncoder().encode(final.text)
      const digest = await crypto.subtle.digest('SHA-256', bytes)
      const hash = Array.from(new Uint8Array(digest)).map(value => value.toString(16).padStart(2, '0')).join('')
      ws.send(JSON.stringify({ ...start, type: 'voice_confirm', text: final.text, text_hash: hash, input_revision: final.input_revision || start.input_revision + 1, ...envelope(`${prefix}-confirm`), context: liveContext }))
      const confirmed = await waitFor('voice_confirmed')
      const confirmedIndex = frames.findIndex((frame, frameIndex) => frameIndex > previewIndex && frame.type === 'voice_confirmed' && frame.event_id === `${prefix}-confirm`)
      const confirmAckIndex = frames.findIndex((frame, frameIndex) => frameIndex > previewIndex && frame.type === 'event_processed' && frame.event_id === `${prefix}-confirm`)
      const postConfirmActions = frames.slice(previewIndex + 1, confirmedIndex)
        .filter(frame => frame.type === 'ui_action' || frame.type === 'teacher_say')
      return { final, confirmed, streamEffectsBeforeConfirmation: streamEffectsBeforeConfirmation.map(frame => frame.type), actionCountBeforeConfirm,
        confirmAckAfterPreview: confirmAckIndex > previewIndex,
        confirmAckBeforeConfirmed: confirmAckIndex > previewIndex && confirmAckIndex < confirmedIndex,
        actionCountAfterConfirm: frames.filter(frame => frame.type === 'ui_action' || frame.type === 'teacher_say').length,
        quizItemCount: postConfirmActions.filter(frame => frame.type === 'ui_action' && frame.action?.type === 'show_quiz_item').length,
        postConfirmActions: postConfirmActions.map(frame => frame.type),
        postConfirmActionTypes: postConfirmActions.filter(frame => frame.type === 'ui_action').map(frame => frame.action?.type),
        phaseChanges: frames.slice(previewIndex + 1, confirmedIndex).filter(frame => frame.type === 'phase_changed').map(frame => frame.phase) }
    }
    const attempts = [await recognizeAndConfirm(1), await recognizeAndConfirm(2)]
    const feedback = await recognizeAndConfirm(3)
    ws.close()
    const last = attempts[attempts.length - 1]
    return { confirmed: last.confirmed, transcript: last.final.text, attempts,
      feedback: { confirmed: feedback.confirmed, transcript: feedback.final.text, postConfirmActions: feedback.postConfirmActions, phaseChanges: feedback.phaseChanges },
      acceptedAfterSnapshot: acceptedAfterSnapshot.map(frame => frame.type === 'ui_action' ? frame.action?.type : frame.type), staleAfterSnapshot,
      phases: frames.filter(frame => frame.type === 'phase_changed').map(frame => frame.phase), frames: frames.map(frame => frame.type) }
  }, { lessonId, ticket: entered.ticket, context: entered.snapshot.context })
  const analyses = await api(request, `/student/lessons/${lessonId}/teacher-analysis`, f.student.token, undefined, 'GET')
  const feedbackID = result.feedback.confirmed.submission_id
  expect(feedbackID, 'confirmed voice feedback must include its idempotency key').toMatch(/^duplex_[A-Za-z0-9_-]+$/)
  const feedbackRecord = await api(request, `/feedbacks/submissions/${feedbackID}`, f.student.token, undefined, 'GET')

  expect(result.confirmed.type).toBe('voice_confirmed')
  expect(result.confirmed.commit_receipt).toBeTruthy()
  expect(result.attempts.every((attempt: any) => attempt.confirmed.effects_status === 'completed'), 'confirmed effects must report any downstream delivery failure')
  expect(result.attempts.every((attempt: any) => attempt.confirmed.commit_receipt), 'each accepted transcript requires its own durable receipt').toBe(true)
  expect(new Set(result.attempts.map((attempt: any) => attempt.confirmed.commit_receipt)).size).toBe(2, 'same-turn retries must not reuse receipt identities')
  expect(result.attempts[0].confirmed.command).toBe('', 'an unrecognized word remains in the same repeat turn')
  expect(result.attempts[0].phaseChanges).toEqual([], 'a miss must not advance the classroom')
  expect(result.confirmed.command).toBe('repeat_read', `second ASR transcript was ${result.transcript}`)
  expect(result.attempts.every((attempt: any) => attempt.streamEffectsBeforeConfirmation.length === 0), 'ASR preview cannot trigger classroom effects before student confirmation').toBe(true)
  expect(result.attempts.every((attempt: any) => attempt.confirmAckAfterPreview), 'success acknowledgements must follow durable confirmation commits').toBe(true)
  expect(result.attempts.every((attempt: any) => attempt.confirmAckBeforeConfirmed), 'confirmation receipts follow their committed event acknowledgements').toBe(true)
  expect(result.attempts[0].postConfirmActions).toEqual([], 'a confirmed mismatch must not emit classroom actions')
  expect(result.attempts[1].postConfirmActions.length).toBeGreaterThan(0, 'the confirmed repeat should publish the next classroom action')
  expect(result.attempts[1].phaseChanges).toContain('wait_answer', 'the matching repeat must advance to the quiz answer phase')
  expect(result.attempts[1].quizItemCount,
    `confirmed classroom action types=${result.attempts[1].postConfirmActionTypes.join(',')}`).toBe(1, 'the confirmed repeat should publish the quiz item exactly once')
  for (const attempt of result.attempts) expect(attempt.actionCountAfterConfirm).toBeGreaterThanOrEqual(attempt.actionCountBeforeConfirm)
  expect(result.frames).toContain('voice_confirmation_required')
  expect(result.frames).toContain('voice_confirmed')
  expect(result.feedback.confirmed.command).toBe('feedback', `feedback transcript was ${result.feedback.transcript}`)
  expect(feedbackRecord.submission_id).toBe(feedbackID)
  expect(feedbackRecord.text).toContain('每天')
  expect(result.feedback.postConfirmActions).toEqual([], 'feedback capture must not mutate or advance the classroom')
  expect(result.feedback.phaseChanges).toEqual([])
  expect(analyses.some((row: any) => row.transcript === 'banana' && row.failure_type === 'target_mismatch'),
    'confirmed repeat mismatch should enqueue exactly the diagnostic source after the commit').toBe(true)
  expect(result.staleAfterSnapshot).toEqual([], 'Join recovery frames must not be discarded as stale after the snapshot')
  expect(result.acceptedAfterSnapshot).toContain('play_tts', 'the client must consume the initial recovered playback action')
})
