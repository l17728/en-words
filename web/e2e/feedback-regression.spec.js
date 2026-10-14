import { test, expect } from '@playwright/test'
import { spawnSync } from 'node:child_process'

test.describe.configure({ mode: 'serial' })
const password = 'fixture12345'
let bookID
const users = {}

async function api(request, path, token, data, method = 'POST') {
  const response = await request.fetch(`/api/v1${path}`, { method, headers: token ? { Authorization: `Bearer ${token}` } : {}, ...(data === undefined ? {} : { data }) })
  const body = await response.json()
  expect(body.code, `${method} ${path}`).toBe(0)
  return body.data
}
async function login(page, user) {
  await page.goto('/login')
  await page.getByLabel('用户名', { exact: true }).fill(user)
  await page.getByLabel('密码', { exact: true }).fill(password)
  await page.getByRole('button', { name: '开始学习', exact: true }).click()
  await expect(page).toHaveURL(user === 'feedback-admin' ? /\/admin$/ : /\/dashboard$/)
}
async function send(page, text) {
  await page.getByRole('button', { name: /我想说点啥/ }).click()
  await page.getByPlaceholder('例如：每次词太多了 / 这周三有事 / abandon 我老忘').fill(text)
  const response = page.waitForResponse(r => r.url().endsWith('/feedbacks') && r.request().method() === 'POST')
  await page.getByRole('button', { name: /^发\s*送$/ }).click()
  const record = (await (await response).json()).data
  await expect(page.locator(`section[aria-label="反馈结果 ${record.submission_id}"]`)).toBeVisible()
  return record
}
function sql(testInfo, statement) {
  const file = process.env.E2E_FEEDBACK_DB
  if (!file || !file.includes('.test-artifacts') || !file.endsWith('test.db')) throw new Error('isolated test DB path unavailable')
  const result = spawnSync('python', ['-c', 'import sqlite3,sys; c=sqlite3.connect(sys.argv[1],timeout=10); c.executescript(sys.argv[2]); c.close()', file, statement], { encoding: 'utf8' })
  expect(result.status, result.stderr).toBe(0)
}
function studentResources(username) {
  const file = process.env.E2E_FEEDBACK_DB
  if (!file || !file.includes('.test-artifacts') || !file.endsWith('test.db')) throw new Error('isolated test DB path unavailable')
  const script = "import sqlite3,json,sys; c=sqlite3.connect(sys.argv[1],timeout=10); sid=c.execute('SELECT id FROM users WHERE username=?',(sys.argv[2],)).fetchone()[0]; print(json.dumps({t:c.execute('SELECT * FROM '+t+' WHERE student_id=? ORDER BY id',(sid,)).fetchall() for t in ['memory_cards','config_overrides']})); c.close()"
  const result = spawnSync('python', ['-c', script, file, username], { encoding: 'utf8' })
  expect(result.status, result.stderr).toBe(0)
  return JSON.parse(result.stdout)
}

test.beforeAll(async ({ request }) => {
  expect((await api(request, '/bootstrap/status', null, undefined, 'GET')).needed).toBe(true)
  await api(request, '/bootstrap', null, { username: 'feedback-admin', password, nickname: 'feedback admin' })
  const admin = await api(request, '/auth/login', null, { username: 'feedback-admin', password })
  const book = await api(request, '/wordbooks', admin.token, { name: 'Feedback fixture', grade: 'g7' })
  bookID = book.ID
  await api(request, `/wordbooks/${bookID}/import/commit`, admin.token, { entries: [{ word: 'cat', meanings: ['猫'] }, { word: 'dog', meanings: ['狗'] }] })
  await api(request, `/wordbooks/${bookID}/publish`, admin.token, {})
  for (const suffix of ['a', 'b', 'fault', 'voice', 'missing', 'switch', 'clarify', 'quota']) {
    const username = `feedback-${suffix}`
    await api(request, '/admin/users', admin.token, { username, password, nickname: username, role: 'student' })
    const user = await api(request, '/auth/login', null, { username, password })
    users[suffix] = user
    await api(request, `/student/books/${bookID}/enable`, user.token, {})
    await api(request, `/student/books/${bookID}/placement`, user.token, { knownWordIds: [], finish: true })
  }
})

async function voiceFixture(page) {
  await page.addInitScript(() => {
    class Utterance { constructor(text) { this.text = text } }
    let timer
    window.SpeechSynthesisUtterance = Utterance
    Object.defineProperty(window, 'speechSynthesis', { configurable: true, value: {
      getVoices: () => ['en-US', 'zh-CN'].map(lang => ({ name: `MOCK ${lang}`, lang, localService: true })),
      speak: utterance => { utterance.onstart?.(); timer = setTimeout(() => utterance.onend?.(), 20) },
      cancel: () => clearTimeout(timer),
    } })
    window.SpeechRecognition = class {
      start() { window.__feedbackRecognizer = this; this.onstart?.() }
      stop() { if (window.__feedbackRecognizer === this) window.__feedbackRecognizer = null }
    }
  })
}
async function enterVoice(page, request, suffix) {
  const lessons = await api(request, `/student/lessons?bookId=${bookID}`, users[suffix].token, undefined, 'GET')
  await login(page, `feedback-${suffix}`)
  await page.goto(`/classroom/${lessons.find(row => row.Status === 'scheduled').ID}`)
  await expect.poll(() => page.evaluate(() => !!window.__feedbackRecognizer)).toBe(true)
}

