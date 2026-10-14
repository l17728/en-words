// API fixture setup is deliberate. These are regression journeys, not C01's
// complete first-use UI acceptance or C07's real model/audio acceptance.
import { test, expect } from '@playwright/test'
test.describe.configure({ mode: 'serial' })

const password = 'fixture12345'
let lessonID
let fixtureBookID
let otherStudentID

async function api(request, path, token, data, method = 'POST') {
  const response = await request.fetch(`/api/v1${path}`, {
    method,
    headers: token ? { Authorization: `Bearer ${token}` } : {},
    ...(data === undefined ? {} : { data }),
  })
  const body = await response.json()
  expect(body.code, `${method} ${path}: ${JSON.stringify(body)}`).toBe(0)
  return body.data
}

async function login(page, user) {
  await page.goto('/login')
  await page.getByLabel('用户名', { exact: true }).fill(user)
  await page.getByLabel('密码', { exact: true }).fill(password)
  await page.getByRole('button', { name: '开始学习' }).click()
  await expect(page.getByText('今天也想见叮当老师').first()).toBeVisible()
}

test.beforeAll(async ({ request }) => {
  const status = await api(request, '/bootstrap/status', null, undefined, 'GET')
  expect(status.needed, 'config must launch a fresh isolated database').toBe(true)
  await api(request, '/bootstrap', null, { username: 'closure-admin', password, nickname: 'Fixture admin' })
  const admin = await api(request, '/auth/login', null, { username: 'closure-admin', password })
  expect(await api(request, '/wordbooks', admin.token, undefined, 'GET'), 'testmode must not import production seed books').toEqual([])
  for (const username of ['closure-a', 'closure-b']) {
    await api(request, '/admin/users', admin.token, { username, password, nickname: username, role: 'student' })
  }
  otherStudentID = (await api(request, '/auth/login', null, { username: 'closure-b', password })).user_id
  const book = await api(request, '/wordbooks', admin.token, { name: 'Closure fixture', grade: 'g7', description: 'isolated regression' })
  fixtureBookID = book.ID
  await api(request, `/wordbooks/${book.ID}/import/commit`, admin.token, { entries: [{ word: 'cat', meanings: ['猫'] }, { word: 'dog', meanings: ['狗'] }] })
  await api(request, `/wordbooks/${book.ID}/publish`, admin.token, {})
  const owner = await api(request, '/auth/login', null, { username: 'closure-a', password })
  await api(request, `/student/books/${book.ID}/enable`, owner.token, {})
  await api(request, `/student/books/${book.ID}/placement`, owner.token, { knownWordIds: [], finish: true })
  const lessons = await api(request, `/student/lessons?bookId=${book.ID}`, owner.token, undefined, 'GET')
  lessonID = lessons.find((lesson) => lesson.Status === 'scheduled').ID
})

