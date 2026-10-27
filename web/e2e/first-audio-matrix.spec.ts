import { expect, test } from '@playwright/test'
import { performance } from 'node:perf_hooks'
import { writeFile } from 'node:fs/promises'

declare global {
  interface Window {
    __firstAudioMatrix?: {
      started: Array<{ text: string; at: number }>
      utterances: Array<{ text: string; onend: (() => void) | null }>
      cancelCount: number
      sentEvents: string[]
      sockets: WebSocket[]
      latestFrame: { url: string; context: Record<string, unknown>; seq: number } | null
      isTtsFinished: (frame: { type?: unknown; event?: unknown }) => boolean
    }
  }
}

const apiOrigin = process.env.FIRST_AUDIO_MATRIX_API_ORIGIN || ''
const lessonId = Number(process.env.FIRST_AUDIO_MATRIX_LESSON_ID || 0)
const generation = Number(process.env.FIRST_AUDIO_MATRIX_GENERATION || 0)
const username = process.env.FIRST_AUDIO_MATRIX_USERNAME || ''
const password = process.env.FIRST_AUDIO_MATRIX_PASSWORD || ''

function isTtsFinishedRestRequest(method: string, url: string, targetLessonId: number, postData: string | null): boolean {
  if (method !== 'POST' || !postData) return false
  try {
    if (new URL(url).pathname !== `/api/v1/classroom/lessons/${targetLessonId}/event`) return false
    return (JSON.parse(postData) as { event?: unknown }).event === 'tts_finished'
  } catch { return false }
}