test('voice feedback survives a lost WS reply with its complete original text', async ({ page, request }) => {
  await voiceFixture(page)
  let submission
  let dropped = 0
  let invalidRejected = 0
  await page.routeWebSocket('**/ws/classroom?*', socket => {
    const server = socket.connectToServer()
    socket.onMessage(message => {
      const frame = JSON.parse(String(message))
      if (frame.type === 'voice_text') {
        submission = frame.submission_id
        server.send(JSON.stringify({ ...frame, event_id: `${frame.event_id}-invalid`, submission_id: null }))
      }
      server.send(message)
    })
    server.onMessage(message => {
      if (JSON.parse(String(message)).reason === 'invalid_envelope') { invalidRejected++; return }
      if (JSON.parse(String(message)).type === 'feedback_reply') { dropped++; return }
      socket.send(message)
    })
  })
  await enterVoice(page, request, 'voice')
  const body = '我要投诉老师讲得不好，例子保留 3.5、A/B 和标点。' + '这是一段需要完整保留的具体教学意见。'.repeat(8)
  await page.evaluate(text => window.__feedbackRecognizer.onresult({ results: [[{ transcript: `反馈${text}` }]] }), body)
  await expect.poll(() => submission).toBeTruthy()
  const panel = page.locator(`section[aria-label="反馈结果 ${submission}"]`)
  await expect(panel).toContainText('已转交管理员')
  expect(dropped).toBe(1)
  expect(invalidRejected).toBe(1)
  const record = await api(request, `/feedbacks/submissions/${submission}`, users.voice.token, undefined, 'GET')
  expect(record.text).toBe(body)
  const history = await api(request, '/feedbacks/mine', users.voice.token, undefined, 'GET')
  expect(history).toHaveLength(1)
  expect(history[0].submission_id).toBe(submission)
})

test('undelivered voice feedback needs confirmation and ignores older feedback versions', async ({ page, request }) => {
  await voiceFixture(page)
  let submission
  let browserSocket
  await page.routeWebSocket('**/ws/classroom?*', socket => {
    browserSocket = socket
    const server = socket.connectToServer()
    socket.onMessage(message => {
      const frame = JSON.parse(String(message))
      if (frame.type === 'voice_text') {
        submission = frame.submission_id
        socket.send(JSON.stringify({ type: 'event_rejected', event_id: frame.event_id, submission_id: submission, error_code: 'stale_context' }))
        return // Protocol fixture: the server never received this voice event.
      }
      server.send(message)
    })
    server.onMessage(message => socket.send(message))
  })
  await enterVoice(page, request, 'missing')
  await page.evaluate(() => window.__feedbackRecognizer.onresult({ results: [[{ transcript: '反馈每次词太多了' }]] }))
  const dialog = page.getByRole('dialog', { name: '反馈未送达' })
  await expect(dialog).toBeVisible()
  expect((await api(request, '/feedbacks/mine', users.missing.token, undefined, 'GET')).length).toBe(0)
  await dialog.getByRole('button', { name: '确认提交反馈' }).click()
  const panel = page.locator(`section[aria-label="反馈结果 ${submission}"]`)
  await expect(panel).toContainText('50')
  const record = await api(request, `/feedbacks/submissions/${submission}`, users.missing.token, undefined, 'GET')
  browserSocket.send(JSON.stringify({ type: 'feedback_reply', seq: 0, context: { epoch: 'obsolete' }, feedback: { ...record, version: record.version - 1, status: 'processing', reply: 'OLD VERSION MUST NOT APPEAR' } }))
  await expect(panel).toContainText('50')
  await expect(page.getByText('OLD VERSION MUST NOT APPEAR')).toHaveCount(0)
})

test('processing polling resumes after a transient query failure', async ({ page }) => {
  await login(page, 'feedback-a')
  let real
  let queries = 0
  await page.route('**/api/v1/feedbacks', async route => {
    const response = await route.fetch()
    const envelope = await response.json()
    real = envelope.data
    await route.fulfill({ json: { ...envelope, data: { ...real, version: 0, status: 'processing', reply: '', results: [], retry_after_ms: 1000 } } })
  })
  await page.route('**/api/v1/feedbacks/submissions/*', async route => {
    if (++queries === 1) return route.abort('failed')
    await route.continue()
  })
  const record = await send(page, '每天两次')
  const panel = page.locator(`section[aria-label="反馈结果 ${record.submission_id}"]`)
  await expect(panel).toContainText('处理状态：已应用')
  expect(queries).toBeGreaterThanOrEqual(2)
  expect(real.status).toBe('applied')
})

test('lost feedback response recovers one frozen submission and survives relogin', async ({ page, request }) => {
  await login(page, 'feedback-a')
  let posted
  let posts = 0
  await page.route('**/api/v1/feedbacks', async route => {
    if (route.request().method() !== 'POST') return route.continue()
    posts++
    posted = route.request().postDataJSON()
    await route.fetch() // Commit the real request, then lose only the response.
    await route.abort('failed')
  })
  await page.getByRole('button', { name: /我想说点啥/ }).click()
  await page.getByPlaceholder('例如：每次词太多了 / 这周三有事 / abandon 我老忘').fill('每次词太多了')
  await page.getByRole('button', { name: /^发\s*送$/ }).dblclick()
  await expect.poll(() => posted?.submission_id).toBeTruthy()
  const result = page.locator(`section[aria-label="反馈结果 ${posted.submission_id}"]`)
  await expect(result).toContainText('50')
  expect(posts).toBe(1)
  const before = await api(request, `/feedbacks/submissions/${posted.submission_id}`, users.a.token, undefined, 'GET')
  const replay = await api(request, '/feedbacks', users.a.token, posted)
  expect(replay.id).toBe(before.id)
  expect(replay.results).toEqual(before.results)
  await page.reload()
  await expect(result).toHaveCount(1)
  await page.getByRole('button', { name: /^退\s*出$/ }).click()
  await login(page, 'feedback-a')
  await expect(result).toHaveCount(1)
  expect((await api(request, '/feedbacks/mine', users.a.token, undefined, 'GET')).filter(row => row.submission_id === posted.submission_id)).toHaveLength(1)
})

