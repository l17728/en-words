import { expect, test } from '@playwright/test'
import { writeFile } from 'node:fs/promises'

declare global {
  interface Window {
    __firstAudioMatrix?: {
      entryAt: number | null
      frames: Array<{ type: string; word: string; at: number }>
      started: Array<{ text: string; at: number }>
    }
  }
}

const apiOrigin = process.env.FIRST_AUDIO_MATRIX_API_ORIGIN || ''
const lessonId = Number(process.env.FIRST_AUDIO_MATRIX_LESSON_ID || 0)
const generation = Number(process.env.FIRST_AUDIO_MATRIX_GENERATION || 0)
const username = process.env.FIRST_AUDIO_MATRIX_USERNAME || ''
const password = process.env.FIRST_AUDIO_MATRIX_PASSWORD || ''
const webBuildIdentity = process.env.FIRST_AUDIO_MATRIX_WEB_BUILD_IDENTITY || ''

test('FIRST_AUDIO_MATRIX_JOURNEY_V1 first entry reaches subtitle and first audio', async ({ page }) => {
  test.setTimeout(40_000)
  expect(new URL(apiOrigin).hostname).toBe('127.0.0.1')
  expect(lessonId).toBeGreaterThan(0)
  expect(generation).toBeGreaterThan(0)
  expect(username).toMatch(/^first-audio-[a-z0-9-]{1,32}$/)
  expect(password).toBeTruthy()
  expect(webBuildIdentity).toMatch(/^[0-9a-f]{64}$/)

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

  const settingsPath = `${apiOrigin}/api/v1/student/playback-settings`
  const settingsResponse = await page.request.get(settingsPath, {
    headers: { Authorization: `Bearer ${login.token}` },
    timeout: 5000,
  })
  expect(settingsResponse.ok()).toBeTruthy()
  const savedSettings = await settingsResponse.json()
  expect(savedSettings.code).toBe(0)
  expect(savedSettings.data.settings.showWordLeadMs).toBe(250)
  const invalidSettings = await page.request.post(settingsPath, {
    headers: { Authorization: `Bearer ${login.token}` },
    data: { ttsEnRepeat: 3, ttsZhRepeat: 1, ttsIntervalMs: 0, ttsZhIntervalMs: 0,
      ttsSwitchIntervalMs: 0, showWordLeadMs: 99 },
    timeout: 5000,
  })
  expect((await invalidSettings.json()).code).not.toBe(0)

  await page.addInitScript(({ token, role, nickname, userId }) => {
    localStorage.setItem('token', token)
    localStorage.setItem('role', role)
    localStorage.setItem('nickname', nickname)
    localStorage.setItem('userId', String(userId))

    const state = {
      entryAt: null as number | null,
      frames: [] as Array<{ type: string; word: string; at: number }>,
      started: [] as Array<{ text: string; at: number }>,
    }
    window.__firstAudioMatrix = state
    const NativeWebSocket = window.WebSocket
    class MatrixWebSocket extends NativeWebSocket {
      constructor(url: string | URL, protocols?: string | string[]) {
        super(url, protocols)
        this.addEventListener('message', event => {
          if (typeof event.data !== 'string') return
          try {
            const frame = JSON.parse(event.data) as {
              type?: string; phase?: string; word?: string; action?: { type?: string; word?: string }
            }
            const actionType = frame.action?.type
            const presentedWord = (frame.type === 'ui_action' && actionType === 'show_word') ||
              (frame.type === 'state_snapshot' && frame.phase === 'show_word' && typeof frame.word === 'string' && frame.word.length > 0)
            if (presentedWord) state.frames.push({ type: 'show_word', word: frame.action?.word || frame.word || '', at: performance.now() })
            if (frame.type === 'ui_action' && actionType === 'play_tts') {
              state.frames.push({ type: 'play_tts', word: frame.action?.word || '', at: performance.now() })
            }
          } catch { /* ignore classroom control frames */ }
        })
      }
    }
    Object.defineProperty(window, 'WebSocket', { configurable: true, writable: true, value: MatrixWebSocket })
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
          const onstart = utterance.onstart
          utterance.onstart = () => {
            state.started.push({ text: utterance.text, at: performance.now() })
            onstart?.()
          }
          utterance.onstart?.()
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
  const connectionDiagnostics: string[] = []
  let observedSocket = false
  const classroomSockets = new Set<object>()
  const closedClassroomSockets = new Set<object>()
  const isPlayback = (frame: Record<string, unknown>) => {
    const action = frame.action as { type?: string } | undefined
    return frame.type === 'ui_action' && action?.type === 'play_tts'
  }
  const isShowWord = (frame: Record<string, unknown>) => {
    const action = frame.action as { type?: string } | undefined
    return (frame.type === 'ui_action' && action?.type === 'show_word') ||
      (frame.type === 'state_snapshot' && frame.phase === 'show_word' && typeof frame.word === 'string' && frame.word.length > 0)
  }
  page.on('websocket', socket => {
    const url = new URL(socket.url())
    if (url.pathname !== '/ws/classroom' || Number(url.searchParams.get('lessonId')) !== lessonId) return
    observedSocket = true
    classroomSockets.add(socket)
    socket.on('socketerror', error => {
      const status = error.match(/(?:response|status)(?: code)?\D{0,8}(\d{3})/i)?.[1]
      connectionDiagnostics.push(status ? `ws_handshake_${status}` : 'ws_transport_error')
    })
    socket.on('close', () => closedClassroomSockets.add(socket))
    socket.on('framereceived', ({ payload }) => {
      try {
        const frame = JSON.parse(String(payload)) as Record<string, unknown>
        frames.push(frame)
      } catch { /* non-JSON control frames are irrelevant to the journey */ }
    })
  })
  page.on('requestfailed', request => {
    const url = new URL(request.url())
    if (url.pathname !== '/ws/classroom') return
    const failure = request.failure()?.errorText || ''
    const category = failure.includes('ERR_CONNECTION_REFUSED') ? 'connection_refused'
      : failure.includes('ERR_CONNECTION_RESET') ? 'connection_reset'
        : failure.includes('ERR_TIMED_OUT') ? 'timed_out' : 'request_failed'
    connectionDiagnostics.push(`ws_${category}`)
  })
  page.on('response', response => {
    const url = new URL(response.url())
    if (response.status() >= 400 && url.pathname.startsWith(`/api/v1/classroom/lessons/${lessonId}/`)) {
      const stage = url.pathname.endsWith('/enter') ? 'enter' : url.pathname.includes('/speech-materials/')
        ? 'speech_materials' : 'classroom_api'
      connectionDiagnostics.push(`${stage}_http_${response.status()}`)
    }
  })

  await page.goto('/')
  await page.evaluate(path => {
    const state = window.__firstAudioMatrix
    if (!state) throw new Error('matrix browser clock state is unavailable')
    state.entryAt = performance.now()
    window.history.pushState({}, '', path)
    window.dispatchEvent(new PopStateEvent('popstate'))
  }, `/classroom/${lessonId}?e2e=1`)
  await expect(page).toHaveURL(new RegExp(`/classroom/${lessonId}\\?e2e=1$`))
  await expect.poll(async () => {
    if (await page.getByText('已连接', { exact: true }).isVisible().catch(() => false)) return 'connected'
    return connectionDiagnostics[0] || (observedSocket ? 'ws_connecting' : 'ws_not_opened')
  }, { timeout: 15_000 }).toBe('connected')
  await expect.poll(() => frames.some(isShowWord), { timeout: 20_000 }).toBeTruthy()
  await expect.poll(() => frames.some(isPlayback), { timeout: 20_000 }).toBeTruthy()
  await expect.poll(() => page.evaluate(() => window.__firstAudioMatrix?.started.length || 0), { timeout: 20_000 }).toBeGreaterThan(0)

  const firstAudio = frames.find(isPlayback)
  expect(firstAudio).toBeTruthy()
  const action = firstAudio?.action as { word?: string } | undefined
  expect(action?.word).toBeTruthy()
  const browserTiming = await page.evaluate(() => {
    const state = window.__firstAudioMatrix
    if (!state || state.entryAt === null) throw new Error('browser-local entry time is missing')
    const showWord = state.frames.find(frame => frame.type === 'show_word')
    const playback = state.frames.find(frame => frame.type === 'play_tts')
    const started = state.started[0]
    if (!showWord || !playback || !started) throw new Error('browser-local first-audio endpoints are incomplete')
    return {
      showWordMs: showWord.at - state.entryAt,
      firstFrameMs: playback.at - state.entryAt,
      audioStartMs: started.at - state.entryAt,
      receiptToOnstartMs: started.at - playback.at,
      playbackWord: playback.word,
      startedText: started.text,
    }
  })
  expect(browserTiming.playbackWord).toBe(action?.word)
  expect(browserTiming.startedText).toBe(action?.word)
  expect(browserTiming.showWordMs).toBeGreaterThanOrEqual(0)
  expect(browserTiming.showWordMs).toBeLessThanOrEqual(browserTiming.firstFrameMs)
  expect(browserTiming.firstFrameMs).toBeLessThanOrEqual(browserTiming.audioStartMs)
  expect(browserTiming.audioStartMs).toBeLessThan(10_000)
  expect(browserTiming.receiptToOnstartMs).toBeGreaterThanOrEqual(0)
  await expect(page.getByRole('status', { name: '当前朗读字幕' }).getByText(action?.word || '', { exact: true })).toBeVisible()
  await expect.poll(() => page.evaluate(() => window.__firstAudioMatrix?.started[0]?.text || '')).toBe(action?.word)
  const evidencePath = process.env.FIRST_AUDIO_MATRIX_EVIDENCE || ''
  expect(evidencePath).toBeTruthy()
  expect(observedSocket).toBeTruthy()
  await page.getByRole('button', { name: /^退\s*出$/ }).click()
  const exitDialog = page.getByRole('dialog')
  await exitDialog.getByRole('button', { name: /^退\s*出$/ }).click()
  await expect(page).toHaveURL(/\/dashboard$/)
  await expect.poll(() => closedClassroomSockets.size, { timeout: 5000 }).toBe(classroomSockets.size)
  await page.close()
  expect(classroomSockets.size).toBeGreaterThan(0)
  const wsClosed = closedClassroomSockets.size === classroomSockets.size
  await writeFile(evidencePath, JSON.stringify({
    lesson_id: lessonId,
    generation,
    show_word_ms: browserTiming.showWordMs,
    first_frame_ms: browserTiming.firstFrameMs,
    audio_start_ms: browserTiming.audioStartMs,
    receipt_to_onstart_ms: browserTiming.receiptToOnstartMs,
    websocket_closed: wsClosed,
    web_build_identity: webBuildIdentity,
  }), { encoding: 'utf8', flag: 'wx', mode: 0o600 })
  expect(generation).toBeGreaterThan(0) // binds browser evidence to controller metadata

})