test('FIRST_AUDIO_MATRIX_JOURNEY_V1 first entry reaches subtitle and first audio', async ({ page }) => {
  test.setTimeout(40_000)
  expect(new URL(apiOrigin).hostname).toBe('127.0.0.1')
  expect(lessonId).toBeGreaterThan(0)
  expect(generation).toBeGreaterThan(0)
  expect(username).toMatch(/^first-audio-[a-z0-9-]{1,32}$/)
  expect(password).toBeTruthy()

  const loginResponse = await page.request.post(`${apiOrigin}/api/v1/auth/login`, {
    data: { username, password },
    timeout: 5000,
  })
  expect(loginResponse.ok()).toBeTruthy()
  const envelope = await loginResponse.json()
  expect(envelope.code).toBe(0)
  const login = envelope.data
  expect(login.role).toBe('student')
  expect(login.user_id).toBeGreaterThan(0)

  let firstAudioFrameAt: number | null = null
  let audioStartedAt: number | null = null
  await page.exposeBinding('__matrixAudioStarted', () => { audioStartedAt = performance.now() })
  await page.addInitScript(({ token, role, nickname, userId }) => {
    localStorage.setItem('token', token)
    localStorage.setItem('role', role)
    localStorage.setItem('nickname', nickname)
    localStorage.setItem('userId', String(userId))

    const state = { started: [] as Array<{ text: string; at: number }>,
      utterances: [] as Array<{ text: string; onend: (() => void) | null }>,
      cancelCount: 0, sentEvents: [] as string[], sockets: [] as WebSocket[],
      latestFrame: null as { url: string; context: Record<string, unknown>; seq: number } | null,
      isTtsFinished: (frame: { type?: unknown; event?: unknown }) =>
        frame.type === 'student_event' && frame.event === 'tts_finished' }
    window.__firstAudioMatrix = state
    class MatrixUtterance {
      text: string
      lang = 'en-US'
      onstart: (() => void) | null = null
      onend: (() => void) | null = null
      onerror: (() => void) | null = null
      constructor(text = '') { this.text = text }
    }
    Object.defineProperty(window, 'SpeechSynthesisUtterance', { configurable: true, value: MatrixUtterance })
    Object.defineProperty(window, 'speechSynthesis', {
      configurable: true,
      value: {
        getVoices: () => [{ name: 'Matrix synthetic en-US', lang: 'en-US', localService: true }],
        speak: (utterance: MatrixUtterance) => {
          state.utterances.push(utterance)
          utterance.onstart?.()
          state.started.push({ text: utterance.text, at: performance.now() })
          void (window as unknown as { __matrixAudioStarted?: () => void }).__matrixAudioStarted?.()
        },
        cancel: () => { state.cancelCount++ },
      },
    })
    const NativeWebSocket = window.WebSocket
    Object.defineProperty(window, 'WebSocket', {
      configurable: true,
      value: new Proxy(NativeWebSocket, {
        construct(target, args) {
          const socket = Reflect.construct(target, args) as WebSocket
          state.sockets.push(socket)
          socket.addEventListener('message', event => {
            try {
              const frame = JSON.parse(String(event.data)) as {
                context?: Record<string, unknown>; seq?: unknown
              }
              if (frame.context && typeof frame.seq === 'number') {
                state.latestFrame = { url: socket.url, context: frame.context, seq: frame.seq }
              }
            } catch { /* ignore non-protocol messages */ }
          })
          const send = socket.send.bind(socket)
          socket.send = (data: string | ArrayBufferLike | Blob | ArrayBufferView) => {
            try {
              const frame = JSON.parse(String(data)) as { type?: unknown }
              if (state.isTtsFinished(frame)) state.sentEvents.push('tts_finished')
            } catch { /* heartbeat and binary payloads are counted only when parseable */ }
            send(data)
          }
          return socket
        },
      }),
    })
  }, {
    token: login.token,
    role: login.role,
    nickname: login.nickname,
    userId: login.user_id,
  })

  const frames: Array<Record<string, unknown>> = []
  let observedSocket = false
  let observedClassroomLessonId: string | null = null
  let restTtsFinishedSent = 0
  page.on('request', request => {
    if (isTtsFinishedRestRequest(request.method(), request.url(), lessonId, request.postData())) restTtsFinishedSent++
  })
  let resolveSocketClosed: (() => void) | undefined
  const socketClosed = new Promise<boolean>(resolve => { resolveSocketClosed = () => resolve(true) })
  const isPlayback = (frame: Record<string, unknown>) => {
    const action = frame.action as { type?: string } | undefined
    return frame.type === 'ui_action' && action?.type === 'play_tts'
  }
  page.on('websocket', socket => {
    observedSocket = true
    try {
      const url = new URL(socket.url())
      if (url.pathname === '/ws/classroom') observedClassroomLessonId = url.searchParams.get('lessonId')
    } catch { /* malformed socket URLs fail the explicit lesson binding below */ }
    socket.on('close', () => resolveSocketClosed?.())
    socket.on('framereceived', ({ payload }) => {
      try {
        const frame = JSON.parse(String(payload)) as Record<string, unknown>
        frames.push(frame)
        if (firstAudioFrameAt === null && isPlayback(frame)) firstAudioFrameAt = performance.now()
      } catch { /* non-JSON control frames are irrelevant to the journey */ }
    })
  })

  const enteredAt = performance.now()
  await page.goto(`/classroom/${lessonId}`)
  await expect(page.getByText('已连接', { exact: true })).toBeVisible({ timeout: 15_000 })
  await expect.poll(() => frames.some(isPlayback), { timeout: 20_000 }).toBeTruthy()
  await expect.poll(() => page.evaluate(() => window.__firstAudioMatrix?.started.length || 0), { timeout: 20_000 }).toBeGreaterThan(0)

  const firstAudio = frames.find(isPlayback)
  expect(firstAudio).toBeTruthy()
  const action = firstAudio?.action as { word?: string } | undefined
  expect(action?.word).toBeTruthy()
  expect(observedClassroomLessonId).toBe(String(lessonId))
  expect(firstAudioFrameAt).not.toBeNull()
  expect(audioStartedAt).not.toBeNull()
  expect((firstAudioFrameAt as number) - enteredAt).toBeLessThan(10_000)
  expect((audioStartedAt as number) - enteredAt).toBeLessThan(10_000)
  await expect(page.getByText(action?.word || '', { exact: true })).toBeVisible()
  const caption = page.locator('[aria-label="当前朗读字幕"]')
  await expect(caption).toHaveText(action?.word || '')
  await expect.poll(() => page.evaluate(() => window.__firstAudioMatrix?.started[0]?.text || '')).toBe(action?.word)
  expect(await page.evaluate(() => window.__firstAudioMatrix?.isTtsFinished({
    type: 'student_event', event: 'tts_finished',
  }) || false)).toBe(true) // Positive control: detector matches the actual ClassroomWS envelope.
  expect(isTtsFinishedRestRequest('POST', `http://127.0.0.1/api/v1/classroom/lessons/${lessonId}/event`, lessonId,
    JSON.stringify({ event: 'tts_finished' }))).toBe(true) // Positive control for REST fallback observation.
  expect(firstAudio?.context).toBeTruthy()
  const frameContext = firstAudio?.context
  expect(frameContext).toMatchObject({ epoch: expect.any(String), turn: expect.any(String) })
  const expectedContext = frameContext as { epoch: string; turn: string }
  const injected = await page.evaluate(({ expected }) => {
    const matrix = window.__firstAudioMatrix
    const latest = matrix?.latestFrame
    if (!latest || latest.context.epoch !== expected.epoch || latest.context.turn !== expected.turn) return false
    const socket = matrix?.sockets.find(candidate => candidate.url === latest?.url &&
      candidate.url.includes('/ws/classroom?') && candidate.readyState === WebSocket.OPEN)
    if (!matrix || !latest || !socket || typeof socket.onmessage !== 'function') return false
    const cancelCountBefore = matrix.cancelCount
    socket.onmessage(new MessageEvent('message', { data: JSON.stringify({
      type: 'ui_action', seq: latest.seq + 1, context: latest.context, action: { type: 'lesson_paused' },
    }) }))
    return { cancelCountBefore }
  }, { expected: expectedContext })
  expect(injected).toBeTruthy()
  if (!injected) throw new Error('could not deliver pause frame to classroom handler')
  await expect(page.getByText('课堂已暂停')).toBeVisible()
  await expect(caption).toBeEmpty()
  const cancelled = await page.evaluate(() => {
    const state = window.__firstAudioMatrix
    const active = state?.utterances[0]
    if (!state || !active || typeof active.onend !== 'function') return false
    active.onend() // Deliver the stale completion callback after cancellation.
    return state.cancelCount
  })
  expect(cancelled).toBeGreaterThan(injected!.cancelCountBefore)
  await page.waitForTimeout(150)
  const interruption = await page.evaluate(() => ({
    started: window.__firstAudioMatrix?.started.length || 0,
    cancelCount: window.__firstAudioMatrix?.cancelCount || 0,
    ttsFinishedSent: window.__firstAudioMatrix?.sentEvents.filter(type => type === 'tts_finished').length || 0,
  }))
  interruption.ttsFinishedSent += restTtsFinishedSent
  expect(interruption.started).toBe(1)
  expect(interruption.cancelCount).toBeGreaterThan(injected!.cancelCountBefore)
  expect(interruption.ttsFinishedSent).toBe(0)
  await expect(caption).toBeEmpty()
  let terminalEnterAttempts = 0
  await page.route(`**/api/v1/classroom/lessons/${lessonId}/enter`, async route => {
    terminalEnterAttempts++
    await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({
      code: 500,
      msg: '课堂状态无法确认，为避免重复操作已停止本课。请返回课程表查看进度。',
      error_code: 'transaction_outcome_uncertain',
      retryable: false,
    }) })
  })
  const socketClosedForRecovery = await page.evaluate(() => {
    const socket = window.__firstAudioMatrix?.sockets.find(candidate =>
      candidate.url.includes('/ws/classroom?') && candidate.readyState === WebSocket.OPEN)
    if (!socket) return false
    socket.close()
    return true
  })
  expect(socketClosedForRecovery).toBe(true)
  await expect(page.getByText('课堂状态无法确认，为避免重复操作已停止本课。请返回课程表查看进度。')).toBeVisible()
  await expect(page.getByRole('button', { name: '重新连接课堂' })).toBeHidden()
  await expect(page.getByRole('button', { name: '返回课程表' })).toBeVisible()
  await expect.poll(() => terminalEnterAttempts).toBe(1)
  await page.waitForTimeout(1_200) // Outlast the first WS backoff and prove there is no second authorize/retry.
  expect(terminalEnterAttempts).toBe(1)
  const evidencePath = process.env.FIRST_AUDIO_MATRIX_EVIDENCE || ''
  expect(evidencePath).toBeTruthy()
  expect(observedSocket).toBeTruthy()
  await page.close()
  const wsClosed = await Promise.race([
    socketClosed,
    new Promise<boolean>(resolve => setTimeout(() => resolve(false), 5000)),
  ])
  expect(wsClosed).toBeTruthy()
  await writeFile(evidencePath, JSON.stringify({
    lesson_id: lessonId,
    generation,
    first_frame_ms: (firstAudioFrameAt as number) - enteredAt,
    audio_start_ms: (audioStartedAt as number) - enteredAt,
    websocket_closed: wsClosed,
    interrupted_after_start: true,
    delayed_onend_ignored: interruption.ttsFinishedSent === 0 && interruption.started === 1,
    playback_start_count_after_cancel: interruption.started,
    cancel_count_before_interruption: injected!.cancelCountBefore,
    cancel_count_after_interruption: interruption.cancelCount,
    terminal_error_rendered: terminalEnterAttempts === 1,
    retry_control_hidden: true,
  }), { encoding: 'utf8', flag: 'wx', mode: 0o600 })
  expect(generation).toBeGreaterThan(0) // binds browser evidence to controller metadata
})