test('partial feedback preserves applied quantity and isolates another student', async ({ browser, baseURL }) => {
  const a = await browser.newContext({ baseURL }); const b = await browser.newContext({ baseURL })
  try {
    const owner = await b.newPage(); const other = await a.newPage()
    await Promise.all([login(owner, 'feedback-b'), login(other, 'feedback-a')])
    const record = await send(owner, '每次词太多了；nonexistentword我老忘')
    expect(record.status).toBe('partial')
    expect(record.results.some(result => result.status === 'applied')).toBe(true)
    expect(record.results.some(result => result.status === 'failed')).toBe(true)
    const panel = owner.locator(`section[aria-label="反馈结果 ${record.submission_id}"]`)
    await expect(panel).toContainText('部分完成')
    await expect(panel).toContainText('50')
    await expect(panel.getByRole('button', { name: '重试未完成项' })).toHaveCount(0)
    await other.reload()
    await expect(other.locator(`section[aria-label="反馈结果 ${record.submission_id}"]`)).toHaveCount(0)
  } finally { await a.close(); await b.close() }
})

test('complaint in partial task appears once in real admin todo and notification views', async ({ browser, baseURL }) => {
  const student = await browser.newContext({ baseURL }); const admin = await browser.newContext({ baseURL })
  try {
    const p = await student.newPage(); const a = await admin.newPage()
    await login(p, 'feedback-b')
    const resources = studentResources('feedback-b')
    await login(a, 'feedback-admin')
    const notices = a.locator('.ant-card', { hasText: '管理员通知' }).locator('.ant-list-item')
    await expect(a.locator('.ant-card', { hasText: '学生反馈待办' })).toBeVisible()
    await expect.poll(() => a.evaluate(async () => (await (await fetch('/api/v1/admin/notifications', { headers: { Authorization: `Bearer ${localStorage.getItem('token')}` } })).json()).data.length)).toBeGreaterThanOrEqual(0)
    const noticeCount = await a.evaluate(async () => (await (await fetch('/api/v1/admin/notifications', { headers: { Authorization: `Bearer ${localStorage.getItem('token')}` } })).json()).data.length)
    const text = '投诉老师每天词太多；missingterm我老忘'
    const record = await send(p, text)
    expect(record.results.some(result => result.status === 'escalated')).toBe(true)
    expect(record.status).toBe('partial')
    expect(studentResources('feedback-b')).toEqual(resources)
    await a.reload()
    const todo = a.locator('.ant-card', { hasText: '学生反馈待办' }).locator('.ant-list-item', { hasText: text })
    await expect(todo).toHaveCount(1)
    await todo.getByRole('button', { name: '查看反馈详情' }).click()
    await expect(a.getByRole('dialog', { name: '反馈待办详情' })).toContainText(text)
    await a.getByRole('dialog').getByRole('button', { name: /close|关闭/i }).click()
    await expect(notices).toHaveCount(noticeCount + 1)
    await a.reload()
    await expect(todo).toHaveCount(1)
    await expect(notices).toHaveCount(noticeCount + 1)
    const denied = await p.evaluate(async () => (await fetch('/api/v1/admin/todos', { headers: { Authorization: `Bearer ${localStorage.getItem('token')}` } })).json())
    expect(denied.code).not.toBe(0)
  } finally { await student.close(); await admin.close() }
})

test('SQLite intent failure retries only unfinished work from the real feedback UI', async ({ page, request }, testInfo) => {
  const lessons = await api(request, `/student/lessons?bookId=${bookID}`, users.fault.token, undefined, 'GET')
  const original = lessons.find(lesson => lesson.Status === 'scheduled')
  await login(page, 'feedback-fault')
  await page.getByRole('button', { name: '查看 Feedback fixture 的课程' }).click()
  const nextClass = page.locator('.ant-card', { hasText: '下一节课 · Feedback fixture' }).getByRole('heading', { level: 4 })
  await expect(nextClass).toBeVisible()
  const oldDate = await nextClass.innerText()
  sql(testInfo, "CREATE TRIGGER e2e_feedback_memory BEFORE INSERT ON durable_memories BEGIN SELECT RAISE(ABORT,'fixture unavailable'); END;")
  let record
  try { record = await send(page, '每天两次；后天请假') }
  finally { sql(testInfo, 'DROP TRIGGER e2e_feedback_memory;') }
  expect(record.status).toBe('partial')
  const panel = page.locator(`section[aria-label="反馈结果 ${record.submission_id}"]`)
  await expect(panel.getByRole('button', { name: '重试未完成项', exact: true })).toBeVisible()
  const unchanged = await api(request, `/student/lessons?bookId=${bookID}`, users.fault.token, undefined, 'GET')
  expect(unchanged.find(lesson => lesson.ID === original.ID).OpenAt).toBe(original.OpenAt)
  let retries = 0
  await page.route(`**/api/v1/feedbacks/submissions/${record.submission_id}/retry`, async route => {
    retries++
    await new Promise(resolve => setTimeout(resolve, 150))
    await route.continue()
  })
  await panel.getByRole('button', { name: '重试未完成项', exact: true }).dblclick()
  await expect(panel).toContainText('处理状态：已应用')
  await expect(panel.getByRole('button', { name: '重试未完成项', exact: true })).toHaveCount(0)
  await expect(nextClass).not.toHaveText(oldDate)
  const moved = await api(request, `/student/lessons?bookId=${bookID}`, users.fault.token, undefined, 'GET')
  expect(new Date(moved.find(lesson => lesson.ID === original.ID).OpenAt).getTime()).toBeGreaterThan(new Date(original.OpenAt).getTime())
  await page.reload()
  await expect(panel).toHaveCount(1)
  await expect(panel).toContainText('处理状态：已应用')
  expect(retries).toBe(1)
})

