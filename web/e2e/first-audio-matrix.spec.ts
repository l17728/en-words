import { expect, test } from '@playwright/test'
import { performance } from 'node:perf_hooks'
import { writeFile } from 'node:fs/promises'

declare global {
  interface Window {
    __firstAudioMatrix?: { started: Array<{ text: string; at: number }> }
  }
}

const apiOrigin = process.env.FIRST_AUDIO_MATRIX_API_ORIGIN || ''
const lessonId = Number(process.env.FIRST_AUDIO_MATRIX_LESSON_ID || 0)
const generation = Number(process.env.FIRST_AUDIO_MATRIX_GENERATION || 0)
const username = process.env.FIRST_AUDIO_MATRIX_USERNAME || ''
const password = process.env.FIRST_AUDIO_MATRIX_PASSWORD || ''

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
  await page.exposeBinding('__matrixAudioStarted', () => {
    if (audioStartedAt === null) audioStartedAt = performance.now()
  })
  await page.addInitScript(({ token, role, nickname, userId }) => {
    localStorage.setItem('token', token)
    localStorage.setItem('role', role)
    localStorage.setItem('nickname', nickname)
    localStorage.setItem('userId', String(userId))

    const state = { started: [] as Array<{ text: string; at: number }> }
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
          utterance.onstart?.()
          state.started.push({ text: utterance.text, at: performance.now() })
          void (window as unknown as { __matrixAudioStarted?: () => Promise<void> }).__matrixAudioStarted?.()
        },
        cancel: () => undefined,
      },
    })
  }, {
    token: login.token,
    role: login.role,
    nickname: login.nickname,
    userId: login.user_id,
  })

  const frames: Array<Record<string, unknown>> = []
  let observedSocket = false
  const classroomSockets = new Set<object>()
  const closedClassroomSockets = new Set<object>()
  const isPlayback = (frame: Record<string, unknown>) => {
    const action = frame.action as { type?: string } | undefined
    return frame.type === 'ui_action' && action?.type === 'play_tts'
  }
  page.on('websocket', socket => {
    const url = new URL(socket.url())
    if (url.pathname !== '/ws/classroom' || Number(url.searchParams.get('lessonId')) !== lessonId) return
    observedSocket = true
    classroomSockets.add(socket)
    socket.on('close', () => closedClassroomSockets.add(socket))
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
  await expect.poll(() => audioStartedAt).not.toBeNull()
  const firstAudioStartedAt = capturedAudioStart()
  await page.evaluate(async () => {
    await (window as unknown as { __matrixAudioStarted?: () => Promise<void> }).__matrixAudioStarted?.()
  })
  expect(audioStartedAt).toBe(firstAudioStartedAt)

  const firstAudio = frames.find(isPlayback)
  expect(firstAudio).toBeTruthy()
  const action = firstAudio?.action as { word?: string } | undefined
  expect(action?.word).toBeTruthy()
  const firstFrameTime = capturedFirstFrame()
  const firstAudioTime = capturedAudioStart()
  expect(firstFrameTime - enteredAt).toBeLessThan(10_000)
  expect(firstAudioTime - enteredAt).toBeLessThan(10_000)
  await expect(page.getByRole('status', { name: '当前朗读字幕' }).getByText(action?.word || '', { exact: true })).toBeVisible()
  await expect.poll(() => page.evaluate(() => window.__firstAudioMatrix?.started[0]?.text || '')).toBe(action?.word)
  const evidencePath = process.env.FIRST_AUDIO_MATRIX_EVIDENCE || ''
  expect(evidencePath).toBeTruthy()
  expect(observedSocket).toBeTruthy()
  await page.close()
  await expect.poll(() => closedClassroomSockets.size, { timeout: 5000 }).toBe(classroomSockets.size)
  expect(classroomSockets.size).toBeGreaterThan(0)
  const wsClosed = closedClassroomSockets.size === classroomSockets.size
  await writeFile(evidencePath, JSON.stringify({
    lesson_id: lessonId,
    generation,
    first_frame_ms: firstFrameTime - enteredAt,
    audio_start_ms: firstAudioTime - enteredAt,
    websocket_closed: wsClosed,
  }), { encoding: 'utf8', flag: 'wx', mode: 0o600 })
  expect(generation).toBeGreaterThan(0) // binds browser evidence to controller metadata

  function capturedAudioStart(): number {
    if (audioStartedAt === null) throw new Error('first audio start timestamp was not captured')
    return audioStartedAt
  }

  function capturedFirstFrame(): number {
    if (firstAudioFrameAt === null) throw new Error('first audio frame timestamp was not captured')
    return firstAudioFrameAt
  }
})
