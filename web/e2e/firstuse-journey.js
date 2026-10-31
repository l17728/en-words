import { test, expect } from '@playwright/test'
const configuredPlayback = process.env.E2E_PLAYBACK_SETTINGS === '1'

// No API setup: all mutations follow visible first-use/admin/student controls.
test('first use and complete classroom with ASR unavailable, typed repeat fallback, report and next lesson', async ({ page }) => {
  test.setTimeout(180_000)
  // Browser TTS is deterministic; local ASR is explicitly unavailable to exercise
  // the designed typed fallback. This does not measure real model/audio quality.
  let asrUnavailable = false
  await page.route('**/api/v1/providers', route => {
    asrUnavailable = true
    return route.fulfill({ status: 200, contentType: 'application/json',
      body: JSON.stringify({ code: 0, msg: 'ok', data: { local: true, asr: false, tts_zh: false, tts_en: false } }) })
  })
  await page.addInitScript(() => {
    const state = { spoken: [], completed: [], recognition: null, transcripts: [] }
    window.__mockClassroomSpeech = state
    class MockUtterance {
      constructor(text) { this.text = text }
    }
    const timers = new Set()
    Object.defineProperty(window, 'SpeechSynthesisUtterance', { configurable: true, value: MockUtterance })
    Object.defineProperty(window, 'speechSynthesis', { configurable: true, value: {
      getVoices: () => ['en-US', 'zh-CN'].map(lang => ({ name: `MOCK ${lang}`, lang, localService: true })),
      speak: utterance => {
        state.spoken.push({ text: utterance.text, lang: utterance.lang, at: performance.now() })
        utterance.onstart?.()
        // Each segment is below its own deadline, while four segments exceed
        // the old shared phase deadline. No segment may truncate.
        const timer = setTimeout(() => {
          timers.delete(timer)
          state.completed.push({ text: utterance.text, lang: utterance.lang, at: performance.now() })
          utterance.onend?.()
        }, 1600)
        timers.add(timer)
      },
      cancel: () => { for (const timer of timers) clearTimeout(timer); timers.clear() },
    } })
    class MockRecognition {
      start() { state.recognition = this }
      stop() { if (state.recognition === this) state.recognition = null }
      abort() { this.stop() }
    }
    window.SpeechRecognition = MockRecognition
    window.webkitSpeechRecognition = MockRecognition
  })
  // AntD's zero-width leaving icon can retain aria-label=loading after busy=false.
  // Require the actual button to be enabled and not loading; never force a busy action.
  const clickAdminAction = async label => {
    const button = page.getByRole('button', { name: new RegExp(`${label}$`) })
    await expect(button).toBeEnabled()
    await expect(button).not.toHaveClass(/\bant-btn-loading\b/)
    await button.click()
  }
  await page.goto('/login')
  await expect(page.getByText('首次使用：创建管理员账号')).toBeVisible()
  await page.getByLabel('用户名', { exact: true }).fill('firstuse-admin')
  await page.getByLabel('密码', { exact: true }).fill('fixture12345')
  const initialBooks = page.waitForResponse(r => r.url().endsWith('/api/v1/wordbooks') && r.request().method() === 'GET')
  await page.getByRole('button', { name: '创建管理员并登录' }).click()
  await expect(page).toHaveURL(/\/admin$/)
  expect((await (await initialBooks).json()).data, 'testmode must not seed production books').toEqual([])
  await page.getByLabel('学生用户名').fill('firstuse-student')
  await page.getByLabel('学生昵称').fill('首启学生')
  await page.getByLabel('初始密码').fill('fixture12345')
  await clickAdminAction('创建学生')
  await expect(page.getByText('学生已创建', { exact: true })).toBeVisible()
  await page.getByLabel('词库名称').fill('首启真实操作库')
  await clickAdminAction('创建词库')
  await expect(page.getByText('词库已创建', { exact: true })).toBeVisible()
  await page.getByLabel('词条内容').fill('cat\t猫\ndog\t狗\nowl\t猫头鹰')
  await clickAdminAction('导入词条')
  await expect(page.getByText('已导入 3 词，跳过 0 词')).toBeVisible()
  await clickAdminAction('发布词库')
  await expect(page.getByText('词库已发布', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: /^退\s*出$/ }).click()
  await expect(page.getByRole('button', { name: '开始学习' })).toBeEnabled()
  await expect(page.getByText('首次使用：创建管理员账号')).toHaveCount(0)
  await page.getByLabel('用户名', { exact: true }).fill('firstuse-student')
  await page.getByLabel('密码', { exact: true }).fill('fixture12345')
  await page.getByRole('button', { name: '开始学习' }).click()
  await expect(page.getByText(/首启学生，今天也想见叮当老师/)).toBeVisible()
  if (configuredPlayback) {
    await page.getByRole('button', { name: '学习设置', exact: true }).click()
    for (const [label, value] of [['英文朗读遍数', '2'], ['中文朗读遍数', '2'], ['英文重复间隔', '0.2'], ['中文重复间隔', '0.4'], ['中英文切换间隔', '0.6']]) {
      await page.getByLabel(label, { exact: true }).fill(value)
    }
    await page.route('**/api/v1/student/playback-settings', route => route.request().method() === 'POST' ? route.abort('failed') : route.continue(), { times: 1 })
    await page.getByRole('button', { name: /^(?:loading\s*)?保存设置$/ }).click()
    await expect(page.getByRole('main').getByRole('alert')).toBeVisible()
    await expect(page.getByText('朗读设置已保存，从下一课生效', { exact: true })).toHaveCount(0)
    await expect(page.getByLabel('英文朗读遍数', { exact: true })).toHaveValue('2')
    await expect(page.getByRole('button', { name: /^(?:loading\s*)?保存设置$/ })).toBeEnabled()
    await page.getByRole('button', { name: /^(?:loading\s*)?保存设置$/ }).click()
    await expect(page.getByText('朗读设置已保存，从下一课生效', { exact: true })).toBeVisible()
    await page.reload()
    await expect(page.getByLabel('英文朗读遍数', { exact: true })).toHaveValue('2')
    await expect(page.getByLabel('中文朗读遍数', { exact: true })).toHaveValue('2')
    await page.getByRole('button', { name: '返回课程表', exact: true }).click()
  }
  await page.getByRole('button', { name: /词库广场$/ }).click()
  const card = page.locator('.ant-card', { hasText: '首启真实操作库' })
  await card.getByRole('button', { name: '选择这个词库' }).click()
  await page.getByRole('button', { name: '开始摸底' }).click()
  await expect(page.getByRole('button', { name: 'cat', exact: true })).toBeVisible()
  await page.getByRole('button', { name: '完成摸底，生成学习计划' }).click()
  await expect(page.getByRole('dialog', { name: '摸底完成！' })).toBeVisible()
  await page.locator('.ant-modal-confirm .ant-btn-primary').click()
  await page.getByRole('button', { name: '查看 首启真实操作库 的课程' }).click()
  await expect(page.getByRole('button', { name: '进入课堂', exact: true })).toBeEnabled()
  await page.getByRole('button', { name: /我想说点啥/ }).click()
  await page.getByPlaceholder('例如：每次词太多了 / 这周三有事 / abandon 我老忘').fill('每次降到50个词')
  const result = page.waitForResponse(r => r.url().endsWith('/feedbacks') && r.request().method() === 'POST')
  await page.getByRole('button', { name: /^发\s*送$/ }).click()
  const feedback = (await (await result).json()).data
  expect(feedback.status).toBe('applied')
  expect(feedback.reply).toContain('50')
  await expect(page.getByText(feedback.reply, { exact: true }).first()).toBeVisible()

  const frames = []
  page.on('websocket', socket => socket.on('framereceived', ({ payload }) => {
    try { frames.push(JSON.parse(String(payload))) } catch { /* ignore non-JSON */ }
  }))
  // Real enter runs on the server; delay delivery longer than show_word's 800ms.
  // No audio segment may advance before the browser can establish its first WS.
  await page.route('**/api/v1/classroom/lessons/*/enter', async route => {
    const response = await route.fetch()
    await new Promise(resolve => setTimeout(resolve, 1500))
    await route.fulfill({ response })
  }, { times: 1 })
  await page.getByRole('button', { name: '进入课堂', exact: true }).click()
  await expect(page.getByText('已连接', { exact: true })).toBeVisible()
  await expect.poll(() => frames.find(f => f.type === 'state_snapshot')?.teacher_text).toBeTruthy()
  const opening = frames.find(f => f.type === 'state_snapshot').teacher_text
  await expect(page.getByText(opening, { exact: true })).toBeVisible()
  if (configuredPlayback) {
    // Another tab changes next-lesson settings while this classroom remains frozen.
    const settingsPage = await page.context().newPage()
    await settingsPage.goto('/settings')
    await settingsPage.getByLabel('英文朗读遍数', { exact: true }).fill('1')
    await settingsPage.getByLabel('中文朗读遍数', { exact: true }).fill('1')
    await settingsPage.getByRole('button', { name: '保存设置', exact: true }).click()
    await expect(settingsPage.getByText('朗读设置已保存，从下一课生效', { exact: true })).toBeVisible()
    await settingsPage.close()
  }
  const lessonPath = new URL(page.url()).pathname
  const postVoiceTranscript = async (text, suppliedContext = null) => page.evaluate(async ({ text, lessonPath, suppliedContext }) => {
    const headers = { 'Content-Type': 'application/json', Authorization: `Bearer ${localStorage.getItem('token') || ''}` }
    const apiLessonPath = lessonPath.replace(/^\/classroom\/(\d+)$/, '/classroom/lessons/$1')
    let context = suppliedContext
    if (!context) {
      const entered = await (await fetch(`/api/v1${apiLessonPath}/enter`, { method: 'POST', headers })).json()
      context = entered.data?.snapshot?.context
      if (entered.code !== 0 || !context) throw new Error('Could not read the current classroom turn')
    }
    const send = async (eventId, turnContext = context) => (await fetch(`/api/v1${apiLessonPath}/voice`, {
      method: 'POST', headers, body: JSON.stringify({ text, context: turnContext, event_id: eventId, submission_id: eventId }),
    })).json()
    const first = await send(`e2e-voice-${crypto.randomUUID()}`)
    if (first.code !== 0) throw new Error(`Voice transcript rejected: ${first.msg}`)
    return first
  }, { text, lessonPath, suppliedContext })
  const taught = []
  let narrationPass = 0
  const repeatWord = async (advanceToOther = true, entersQuiz = false) => {
    // Observe each declared media completion, including progress already received.
    // Four 1600ms segments + configured gaps do not share a single UI wait budget.
    for (let segment = 1; segment <= 4; segment++) {
      await expect.poll(() => page.evaluate(() => window.__mockClassroomSpeech.completed.length),
        { timeout: 30_000 }).toBeGreaterThanOrEqual(narrationPass * 4 + segment)
    }
    narrationPass++
    await expect(page.getByText('轮到你读了！大声一点没关系', { exact: true })).toBeVisible()
    expect(asrUnavailable).toBe(true)
    const word = await page.getByRole('button', { name: /^(cat|dog|owl)$/ }).textContent()
    const input = page.getByLabel('跟读文字输入框')
    await input.fill('banana')
    // Simulate a rapid duplicate submit; only the first request may advance the turn.
    await page.getByRole('button', { name: '提交跟读文字' }).click()
    await expect(page.getByText('未确认听到当前单词，请核对后重试，或输入“跳过”。', { exact: true })).toBeVisible()
    await expect(input).toHaveValue('banana')
    await expect(page.getByRole('button', { name: word, exact: true })).toBeVisible()
    await input.fill(word)
    await page.getByRole('button', { name: '提交跟读文字' }).evaluate(button => {
      button.click()
      button.click()
    })
    const words = ['cat', 'dog', 'owl']
    const expectedWord = advanceToOther ? words[(words.indexOf(word.trim()) + 1) % words.length] : word.trim()
    if (entersQuiz) await expect(page.getByLabel('答案输入框')).toBeVisible()
    else await expect(page.getByRole('button', { name: expectedWord, exact: true })).toBeVisible()
    return word.trim()
  }
  for (let i = 0; i < 3; i++) {
    // The production timer may start narration before this click; either is valid.
    const start = page.getByRole('button', { name: /^(cat|dog|owl)，点击开始学习$/ })
    if (await start.isVisible()) await start.click()
    taught.push(await repeatWord(i < 2, i === 2))
    await expect(page.getByText('轮到你读了！大声一点没关系', { exact: true })).toBeHidden()
    if (i === 0) expect(frames.some(f => f.action?.type === 'start_quiz')).toBe(false)
  }
  expect(new Set(taught)).toEqual(new Set(['cat', 'dog', 'owl']))
  await expect(page.getByLabel('答案输入框')).toBeVisible()
  await expect(page.getByText('说出中文意思，或确认认识/不会', { exact: true })).toBeVisible()
  const teachingSpeech = await page.evaluate(() => window.__mockClassroomSpeech.spoken)
  const completedSpeech = await page.evaluate(() => window.__mockClassroomSpeech.completed)
  expect(completedSpeech.map(({ at: _at, ...segment }) => segment)).toEqual(teachingSpeech.map(({ at: _at, ...segment }) => segment))
  for (const word of ['cat', 'dog', 'owl']) expect(teachingSpeech.filter(s => s.text === word && s.lang === 'en-US')).toHaveLength(configuredPlayback ? 2 : 3)
  expect(teachingSpeech.filter(s => s.lang === 'zh-CN')).toHaveLength(configuredPlayback ? 6 : 3)
  if (configuredPlayback) {
    expect(teachingSpeech.map(s => s.lang)).toEqual(Array(3).fill(['en-US', 'en-US', 'zh-CN', 'zh-CN']).flat())
    for (let offset = 0; offset < 12; offset += 4) {
      for (const [index, minimum] of [[1, 200], [2, 600], [3, 400]]) {
        expect(teachingSpeech[offset + index].at - completedSpeech[offset + index - 1].at).toBeGreaterThanOrEqual(minimum - 15)
      }
    }
  }
  await expect(page.getByLabel('答案输入框')).toBeEnabled()
  await page.getByLabel('答案输入框').fill('香蕉')
  // The real answer timeout and teacher tool invocation can take slightly over 30s on a cold first-use journey.
  await expect.poll(() => frames.some(f => f.action?.type === 'nudge_answer' && f.context), { timeout: 45_000 }).toBe(true)
  await expect(page.getByLabel('答案输入框')).toHaveValue('香蕉')
  await expect(page.getByRole('button', { name: /答/ })).toBeEnabled()
  await postVoiceTranscript('不会')
  await expect.poll(() => frames.some(f => f.phase === 'reteach_show' || f.action?.type === 'reteach')).toBe(true)
  await expect.poll(() => frames.some(f => f.action?.type === 'say' && f.action.category === 'correct')).toBe(true)
  const correction = frames.find(f => f.action?.type === 'say' && f.action.category === 'correct').action.text
  await expect(page.getByText(correction, { exact: true })).toBeVisible()
  await repeatWord(false)
  // A second exact “不会” uses the final chance and advances without another reteach.
  const retryWord = (await page.getByRole('button', { name: /^(cat|dog|owl)$/ }).textContent()).trim()
  const beforeNegativeRetry = frames.length
  await postVoiceTranscript('不会')
  await expect.poll(() => frames.slice(beforeNegativeRetry).some(f => f.action?.type === 'say_hint'))
    .toBe(true)
  const quizWordButton = page.getByRole('button', { name: /^(cat|dog|owl)$/ })
  await expect.poll(async () => (await quizWordButton.textContent())?.trim()).not.toBe(retryWord)
  // A positive spoken self-report passes deterministically; explicit skip on the
  // remaining word records one wrong result and advances exactly once.
  const reviewSpeechBefore = await page.evaluate(() => window.__mockClassroomSpeech.completed.length)
  await expect(page.getByLabel('答案输入框')).toBeVisible()
  const positiveWord = (await quizWordButton.textContent()).trim()
  const beforePositive = frames.length
  await postVoiceTranscript('认识')
  await expect.poll(() => frames.slice(beforePositive).filter(f => f.type === 'judge_result').length).toBe(1)
  await expect.poll(async () => (await quizWordButton.textContent())?.trim()).not.toBe(positiveWord)
  const beforeSkip = frames.filter(f => f.type === 'judge_result').length
  const skipContext = await page.evaluate(async lessonPath => {
    const headers = { Authorization: `Bearer ${localStorage.getItem('token') || ''}` }
    const apiLessonPath = lessonPath.replace(/^\/classroom\/(\d+)$/, '/classroom/lessons/$1')
    const entered = await (await fetch(`/api/v1${apiLessonPath}/enter`, { method: 'POST', headers })).json()
    return entered.data?.snapshot?.context
  }, lessonPath)
  await page.getByLabel('答案输入框').fill('跳过')
  await page.getByRole('button', { name: /^回\s*答$/ }).click()
  await expect.poll(() => frames.filter(f => f.type === 'judge_result').length).toBe(beforeSkip)
  const lateSkip = await postVoiceTranscript('跳过', skipContext)
  expect(lateSkip.data?.frames?.some(frame => frame.type === 'event_rejected')).toBe(true)
  // A word answered incorrectly once remains in the lightweight review, even
  // after the retry succeeds. The explicit skip adds a second review item.
  // Wait for all eight playback segments before asserting the lesson summary.
  await expect.poll(() => page.evaluate(before => window.__mockClassroomSpeech.completed.length - before, reviewSpeechBefore),
    { timeout: 60_000 }).toBeGreaterThanOrEqual(8)
  await expect(page.getByText('下课啦！叮当老师的小结')).toBeVisible()
  await expect.poll(() => frames.filter(f => f.action?.type === 'say' && f.action.category === 'closing').length).toBe(1)
  const closing = frames.filter(f => f.action?.type === 'say' && f.action.category === 'closing')
  expect(closing).toHaveLength(1)
  expect(frames.findIndex(f => f.type === 'lesson_summary')).toBeLessThan(frames.indexOf(closing[0]))
  await expect(page.getByText(closing[0].action.text, { exact: true })).toBeVisible()
  await expect(page.getByText(/首答率 33%/)).toBeVisible()
  await expect(page.getByText(/补学 2 轮/)).toBeVisible()
  await page.getByRole('button', { name: '回课程表', exact: true }).click()
  await page.getByRole('button', { name: '查看 首启真实操作库 的课程' }).click()
  const reportButton = page.getByRole('button', { name: /查看第 \d+ 课课报/ })
  await expect(reportButton).toHaveCount(1)
  await reportButton.click()
  await expect(page.getByText('本课 3 词，答对 1 词，首次正确率 33%')).toBeVisible()
  await page.getByRole('dialog').getByRole('button', { name: /close|关闭/i }).click()
  await page.getByRole('button', { name: /学习统计$/ }).click()
  await expect(page.locator('[aria-label="已学词数：3"]')).toBeVisible()
  await expect(page.locator('[aria-label="已完成课程：1"]')).toBeVisible()
  await expect(page.getByRole('cell', { name: '33%', exact: true })).toBeVisible()
  await page.reload()
  await expect(page.locator('[aria-label="已学词数：3"]')).toBeVisible()
  await expect(page.getByRole('cell', { name: '33%', exact: true })).toBeVisible()
  await page.getByRole('button', { name: '返回课程表', exact: true }).click()
  await page.getByRole('button', { name: '查看 首启真实操作库 的课程' }).click()
  const previousSpeechCount = await page.evaluate(() => window.__mockClassroomSpeech.spoken.length)
  await page.getByRole('button', { name: '进入课堂', exact: true }).click()
  await expect(page).not.toHaveURL(new RegExp(`${lessonPath}$`))
  await expect(page.getByText('已连接', { exact: true })).toBeVisible()
  if (configuredPlayback) {
    await expect(page.getByText('轮到你读了！大声一点没关系', { exact: true })).toBeVisible()
    const nextSpeech = await page.evaluate(offset => window.__mockClassroomSpeech.spoken.slice(offset), previousSpeechCount)
    expect(nextSpeech.map(s => s.lang)).toEqual(['en-US', 'zh-CN'])
  }
  // Preserve the historical fulljourney notification and logout assertions.
  await page.getByRole('button', { name: /^退\s*出$/ }).click()
  await page.getByRole('dialog').getByRole('button', { name: /^退\s*出$/ }).click()
  await expect(page).toHaveURL(/\/dashboard$/)
  await page.getByRole('button', { name: /通知/ }).click()
  await expect(page.locator('.ant-card', { hasText: '通知' }).getByText(/课报/).first()).toBeVisible()
  await page.getByRole('button', { name: /^退\s*出$/ }).click()
  await expect(page).toHaveURL(/\/login$/)
  expect(await page.evaluate(() => localStorage.getItem('token'))).toBeNull()

})