test('definitive rejection clears pending hints and failed records never claim processing', async ({ page }) => {
  await login(page, 'feedback-a')
  let rejectedID
  await page.route('**/api/v1/feedbacks', async route => {
    rejectedID = route.request().postDataJSON().submission_id
    await route.fulfill({ status: 400, json: { code: 400, msg: 'fixture invalid feedback', error_code: 'invalid_text', submission_id: rejectedID } })
  })
  await page.getByRole('button', { name: /我想说点啥/ }).click()
  await page.getByPlaceholder('例如：每次词太多了 / 这周三有事 / abandon 我老忘').fill('每天两次')
  await page.getByRole('button', { name: /^发\s*送$/ }).click()
  await expect(page.getByText('fixture invalid feedback', { exact: true })).toBeVisible()
  expect(await page.evaluate(id => Object.keys(sessionStorage).filter(key => key.startsWith('feedback_pending:')).some(key => JSON.parse(sessionStorage.getItem(key)).includes(id)), rejectedID)).toBe(false)
  await page.route('**/api/v1/feedbacks/mine', route => route.fulfill({ json: { code: 0, data: [{ id: 987654, submission_id: 'fixture_failed', version: 1, text: 'failure fixture', status: 'failed', reply: '', error_code: 'storage_unavailable', results: [], created_at: '' }] } }))
  await page.reload()
  const panel = page.locator('section[aria-label="反馈结果 fixture_failed"]')
  await expect(panel).toContainText('反馈未完成')
  await expect(panel).not.toContainText('正在处理')
})

test('submission conflict shows the original durable result without executing edited text', async ({ page, request }) => {
  await login(page, 'feedback-a')
  let original
  await page.route('**/api/v1/feedbacks', async route => {
    const posted = route.request().postDataJSON()
    original = await api(request, '/feedbacks', users.a.token, { ...posted, text: '每天两次' })
    await route.fulfill({ status: 409, json: { code: 409, msg: 'fixture submission conflict', error_code: 'submission_conflict', submission_id: posted.submission_id } })
  })
  await page.getByRole('button', { name: /我想说点啥/ }).click()
  await page.getByPlaceholder('例如：每次词太多了 / 这周三有事 / abandon 我老忘').fill('每次词太多了')
  await page.getByRole('button', { name: /^发\s*送$/ }).click()
  await expect(page.getByText('fixture submission conflict', { exact: true })).toBeVisible()
  await expect.poll(() => original?.submission_id).toBeTruthy()
  const panel = page.locator(`section[aria-label="反馈结果 ${original.submission_id}"]`)
  await expect(panel).toContainText('处理状态：已应用')
  const persisted = await api(request, `/feedbacks/submissions/${original.submission_id}`, users.a.token, undefined, 'GET')
  expect(persisted.text).toBe('每天两次')
})

test('invalid date and excessive frequency require confirmation without changing learning settings', async ({ page, request }) => {
  const before = await api(request, `/student/lessons?bookId=${bookID}`, users.b.token, undefined, 'GET')
  const resources = studentResources('feedback-b')
  await login(page, 'feedback-b')
  for (const text of ['2月30日请假', '明天或后天请假', '每天4次', '每天10次', '每天12次', '每天三遍']) {
    const record = await send(page, text)
    expect(record.status).toBe('need_confirm')
    await expect(page.locator(`section[aria-label="反馈结果 ${record.submission_id}"]`)).toContainText('需要确认')
  }
  const after = await api(request, `/student/lessons?bookId=${bookID}`, users.b.token, undefined, 'GET')
  expect(after.map(row => [row.ID, row.OpenAt])).toEqual(before.map(row => [row.ID, row.OpenAt]))
  expect(studentResources('feedback-b')).toEqual(resources)
})

test('FB060 comma and semicolon date alternatives require confirmation without schedule writes', async ({ page, request }) => {
  const before = await api(request, `/student/lessons?bookId=${bookID}`, users.b.token, undefined, 'GET')
  const resources = studentResources('feedback-b')
  await login(page, 'feedback-b')
  for (const text of ['请假，明天或后天再上', '请假；明天或后天再上']) {
    const record = await send(page, text)
    expect(record.status).toBe('need_confirm')
    await expect(page.locator(`section[aria-label="反馈结果 ${record.submission_id}"]`)).toContainText('需要确认')
  }
  const after = await api(request, `/student/lessons?bookId=${bookID}`, users.b.token, undefined, 'GET')
  expect(after.map(row => [row.ID, row.OpenAt])).toEqual(before.map(row => [row.ID, row.OpenAt]))
  expect(studentResources('feedback-b')).toEqual(resources)
})

test('linked clear clarification survives a lost response using the same child submission', async ({ page, request }) => {
  await login(page, 'feedback-clarify')
  const parent = await send(page, '这是一条尚不明确的反馈')
  const panel = page.locator(`section[aria-label="反馈结果 ${parent.submission_id}"]`)
  await panel.getByRole('button', { name: '补充说明', exact: true }).click()
  await expect(page.getByRole('dialog', { name: '补充说明', exact: true })).toContainText(parent.text)
  await page.getByLabel('反馈补充说明').fill('每天两次')
  const submissions = []
  await page.route('**/api/v1/feedbacks', async route => {
    const body = route.request().postDataJSON()
    submissions.push(body)
    await route.fetch(); await route.abort('failed')
  }, { times: 1 })
  await page.getByRole('button', { name: '发送补充说明', exact: true }).click()
  await expect(page.getByRole('dialog', { name: '补充说明', exact: true })).toHaveCount(0)
  expect(submissions).toHaveLength(1)
  expect(submissions[0].parent_submission_id).toBe(parent.submission_id)
  expect(submissions[0].submission_id).not.toBe(parent.submission_id)
  const child = await api(request, `/feedbacks/submissions/${submissions[0].submission_id}`, users.clarify.token, undefined, 'GET')
  expect(child.status).toBe('applied'); expect(child.parent_submission_id).toBe(parent.submission_id)
  await page.reload()
  await expect(page.locator(`section[aria-label="反馈结果 ${child.submission_id}"]`)).toHaveCount(1)
  await expect(panel.getByRole('button', { name: '补充说明', exact: true })).toHaveCount(0)
})