test('two students: foreign classroom navigation is denied without damaging owner session', async ({ browser, baseURL }) => {
  const owner = await browser.newContext({ baseURL })
  const other = await browser.newContext({ baseURL })
  try {
    const ownerPage = await owner.newPage()
    const otherPage = await other.newPage()
    await Promise.all([login(ownerPage, 'closure-a'), login(otherPage, 'closure-b')])
    const attempts = await ownerPage.evaluate(async studentID => {
      const calls = [
        { name: 'memory.append', args: { student_id: studentID, scope: 'student_profile', kind: 'promise', content: 'forged task', importance: 3 } },
        { name: 'config.set_student', args: { student_id: studentID, key: 'wordsPerLesson', value: 100, reason: 'forged task' } },
      ]
      return Promise.all(calls.map(async body => {
        const response = await fetch('/api/v1/agent/tools/call', { method: 'POST', headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${localStorage.getItem('token')}` }, body: JSON.stringify(body) })
        return response.json()
      }))
    }, otherStudentID)
    for (const result of attempts) expect(result.code).not.toBe(0)
    await ownerPage.goto(`/classroom/${lessonID}?e2e=1`)
    await expect(ownerPage.getByText('已连接', { exact: true })).toBeVisible()
    const denied = otherPage.waitForResponse((response) => response.url().endsWith(`/lessons/${lessonID}/enter`))
    await otherPage.goto(`/classroom/${lessonID}?e2e=1`)
    expect((await (await denied).json()).code).not.toBe(0)
    await expect(otherPage.getByText('连接已停止', { exact: true })).toBeVisible()
    await expect(otherPage.getByRole('button', { name: /^(cat|dog)$/ })).toHaveCount(0)
    await otherPage.getByRole('button', { name: '返回课程表', exact: true }).click()
    await expect(otherPage).toHaveURL(/\/dashboard$/)
    await expect(otherPage.getByText('下课啦！叮当老师的小结')).toHaveCount(0)
    await expect(ownerPage.getByText('已连接', { exact: true })).toBeVisible()
  } finally {
    await Promise.all([owner.close(), other.close()])
  }
})

test('feedback UI shows the actual applied quantity and notification body', async ({ page }) => {
  await login(page, 'closure-b')
  await page.getByRole('button', { name: /我想说点啥/ }).click()
  // B starts at 60: relative reduction must yield 50, not 90 from A's rejected 100.
  await page.getByPlaceholder('例如：每次词太多了 / 这周三有事 / abandon 我老忘').fill('每次词太多了')
  const result = page.waitForResponse((response) => response.url().endsWith('/feedbacks') && response.request().method() === 'POST')
  await page.getByRole('button', { name: /^发\s*送$/ }).click()
  const feedback = (await (await result).json()).data
  expect(feedback.status).toBe('applied')
  expect(feedback.reply).toContain('50')
  await expect(page.getByText(feedback.reply, { exact: true }).first()).toBeVisible()
  await page.reload()
  await expect(page.getByText('反馈处理结果', { exact: true }).first()).toBeVisible()
  await expect(page.getByText(feedback.reply, { exact: true }).first()).toBeVisible()
})


test('classroom rejects a browser event replay across WS and REST', async ({ page, request }) => {
  await page.addInitScript(() => {
    window.__delayProbe = { started: [] }
    class Utterance { constructor(text) { this.text = text } }
    let timer
    window.SpeechSynthesisUtterance = Utterance
    Object.defineProperty(window, 'speechSynthesis', { configurable: true, value: {
      getVoices: () => ['en-US', 'zh-CN'].map(lang => ({ name: `MOCK ${lang}`, lang, localService: true })),
      speak: utterance => {
        window.__delayProbe.started.push({ text: utterance.text, at: performance.now() })
        utterance.onstart?.()
        timer = setTimeout(() => utterance.onend?.(), 30)
      },
      cancel: () => clearTimeout(timer),
    } })
  })
  await login(page, 'closure-b')
  await page.getByRole('button', { name: '学习设置', exact: true }).click()
  await page.getByLabel('英文重复间隔', { exact: true }).fill('10')
  await page.getByRole('button', { name: '保存设置', exact: true }).click()
  await expect(page.getByText('朗读设置已保存，从下一课生效', { exact: true })).toBeVisible()
  const student = await api(request, "/auth/login", null, { username: "closure-b", password })
  await api(request, `/student/books/${fixtureBookID}/enable`, student.token, {})
  await api(request, `/student/books/${fixtureBookID}/placement`, student.token, { knownWordIds: [], finish: true })
  const lessons = await api(request, `/student/lessons?bookId=${fixtureBookID}`, student.token, undefined, "GET")
  const replayLessonID = lessons.find(l => l.Status === "scheduled").ID
  let clicked
  const sent = []
  const received = []
  page.on('websocket', socket => {
    socket.on('framesent', frame => {
      try { const event = JSON.parse(String(frame.payload)); sent.push(event); if (event.type === 'student_event' && !clicked) clicked = event } catch { /* protocol heartbeat */ }
    })
    socket.on('framereceived', frame => { try { received.push(JSON.parse(String(frame.payload))) } catch { /* protocol heartbeat */ } })
  })
  await page.goto(`/classroom/${replayLessonID}?e2e=1`)
  await expect(page.getByText('已连接', { exact: true })).toBeVisible()
  // Auto-play can already have advanced show_word by the time connection UI renders.
  // Replay the first actual browser receipt instead of clicking an expired phase.
  await expect.poll(() => clicked?.context?.epoch).toBeTruthy()
  const replay = await page.evaluate(async ({ id, payload }) => {
    const response = await fetch(`/api/v1/classroom/lessons/${id}/event`, {
      method: 'POST', headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${localStorage.getItem('token')}` }, body: JSON.stringify(payload),
    })
    return response.json()
  }, { id: replayLessonID, payload: clicked })
  expect(replay.code).toBe(0)
  expect(replay.data[0].type).toBe('event_rejected')
  expect(replay.data[1].type).toBe('state_snapshot')
  await expect(page.getByText('已连接', { exact: true })).toBeVisible()
  await expect.poll(() => received.some(f => f.action?.type === 'play_tts' && f.action.delay_before_ms === 10000)).toBe(true)
  expect(await page.evaluate(() => window.__delayProbe.started.length)).toBe(1)
  const receipts = sent.filter(f => f.type === 'student_event' && f.event === 'tts_finished').length
  await page.getByRole('button', { name: /^退\s*出$/ }).click()
  await page.getByRole('dialog').getByRole('button', { name: /^退\s*出$/ }).click()
  await expect(page).toHaveURL(/\/dashboard$/)
  // Deliberately cross the canceled timer's full deadline; no ghost speech/receipt.
  await page.waitForTimeout(10_200)
  expect(await page.evaluate(() => window.__delayProbe.started.length)).toBe(1)
  expect(sent.filter(f => f.type === 'student_event' && f.event === 'tts_finished')).toHaveLength(receipts)
})

test('student statistics empty state recovers from a real request failure', async ({ page }) => {
  await login(page, 'closure-b')
  await page.route('**/api/v1/student/stats', route => route.abort('failed'))
  await page.getByRole('button', { name: /学习统计$/ }).click()
  await expect(page.getByText('学习统计加载失败', { exact: true })).toBeVisible()
  await expect(page.getByText('还没有已完成的课程，完成第一课后即可查看记录。')).toHaveCount(0)
  await page.unroute('**/api/v1/student/stats')
  await page.getByRole('button', { name: /^重\s*试$/ }).click()
  await expect(page.getByText('还没有已完成的课程，完成第一课后即可查看记录。')).toBeVisible()
  await expect(page.locator('[aria-label="已完成课程：0"]')).toBeVisible()
  await expect(page.locator('[aria-label="已学词数：0"]')).toBeVisible()
  await page.reload()
  await expect(page.locator('[aria-label="已完成课程：0"]')).toBeVisible()
})

test('playback settings remain private to their student after relogin', async ({ page }) => {
  await login(page, 'closure-a')
  await page.getByRole('button', { name: '学习设置', exact: true }).click()
  await expect(page.getByLabel('英文重复间隔', { exact: true })).toHaveValue('1.5')
  await expect(page.getByLabel('英文朗读遍数', { exact: true })).toHaveValue('3')
  await page.goto('/dashboard')
  await page.getByRole('button', { name: /^退\s*出$/ }).click()
  await login(page, 'closure-b')
  await page.getByRole('button', { name: '学习设置', exact: true }).click()
  await expect(page.getByLabel('英文重复间隔', { exact: true })).toHaveValue('10.0')
  await page.reload()
  await expect(page.getByLabel('英文重复间隔', { exact: true })).toHaveValue('10.0')
})

test('injected pause/resume protocol cancels delay and deduplicates recovery speech', async ({ page, request }) => {
  // Protocol fixture only: Go FakeClock tests establish real session-timeout facts.
  const admin = await api(request, '/auth/login', null, { username: 'closure-admin', password })
  await api(request, '/admin/users', admin.token, { username: 'closure-pause', password, nickname: 'pause student', role: 'student' })
  const student = await api(request, '/auth/login', null, { username: 'closure-pause', password })
  await api(request, '/student/playback-settings', student.token, { ttsEnRepeat: 3, ttsZhRepeat: 1, ttsIntervalMs: 10000, ttsZhIntervalMs: 1500, ttsSwitchIntervalMs: 1500 })
  await api(request, `/student/books/${fixtureBookID}/enable`, student.token, {})
  await api(request, `/student/books/${fixtureBookID}/placement`, student.token, { knownWordIds: [], finish: true })
  const lessons = await api(request, `/student/lessons?bookId=${fixtureBookID}`, student.token, undefined, 'GET')
  const id = lessons.find(lesson => lesson.Status === 'scheduled').ID
  await page.addInitScript(() => {
    window.__pauseProbe = []
    class Utterance { constructor(text) { this.text = text } }
    let timer
    window.SpeechSynthesisUtterance = Utterance
    Object.defineProperty(window, 'speechSynthesis', { configurable: true, value: {
      getVoices: () => ['en-US', 'zh-CN'].map(lang => ({ name: `MOCK ${lang}`, lang, localService: true })),
      speak: utterance => {
        window.__pauseProbe.push(utterance.text)
        utterance.onstart?.()
        timer = setTimeout(() => utterance.onend?.(), 30)
      }, cancel: () => clearTimeout(timer),
    } })
  })
  let pausedSegment
  let browserSocket
  let acknowledgements = 0
  await page.routeWebSocket('**/ws/classroom?*', socket => {
    browserSocket = socket
    const server = socket.connectToServer()
    socket.onMessage(message => {
      const event = JSON.parse(String(message))
      if (event.event === 'tts_finished') acknowledgements++
      if (!pausedSegment) server.send(message)
    })
    server.onMessage(message => {
      if (pausedSegment) return
      const frame = JSON.parse(String(message))
      socket.send(message)
      if (frame.action?.type === 'play_tts' && frame.action.delay_before_ms === 10000) {
        pausedSegment = frame
        socket.send(JSON.stringify({ ...frame, seq: frame.seq + 1, action: { type: 'lesson_paused' } }))
      }
    })
  })
  await login(page, 'closure-pause')
  await page.goto(`/classroom/${id}?e2e=1`)
  await expect(page.getByText('课堂已暂停', { exact: true })).toBeVisible()
  const receiptsBefore = acknowledgements
  await page.waitForTimeout(10_200)
  expect(await page.evaluate(() => window.__pauseProbe.length)).toBe(1)
  expect(acknowledgements).toBe(receiptsBefore)
  let attempts = 0
  await page.route(`**/api/v1/classroom/lessons/${id}/resume`, async route => {
    if (++attempts === 1) return route.abort('failed')
    const show = { ...pausedSegment, seq: pausedSegment.seq + 2, action: { type: 'show_word', word: pausedSegment.action.word } }
    const play = { ...pausedSegment, seq: pausedSegment.seq + 3, action: { ...pausedSegment.action, delay_before_ms: 200 } }
    browserSocket.send(JSON.stringify(show))
    browserSocket.send(JSON.stringify(play))
    await new Promise(resolve => setTimeout(resolve, 350))
    browserSocket.send(JSON.stringify({ ...play, seq: play.seq + 1, action: { type: 'lesson_paused' } }))
    await new Promise(resolve => setTimeout(resolve, 30))
    await route.fulfill({ json: { code: 0, data: [show, play] } })
  })
  const resumeButton = page.getByRole('button', { name: /^(?:loading\s*)?继续课堂$/ })
  await resumeButton.click()
  await expect(page.getByText('Failed to fetch', { exact: true })).toBeVisible()
  await expect(page.getByText('课堂已暂停', { exact: true })).toBeVisible()
  const lateResponse = page.waitForResponse(response => response.url().endsWith(`/lessons/${id}/resume`))
  await expect(resumeButton).toBeEnabled()
  await resumeButton.click()
  await expect(page.getByText('课堂已暂停', { exact: true })).toHaveCount(0)
  await expect.poll(() => page.evaluate(() => window.__pauseProbe.length)).toBe(2)
  await expect.poll(() => acknowledgements).toBe(receiptsBefore + 1)
  await lateResponse
  await expect(page.getByText('课堂已暂停', { exact: true })).toBeVisible()
  await expect(resumeButton).toBeEnabled()
  expect(await page.evaluate(() => window.__pauseProbe.length)).toBe(2)
})