test('uncertain linked reply locks its draft and recovers the same child without a second mutation', async ({ page, request }) => {
  await login(page, 'feedback-clarify')
  const parent = await send(page, '这件事情还没有说明白')
  await page.locator(`section[aria-label="反馈结果 ${parent.submission_id}"]`).getByRole('button', { name: '补充说明', exact: true }).click()
  const field = page.getByLabel('反馈补充说明')
  await field.fill('每天两次')
  let release
  const gate = new Promise(resolve => { release = resolve })
  let sent = false; let posts = 0; let childID
  await page.route('**/api/v1/feedbacks', async route => {
    posts++; childID = route.request().postDataJSON().submission_id
    await route.fetch(); sent = true; await gate; await route.abort('failed')
  })
  await page.route('**/api/v1/feedbacks/submissions/*', route => route.abort('failed'))
  await page.getByRole('button', { name: '发送补充说明', exact: true }).click()
  try {
    await expect.poll(() => sent).toBe(true)
    await expect(field).toBeDisabled()
    release()
    await expect(page.getByText('暂未确认是否送达，请保留正文并再次发送以查询原提交。', { exact: true })).toBeVisible()
    await expect(field).toBeDisabled(); await expect(field).toHaveValue('每天两次')
    await page.unroute('**/api/v1/feedbacks/submissions/*')
    await page.getByRole('button', { name: '发送补充说明', exact: true }).click()
    await expect(page.getByRole('dialog', { name: '补充说明', exact: true })).toHaveCount(0)
    expect(posts).toBe(1)
    const child = await api(request, `/feedbacks/submissions/${childID}`, users.clarify.token, undefined, 'GET')
    expect(child.parent_submission_id).toBe(parent.submission_id); expect(child.status).toBe('applied')
  } finally { release() }
})

test('twenty-first difficult word shows FIFO eviction and never changes another student pool', async ({ page, request }) => {
  test.setTimeout(180_000)
  const admin = await api(request, '/auth/login', null, { username: 'feedback-admin', password })
  const book = await api(request, '/wordbooks', admin.token, { name: 'FIFO fixture', grade: 'g7' })
  const words = ['apple', 'banana', 'orange', 'pear', 'peach', 'grape', 'lemon', 'melon', 'cherry', 'plum', 'kiwi', 'mango', 'bread', 'rice', 'milk', 'water', 'tea', 'coffee', 'egg', 'fish', 'beef']
  await api(request, `/wordbooks/${book.ID}/import/commit`, admin.token, { entries: words.map(word => ({ word, meanings: ['测试释义'] })) })
  await api(request, `/wordbooks/${book.ID}/publish`, admin.token, {})
  for (const suffix of ['fifo', 'fifo-other']) {
    const username = `feedback-${suffix}`
    await api(request, '/admin/users', admin.token, { username, password, nickname: username, role: 'student' })
    users[suffix] = await api(request, '/auth/login', null, { username, password })
    await api(request, `/student/books/${book.ID}/enable`, users[suffix].token, {})
    await api(request, `/student/books/${book.ID}/placement`, users[suffix].token, { knownWordIds: [], finish: true })
  }
  await api(request, '/feedbacks', users['fifo-other'].token, { submission_id: crypto.randomUUID(), text: 'apple我老忘', channel: 'page' })
  const otherBefore = studentResources('feedback-fifo-other')
  await login(page, 'feedback-fifo')
  let record
  for (const word of words) { record = await send(page, `${word}我老忘`); expect(record.status).toBe('applied') }
  const actual = record.results[0].actual
  expect(actual.boosted_word).toBe('beef')
  expect(actual.evicted_words.map(row => row.word)).toEqual(['apple'])
  const panel = page.locator(`section[aria-label="反馈结果 ${record.submission_id}"]`)
  await expect(panel).toContainText('已加入重点复习：beef')
  await expect(panel).toContainText('按加入顺序移出：apple')
  const script = "import sqlite3,json,sys; c=sqlite3.connect(sys.argv[1]); print(json.dumps(c.execute('SELECT w.word FROM memory_cards m JOIN word_entries w ON w.id=m.word_id AND w.book_id=m.book_id WHERE m.student_id=? AND m.boost_flag=1 ORDER BY m.boost_sequence',(sys.argv[2],)).fetchall())); c.close()"
  const pool = spawnSync('python', ['-c', script, process.env.E2E_FEEDBACK_DB, String(users.fifo.user_id)], { encoding: 'utf8' })
  expect(pool.status, pool.stderr).toBe(0)
  expect(JSON.parse(pool.stdout).flat()).toEqual(words.slice(1))
  expect(studentResources('feedback-fifo-other')).toEqual(otherBefore)
})

test('second unclear linked reply escalates once to administrator and remains student-isolated', async ({ browser, baseURL, request }) => {
  const contexts = await Promise.all([0, 1, 2].map(() => browser.newContext({ baseURL })))
  try {
    const [student, admin, other] = await Promise.all(contexts.map(c => c.newPage()))
    await Promise.all([login(student, 'feedback-clarify'), login(admin, 'feedback-admin'), login(other, 'feedback-a')])
    const parent = await send(student, '我还没有说清楚的事情')
    await student.locator(`section[aria-label="反馈结果 ${parent.submission_id}"]`).getByRole('button', { name: '补充说明', exact: true }).click()
    await student.getByLabel('反馈补充说明').fill('仍然说不清楚的事情')
    const response = student.waitForResponse(r => r.url().endsWith('/feedbacks') && r.request().method() === 'POST')
    await student.getByRole('button', { name: '发送补充说明', exact: true }).click()
    const child = (await (await response).json()).data
    expect(child.status).toBe('escalated')
    await expect(student.locator(`section[aria-label="反馈结果 ${child.submission_id}"]`)).toContainText('已转交管理员')
    await admin.reload()
    const todo = admin.locator('.ant-card', { hasText: '学生反馈待办' }).locator('.ant-list-item', { hasText: '仍然说不清楚的事情' })
    await expect(todo).toHaveCount(1)
    await api(request, `/feedbacks/submissions/${child.submission_id}`, users.clarify.token, undefined, 'GET')
    await admin.reload(); await expect(todo).toHaveCount(1)
    await other.reload(); await expect(other.locator(`section[aria-label="反馈结果 ${child.submission_id}"]`)).toHaveCount(0)
  } finally { await Promise.all(contexts.map(c => c.close())) }
})

test('already answered clarification stays linked and shows refresh guidance', async ({ page, request }) => {
  await login(page, 'feedback-clarify')
  const parent = await send(page, '另一条没有说清楚的事情')
  await page.locator(`section[aria-label="反馈结果 ${parent.submission_id}"]`).getByRole('button', { name: '补充说明', exact: true }).click()
  await page.getByLabel('反馈补充说明').fill('每天一次')
  await api(request, '/feedbacks', users.clarify.token, { submission_id: crypto.randomUUID(), parent_submission_id: parent.submission_id, text: '每天两次', channel: 'page' })
  await page.getByRole('button', { name: '发送补充说明', exact: true }).click()
  await expect(page.getByText('这条反馈已补充说明，请刷新反馈历史查看结果。', { exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: '发送补充说明', exact: true })).toBeDisabled()
})

test('monthly fourth postponement asks confirmation without hidden override or another schedule write', async ({ page, request }) => {
  await login(page, 'feedback-quota')
  const date = offset => {
    const d = new Date(); d.setDate(d.getDate() + offset)
    return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`
  }
  for (const offset of [1, 2, 3]) expect((await send(page, `${date(offset)}请假`)).status).toBe('applied')
  const before = await api(request, `/student/lessons?bookId=${bookID}`, users.quota.token, undefined, 'GET')
  const resources = studentResources('feedback-quota')
  const fourth = await send(page, `${date(4)}请假`)
  expect(fourth.status).toBe('need_confirm')
  const panel = page.locator(`section[aria-label="反馈结果 ${fourth.submission_id}"]`)
  await expect(panel).toContainText('需要确认')
  await expect(panel.getByRole('button', { name: /补充说明|确认执行|重试未完成项/ })).toHaveCount(0)
  expect(await api(request, `/student/lessons?bookId=${bookID}`, users.quota.token, undefined, 'GET')).toEqual(before)
  expect(studentResources('feedback-quota')).toEqual(resources)
})

test('late linked child response after account switch cannot query old parent with new credentials', async ({ page, context }) => {
  await login(page, 'feedback-clarify')
  const parent = await send(page, '这条反馈暂时没有说清楚')
  await page.locator(`section[aria-label="反馈结果 ${parent.submission_id}"]`).getByRole('button', { name: '补充说明', exact: true }).click()
  await page.getByLabel('反馈补充说明').fill('每天一次')
  let release
  const gate = new Promise(resolve => { release = resolve })
  let submitted = false; let switched = false; let crossQueries = 0
  page.on('request', req => { if (switched && req.url().endsWith(`/feedbacks/submissions/${parent.submission_id}`)) crossQueries++ })
  await page.route('**/api/v1/feedbacks', async route => {
    const response = await route.fetch(); submitted = true
    await gate; await route.fulfill({ response })
  }, { times: 1 })
  await page.getByRole('button', { name: '发送补充说明', exact: true }).click()
  await expect.poll(() => submitted).toBe(true)
  const second = await context.newPage()
  try {
    await login(second, 'feedback-a'); switched = true
    await expect(page.getByRole('dialog', { name: '补充说明', exact: true })).toHaveCount(0)
    release()
    await expect(page.locator(`section[aria-label="反馈结果 ${parent.submission_id}"]`)).toHaveCount(0)
    await page.evaluate(() => new Promise(resolve => setTimeout(resolve, 100)))
    expect(crossQueries).toBe(0)
  } finally { release(); await second.close() }
})

test('cross-tab account change removes old voice drafts and closes the previous classroom', async ({ page, context, request }) => {
  await voiceFixture(page)
  let submission
  let oldSocket
  await page.routeWebSocket('**/ws/classroom?*', socket => {
    oldSocket = socket
    const server = socket.connectToServer()
    socket.onMessage(message => {
      const frame = JSON.parse(String(message))
      if (frame.type === 'voice_text') {
        submission = frame.submission_id
        socket.send(JSON.stringify({ type: 'event_rejected', event_id: frame.event_id, submission_id: submission, error_code: 'stale_context' }))
        return
      }
      server.send(message)
    })
    server.onMessage(message => socket.send(message))
  })
  await enterVoice(page, request, 'switch')
  await expect(page.getByRole('button', { name: 'cat', exact: true })).toBeVisible()
  const oldTeacherBubble = await page.locator('div[aria-live="polite"]').allTextContents()
  expect(oldTeacherBubble.filter(Boolean).length).toBeGreaterThan(0)
  const privateText = '这是原学生的私人反馈正文，不能替新学生提交'
  await page.evaluate(text => window.__feedbackRecognizer.onresult({ results: [[{ transcript: `反馈${text}` }]] }), privateText)
  await expect(page.getByRole('dialog', { name: '反馈未送达' })).toBeVisible()
  const secondTab = await context.newPage()
  await login(secondTab, 'feedback-b')
  try { oldSocket.send(JSON.stringify({ type: 'event_processed', command: 'feedback', event_id: 'late-old-user', submission_id: submission, normalized_text: privateText })) } catch { /* Closing the old socket is the intended outcome. */ }
  await expect(page.getByText('连接已停止', { exact: true })).toBeVisible()
  await expect(page.getByRole('dialog', { name: '反馈未送达' })).toHaveCount(0)
  await expect(page.getByText(privateText, { exact: true })).toHaveCount(0)
  await expect(page.getByRole('button', { name: /^(cat|dog)$/ })).toHaveCount(0)
  for (const text of oldTeacherBubble.filter(Boolean)) await expect(page.getByText(text, { exact: true })).toHaveCount(0)
  await page.getByRole('button', { name: '返回课程表', exact: true }).click()
  await expect(page).toHaveURL(/\/dashboard$/)
  const stored = await page.evaluate(() => ({ userId: localStorage.getItem('userId'), pending: Object.fromEntries(Object.keys(sessionStorage).map(key => [key, sessionStorage.getItem(key)])) }))
  expect(JSON.parse(stored.pending[`feedback_pending:${stored.userId}`] || '[]')).not.toContain(submission)
  expect((await api(request, '/feedbacks/mine', users.b.token, undefined, 'GET')).some(record => record.text === privateText)).toBe(false)
})

test('selected schedule updates after normal and queried feedback while another student remains unchanged', async ({ browser, baseURL }) => {
  const ownerContext = await browser.newContext({ baseURL }); const otherContext = await browser.newContext({ baseURL })
  try {
    const owner = await ownerContext.newPage(); const other = await otherContext.newPage()
    await Promise.all([login(owner, 'feedback-a'), login(other, 'feedback-b')])
    for (const page of [owner, other]) await page.getByRole('button', { name: '查看 Feedback fixture 的课程' }).click()
    const date = page => page.locator('.ant-card', { hasText: '下一节课 · Feedback fixture' }).getByRole('heading', { level: 4 })
    await expect(date(owner)).toBeVisible(); await expect(date(other)).toBeVisible()
    const oldOwner = await date(owner).innerText(); const oldOther = await date(other).innerText()
    await send(owner, '明天请假')
    await expect(date(owner)).not.toHaveText(oldOwner)
    const afterNormal = await date(owner).innerText()
    let submission
    let queries = 0
    await owner.route('**/api/v1/feedbacks', async route => {
      submission = route.request().postDataJSON().submission_id
      await route.fetch(); await route.abort('failed')
    })
    await owner.route('**/api/v1/feedbacks/submissions/*', async route => {
      if (++queries === 1) return route.abort('failed')
      await route.continue()
    })
    await owner.getByRole('button', { name: /我想说点啥/ }).click()
    await owner.getByPlaceholder('例如：每次词太多了 / 这周三有事 / abandon 我老忘').fill('后天请假')
    await owner.getByRole('button', { name: /^发\s*送$/ }).click()
    await expect(owner.getByText('反馈送达状态待确认', { exact: true })).toBeVisible()
    await expect(owner.getByText('Failed to fetch', { exact: true })).toBeVisible()
    await owner.getByRole('dialog').getByRole('button', { name: '关闭', exact: true }).click()
    await expect(owner.getByRole('dialog')).toHaveCount(0)
    await owner.locator('.ant-alert', { hasText: '反馈送达状态待确认' }).getByRole('button', { name: '查询处理进度' }).click()
    await expect(owner.locator(`section[aria-label="反馈结果 ${submission}"]`)).toContainText('处理状态：已应用')
    await expect(date(owner)).not.toHaveText(afterNormal)
    await expect(date(other)).toHaveText(oldOther)
    await other.reload()
    await other.getByRole('button', { name: '查看 Feedback fixture 的课程' }).click()
    await expect(date(other)).toHaveText(oldOther)
  } finally { await ownerContext.close(); await otherContext.close() }
})

test('excessive difficult-word feedback explains splitting and performs no partial action', async ({ page, request }) => {
  await login(page, 'feedback-b')
  const resources = studentResources('feedback-b')
  const before = await api(request, `/student/lessons?bookId=${bookID}`, users.b.token, undefined, 'GET')
  const record = await send(page, 'apple我老忘；banana我老忘；cat我老忘；dog我老忘；orange我老忘')
  expect(record.status).toBe('failed')
  const panel = page.locator(`section[aria-label="反馈结果 ${record.submission_id}"]`)
  await expect(panel).toContainText(/拆(?:分|成).*提交/)
  expect(record.results.every(result => result.status !== 'applied')).toBe(true)
  const after = await api(request, `/student/lessons?bookId=${bookID}`, users.b.token, undefined, 'GET')
  expect(after.map(row => [row.ID, row.OpenAt, row.BatchWordIDs])).toEqual(before.map(row => [row.ID, row.OpenAt, row.BatchWordIDs]))
  expect(studentResources('feedback-b')).toEqual(resources)
})

test('dashboard account switch clears drafts and selection and ignores old notifications', async ({ page, context }) => {
  let oldNotification
  let release
  const held = new Promise(resolve => { release = resolve })
  await page.route('**/api/v1/student/notifications', async route => {
    if (!oldNotification) {
      oldNotification = route.request().headers().authorization
      await held
      return route.fulfill({ json: { code: 0, data: [{ id: 998, title: '旧账户私密通知', body: '不可显示在新账户', read: false }] } })
    }
    return route.continue()
  })
  await login(page, 'feedback-a')
  await page.getByRole('button', { name: '查看 Feedback fixture 的课程' }).click()
  await expect(page.getByText('下一节课 · Feedback fixture', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: /我想说点啥/ }).click()
  await page.getByPlaceholder('例如：每次词太多了 / 这周三有事 / abandon 我老忘').fill('旧账户未发送的私人草稿')
  await page.getByRole('dialog', { name: '告诉叮当老师' }).getByRole('button', { name: /^取\s*消$/ }).click()
  await page.getByRole('button', { name: '查看 Feedback fixture 的课程' }).getByRole('button').click()
  await expect(page.getByRole('dialog', { name: '移除词库' })).toBeVisible()
  const secondTab = await context.newPage()
  await login(secondTab, 'feedback-b')
  release()
  await expect(page.getByRole('heading', { name: /feedback-b/ })).toBeVisible()
  await expect(page.getByRole('dialog', { name: '告诉叮当老师' })).toHaveCount(0)
  await expect(page.getByRole('dialog', { name: '移除词库' })).toHaveCount(0)
  await expect(page.getByText('下一节课 · Feedback fixture', { exact: true })).toHaveCount(0)
  await expect(page.getByText('旧账户私密通知', { exact: true })).toHaveCount(0)
  await page.getByRole('button', { name: /我想说点啥/ }).click()
  await expect(page.getByPlaceholder('例如：每次词太多了 / 这周三有事 / abandon 我老忘')).toHaveValue('')
})

test('settings account switch reloads the new student values instead of old unsaved edits', async ({ page, context }) => {
  await login(page, 'feedback-a')
  await page.getByRole('button', { name: '学习设置', exact: true }).click()
  const field = page.getByLabel('英文朗读遍数', { exact: true })
  await expect(field).toHaveValue('3')
  await field.fill('5')
  const secondTab = await context.newPage()
  await login(secondTab, 'feedback-b')
  await expect(field).toHaveValue('3')
})

test('late 401 from an earlier token cannot log out the new session while a current 401 does', async ({ page, context, request }) => {
  let release
  let first = true
  const held = new Promise(resolve => { release = resolve })
  await page.route('**/api/v1/student/notifications', async route => {
    if (first) {
      first = false
      await held
      return route.fulfill({ json: { code: 401, msg: 'old session expired' } })
    }
    return route.continue()
  })
  await login(page, 'feedback-a')
  const oldToken = await page.evaluate(() => localStorage.getItem('token'))
  let newToken
  await expect.poll(async () => {
    newToken = (await api(request, '/auth/login', null, { username: 'feedback-a', password })).token
    return newToken
  }).not.toBe(oldToken)
  const secondTab = await context.newPage()
  await secondTab.goto('/dashboard')
  await secondTab.evaluate(token => localStorage.setItem('token', token), newToken)
  await expect.poll(() => page.evaluate(() => localStorage.getItem('token'))).toBe(newToken)
  release()
  await page.getByRole('button', { name: '学习设置', exact: true }).click()
  await expect(page.getByLabel('英文朗读遍数', { exact: true })).toBeVisible()
  expect(await page.evaluate(() => localStorage.getItem('token'))).toBe(newToken)
  await page.unroute('**/api/v1/student/notifications')
  await page.route('**/api/v1/student/notifications', route => route.fulfill({ json: { code: 401, msg: 'current session expired' } }))
  await page.getByRole('button', { name: '返回课程表', exact: true }).click()
  await expect(page).toHaveURL(/\/login$/)
  expect(await page.evaluate(() => localStorage.getItem('token'))).toBeNull()
})

test('terminal prerequisite failure removes the dependent intent retry button', async ({ page, request }, testInfo) => {
  await login(page, 'feedback-b')
  sql(testInfo, "CREATE TRIGGER e2e_feedback_config_insert BEFORE INSERT ON config_overrides BEGIN SELECT RAISE(ABORT,'fixture unavailable'); END; CREATE TRIGGER e2e_feedback_config_update BEFORE UPDATE ON config_overrides BEGIN SELECT RAISE(ABORT,'fixture unavailable'); END;")
  let record
  try { record = await send(page, '太多了；每天一次') }
  finally { sql(testInfo, 'DROP TRIGGER e2e_feedback_config_insert; DROP TRIGGER e2e_feedback_config_update;') }
  expect(record.results).toHaveLength(2)
  expect(record.retryable).toBe(true)
  // Explicit persisted-plan fixture: ordinary duplicate natural-language
  // intents are deduplicated and are not claimed to produce this dependency.
  expect(record.task_id).toMatch(/^[A-Za-z0-9_-]+$/)
  sql(testInfo, `UPDATE feedback_intents SET frozen_json=json_set(frozen_json,'$.depends_on',0) WHERE task_id='${record.task_id}' AND intent_index=1; UPDATE feedback_tasks SET plan_json=json_set(plan_json,'$[1].depends_on',0) WHERE task_id='${record.task_id}';`)
  const admin = await api(request, '/auth/login', null, { username: 'feedback-admin', password })
  await api(request, '/config/set', admin.token, { scope: 'student', studentId: record.student_id, key: 'wordsPerLesson', value: 80, reason: 'independent revision fixture' })
  const before = studentResources('feedback-b')
  const panel = page.locator(`section[aria-label="反馈结果 ${record.submission_id}"]`)
  const retryResponse = page.waitForResponse(response => response.url().endsWith(`/feedbacks/submissions/${record.submission_id}/retry`) && response.request().method() === 'POST')
  await panel.getByRole('button', { name: '重试未完成项', exact: true }).click()
  expect((await (await retryResponse).json()).code).toBe(0)
  await expect(panel.getByRole('button', { name: '重试未完成项', exact: true })).toHaveCount(0)
  const final = await api(request, `/feedbacks/submissions/${record.submission_id}`, users.b.token, undefined, 'GET')
  expect(final.results.map(result => result.result_code)).toEqual(['stale_target', 'dependency_failed'])
  expect(final.results.every(result => !result.retryable)).toBe(true)
  expect(final.retryable).toBe(false)
  expect(studentResources('feedback-b')).toEqual(before)
})

test('renewed administrator session closes the previous feedback detail', async ({ page, context, request }) => {
  await login(page, 'feedback-admin')
  await page.getByRole('button', { name: '查看反馈详情', exact: true }).first().click()
  await expect(page.getByRole('dialog', { name: '反馈待办详情' })).toBeVisible()
  const oldToken = await page.evaluate(() => localStorage.getItem('token'))
  let newToken
  await expect.poll(async () => {
    newToken = (await api(request, '/auth/login', null, { username: 'feedback-admin', password })).token
    return newToken
  }).not.toBe(oldToken)
  const secondTab = await context.newPage()
  await secondTab.goto('/admin')
  await secondTab.evaluate(token => localStorage.setItem('token', token), newToken)
  await expect(page.getByRole('dialog', { name: '反馈待办详情' })).toHaveCount(0)
  await expect(page.getByRole('heading', { name: '叮当words 管理', exact: true })).toBeVisible()
})
