import { test, expect, type Page } from '@playwright/test'
import { readFileSync, mkdirSync, existsSync, writeFileSync } from 'node:fs'
import { randomUUID } from 'node:crypto'
import { resolve } from 'node:path'
import { execFileSync } from 'node:child_process'
import { createServer } from 'node:http'
import { StartupOwned } from './startup-owned'
import { ownedExited } from './owned-process'
import { media, login } from './isolated-fixtures'

const binary = () => { if (!process.env.E2E_SERVER_BINARY) throw new Error('E2E_SERVER_BINARY required'); return resolve(process.env.E2E_SERVER_BINARY) }
const harness = () => { if (!process.env.E2E_STARTUP_HARNESS_BINARY) throw new Error('E2E_STARTUP_HARNESS_BINARY required'); return resolve(process.env.E2E_STARTUP_HARNESS_BINARY) }
const objects = (db: string) => JSON.parse(execFileSync('python', ['-c',
  'import sqlite3,json,sys; c=sqlite3.connect("file:"+sys.argv[1]+"?mode=ro",uri=True); print(json.dumps(c.execute("SELECT type,name FROM sqlite_master WHERE substr(name,1,7) != ? ORDER BY type,name",("sqlite_",)).fetchall()))', db], { encoding: 'utf8', windowsHide: true }))
const logEvents = (path: string) => readFileSync(path, 'utf8').split(/\r?\n/).flatMap(line => { try { return [JSON.parse(line)] } catch { return [] } })
function verifyStartupLog(events: any[]) {
  const stages = events.filter(e => e.event?.startsWith('startup_stage_'))
  expect(stages.length).toBeGreaterThan(0)
  expect(new Set(stages.map(e => e.trace_id)).size).toBe(1)
  for (const event of stages) {
    expect(event.module).toBe('startup'); expect(event.trace_id).toMatch(/^tr-[a-zA-Z0-9-]+$/)
    expect(Number.isInteger(event.duration_ms)).toBe(true); expect(event.duration_ms).toBeGreaterThanOrEqual(0)
    for (const key of ['sql', 'path', 'db', 'password', 'token', 'error', 'dsn']) expect(event).not.toHaveProperty(key)
    expect(JSON.stringify(event)).not.toMatch(/CREATE TABLE|CREATE TRIGGER|[A-Z]:\\\\|private DDL|injected private/)
  }
}
function expectedExit(child: import('node:child_process').ChildProcess) {
  expect(child.signalCode).toBeNull()
  expect(typeof child.exitCode).toBe('number'); expect(child.exitCode).toBeGreaterThan(0)
}

async function unavailable(page: Page) {
  await page.goto('/login')
  await expect(page.getByRole('button', { name: /^重\s*试$/ })).toBeVisible()
  await expect(page.getByRole('button', { name: '开始学习', exact: true })).toBeDisabled()
}

test('ST06 two genuinely competing initializers cannot expose a partial schema', async ({ page }, info) => {
  const owned = new StartupOwned(), db = info.outputPath('startup.db')
  const barrier = info.outputPath('barrier'), nonce = randomUUID().replaceAll('-', '')
  mkdirSync(barrier)
  try {
    const children = ['a', 'b'].map((participant, index) => owned.start(harness(), db, 18083 + index,
      info.outputPath(`competitor-${participant}.log`), {
        ENWORDS_STARTUP_HARNESS: '1', ENWORDS_STARTUP_DB: db, ENWORDS_STARTUP_PORT: String(18083 + index),
        ENWORDS_STARTUP_FAULT: 'none', ENWORDS_STARTUP_BARRIER: barrier,
        ENWORDS_STARTUP_NONCE: nonce, ENWORDS_STARTUP_PARTICIPANT: participant,
      }))
    await expect.poll(() => ['a', 'b'].every(id => existsSync(resolve(barrier, `ready-${nonce}-${id}`))), { timeout: 20_000 }).toBe(true)
    expect(children.every(child => !ownedExited(child))).toBe(true)
    writeFileSync(resolve(barrier, `release-${nonce}`), 'release\n', { flag: 'wx' })
    const outcomes = await Promise.all(children.map(async (child, index) => {
      try { await owned.ready(child, 18083 + index); return 'ready' }
      catch {
        await owned.exited(child, 1000); expectedExit(child)
        const events = logEvents(info.outputPath(`competitor-${index ? 'b' : 'a'}.log`))
        verifyStartupLog(events)
        expect(events.some(e => e.event === 'startup_stage_failed' && ['initialization_conflict', 'schema_failed'].includes(e.reason))).toBe(true)
        expect(events.some(e => e.stage === 'listen' && e.event === 'startup_stage_completed')).toBe(false)
        return 'conflict-exit'
      }
    }))
    expect(outcomes).toContain('ready')
    const before = objects(db)
    expect(before.filter(([kind]: string[]) => kind === 'table')).toHaveLength(22)
    await Promise.all(children.map(child => owned.stop(child)))
    await unavailable(page)
    const recovered = owned.start(binary(), db, 18083, info.outputPath('recovered.log'))
    expect((await owned.ready(recovered, 18083)).needed).toBe(true)
    expect(objects(db)).toEqual(before)
    await bootstrapAfterRetry(page)
  } finally {
    try { await owned.close() } finally { await info.attach('startup-processes', { body: JSON.stringify(owned.records), contentType: 'application/json' }) }
  }
})
async function bootstrapAfterRetry(page: Page) {
  await page.getByRole('button', { name: /^重\s*试$/ }).click()
  await bootstrap(page)
}
async function bootstrap(page: Page) {
  await expect(page.getByText('首次使用：创建管理员账号')).toBeVisible()
  await page.getByLabel('用户名', { exact: true }).fill('startup-admin')
  await page.getByLabel('密码', { exact: true }).fill('fixture12345')
  await page.getByRole('button', { name: '创建管理员并登录', exact: true }).click()
  await expect(page).toHaveURL(/\/admin$/)
}

async function action(page: Page, label: string) {
  const button = page.getByRole('button', { name: new RegExp(`${label}$`) })
  await expect(button).toBeEnabled(); await expect(button).not.toHaveClass(/\bant-btn-loading\b/)
  await button.click()
}
async function setupByUI(admin: Page) {
  await admin.goto('/login'); await bootstrap(admin)
  for (const username of ['startup-a', 'startup-b']) {
    await admin.getByLabel('学生用户名').fill(username)
    await admin.getByLabel('学生昵称').fill(username)
    await admin.getByLabel('初始密码').fill('fixture12345')
    const response = admin.waitForResponse(r => r.url().endsWith('/admin/users') && r.request().method() === 'POST')
    await action(admin, '创建学生'); expect((await (await response).json()).code).toBe(0)
  }
  await admin.getByLabel('词库名称').fill('启动验收库'); await action(admin, '创建词库')
  await expect(admin.getByText('词库已创建', { exact: true })).toBeVisible()
  await admin.getByLabel('词条内容').fill('cat\t猫')
  await action(admin, '导入词条'); await expect(admin.getByText('已导入 1 词，跳过 0 词')).toBeVisible()
  await action(admin, '发布词库'); await expect(admin.getByText('词库已发布', { exact: true })).toBeVisible()
}
async function planByUI(page: Page, username: string) {
  await media(page); await login(page, username)
  await page.getByRole('button', { name: /词库广场$/ }).click()
  await page.locator('.ant-card', { hasText: '启动验收库' }).getByRole('button', { name: '选择这个词库' }).click()
  await page.getByRole('button', { name: '开始摸底' }).click()
  await expect(page.getByRole('button', { name: 'cat', exact: true })).toBeVisible()
  await page.getByRole('button', { name: '完成摸底，生成学习计划' }).click()
  await expect(page.getByRole('dialog', { name: '摸底完成！' })).toBeVisible()
  await page.locator('.ant-modal-confirm .ant-btn-primary').click()
  await page.getByRole('button', { name: '查看 启动验收库 的课程' }).click()
  await expect(page.getByRole('button', { name: '进入课堂', exact: true })).toBeEnabled()
}

for (const legacy of [false, true]) {
  test(`${legacy ? 'ST04' : 'ST01-ST07'} real bootstrap and two students survive isolated startup`, async ({ page, browser }, info) => {
    const owned = new StartupOwned(), db = info.outputPath('startup.db')
    const contexts = await Promise.all([browser.newContext({ baseURL: 'http://127.0.0.1:15175' }), browser.newContext({ baseURL: 'http://127.0.0.1:15175' })])
    const [a, b] = await Promise.all(contexts.map(context => context.newPage()))
    try {
      const initialBinary = legacy ? process.env.E2E_STARTUP_OLD_BINARY : binary()
      if (!initialBinary) throw new Error('E2E_STARTUP_OLD_BINARY required')
      let ownedChild = owned.start(initialBinary, db, 18083, info.outputPath('initial.log'))
      expect((await owned.ready(ownedChild, 18083)).needed).toBe(true)
      await setupByUI(page)
      await Promise.all([planByUI(a, 'startup-a'), planByUI(b, 'startup-b')])
      let priorLessonPath = ''
      if (legacy) {
        const oldEnter = a.waitForResponse(r => /\/classroom\/lessons\/\d+\/enter$/.test(r.url()), { timeout: 30_000 })
        await a.getByRole('button', { name: '进入课堂', exact: true }).click()
        expect((await (await oldEnter).json()).code).toBe(0)
        await expect(a.getByText('已连接', { exact: true })).toBeVisible()
        priorLessonPath = new URL(a.url()).pathname
        await a.goto('/dashboard')
        await a.getByRole('button', { name: '查看 启动验收库 的课程' }).click()
        await owned.stop(ownedChild)
        ownedChild = owned.start(binary(), db, 18083, info.outputPath('upgraded.log'))
        expect((await owned.ready(ownedChild, 18083)).needed).toBe(false)
        await Promise.all([a.reload(), b.reload()])
        await Promise.all([a, b].map(student => student.getByRole('button', { name: '查看 启动验收库 的课程' }).click()))
        await expect(a.getByRole('button', { name: '进入课堂', exact: true })).toBeEnabled()
        await expect(b.getByRole('button', { name: '进入课堂', exact: true })).toBeEnabled()
        await expect.poll(() => logEvents(info.outputPath('upgraded.log')).some(e => e.stage === 'legacy_schema' && e.event === 'startup_stage_completed')).toBe(true)
        verifyStartupLog(logEvents(info.outputPath('upgraded.log')))
      } else {
        await expect.poll(() => logEvents(info.outputPath('initial.log')).some(e => e.stage === 'listen' && e.event === 'startup_stage_completed')).toBe(true)
        verifyStartupLog(logEvents(info.outputPath('initial.log')))
      }
      const enter = a.waitForResponse(r => /\/classroom\/lessons\/\d+\/enter$/.test(r.url()), { timeout: 30_000 })
      await a.getByRole('button', { name: '进入课堂', exact: true }).click()
      expect((await (await enter).json()).code).toBe(0)
      if (legacy) expect(new URL(a.url()).pathname).toBe(priorLessonPath)
      for (let n = 1; n <= 4; n++) await expect.poll(() => a.evaluate(() => (window as any).__media.completed.length)).toBeGreaterThanOrEqual(n)
      await expect(a.getByText('轮到你读了！大声一点没关系', { exact: true })).toBeVisible()
      await expect.poll(() => a.evaluate(() => !!(window as any).__media.recognition)).toBe(true)
      await a.evaluate(() => (window as any).__media.recognition.onresult({ results: [[{ transcript: 'cat' }]] }))
      await expect(a.getByLabel('答案输入框')).toBeVisible()
      // A's persisted classroom progress must not enter or answer B's lesson.
      await b.reload()
      await b.getByRole('button', { name: '查看 启动验收库 的课程' }).click()
      await expect(b.getByRole('button', { name: '进入课堂', exact: true })).toBeEnabled()
      expect(new URL(b.url()).pathname).toBe('/dashboard')
      const bEnter = b.waitForResponse(r => /\/classroom\/lessons\/\d+\/enter$/.test(r.url()), { timeout: 30_000 })
      await b.getByRole('button', { name: '进入课堂', exact: true }).click()
      expect((await (await bEnter).json()).code).toBe(0)
      await expect(b.getByText('已连接', { exact: true })).toBeVisible()
      expect(new URL(b.url()).pathname).not.toBe(new URL(a.url()).pathname)
      await expect(a.getByLabel('答案输入框')).toBeVisible()
      expect(objects(db).length).toBeGreaterThan(22)
    } finally {
      try { await owned.close() } finally { await Promise.all(contexts.map(context => context.close())); await info.attach('startup-processes', { body: JSON.stringify(owned.records), contentType: 'application/json' }) }
    }
  })
}

// These are real test-driver/storage faults, never fulfilled HTTP errors.
for (const [id, fault] of [['ST02-schema', 'schema'], ['ST02-trigger', 'trigger'], ['ST02-index', 'index'],
  ['ST03-commit', 'commit'], ['ST03-cancel', 'cancel'], ['ST03-panic', 'panic'],
  ['ST05-outer', 'probe_outer'], ['ST05-inner', 'probe_inner']]) {
  test(`${id} failed fresh startup is atomic and manual browser retry recovers`, async ({ page }, info) => {
    const owned = new StartupOwned()
    const db = info.outputPath('startup.db'), failedLog = info.outputPath('failed.log')
    try {
      const failed = owned.start(harness(), db, 18083, failedLog, {
        ENWORDS_STARTUP_HARNESS: '1', ENWORDS_STARTUP_DB: db, ENWORDS_STARTUP_PORT: '18083', ENWORDS_STARTUP_FAULT: fault,
      })
      await owned.exited(failed)
      expectedExit(failed)
      expect(objects(db)).toEqual([])
      const events = logEvents(failedLog)
      verifyStartupLog(events)
      const expected: Record<string, [string, string]> = {
        schema: ['fresh_schema', 'schema_failed'], trigger: ['triggers', 'trigger_failed'], index: ['index', 'index_failed'],
        commit: ['finalize', 'commit_failed'], cancel: ['fresh_schema', 'canceled'], panic: ['fresh_schema', 'schema_failed'],
        probe_outer: ['schema_probe', 'probe_failed'], probe_inner: ['schema_probe', 'probe_failed'],
      }
      const [stage, reason] = expected[fault]
      expect(events.some(event => event.event === 'startup_stage_failed' && event.stage === stage && event.reason === reason)).toBe(true)
      expect(events.some(event => event.event === 'startup_stage_completed' && event.stage === stage && stage !== 'schema_probe')).toBe(false)
      expect(events.some(event => event.stage === 'listen' && event.event === 'startup_stage_completed')).toBe(false)
      await unavailable(page)
      const recovered = owned.start(binary(), db, 18083, info.outputPath('recovered.log'))
      expect((await owned.ready(recovered, 18083)).needed).toBe(true)
      await bootstrapAfterRetry(page)
    } finally {
      try { await owned.close() } finally { await info.attach('startup-processes', { body: JSON.stringify(owned.records), contentType: 'application/json' }) }
    }
  })
}

test('ST08 occupied listener reports failure before any server-ready success and UI can retry', async ({ page }, info) => {
  const owned = new StartupOwned()
  const blocker = createServer((_req, response) => { response.writeHead(503); response.end('fixture port occupied') })
  await new Promise<void>((done, reject) => { blocker.once('error', reject); blocker.listen({ port: 18083, host: '::', ipv6Only: false, exclusive: true }, done) })
  let blocking = true
  const db = info.outputPath('startup.db'), failedLog = info.outputPath('occupied.log')
  try {
    for (const host of ['127.0.0.1', '[::1]']) {
      const probe = await fetch(`http://${host}:18083/`, { signal: AbortSignal.timeout(1000) })
      expect(probe.status).toBe(503); expect(await probe.text()).toBe('fixture port occupied')
    }
    const failed = owned.start(binary(), db, 18083, failedLog)
    await owned.exited(failed)
    const events = logEvents(failedLog)
    // Running this exact case against frozen old main locks its false server_started.
    expect(events.filter(event => event.event === 'server_started')).toEqual([])
    expectedExit(failed)
    verifyStartupLog(events)
    expect(events.some(event => event.event === 'startup_stage_failed' && event.stage === 'listen')).toBe(true)
    expect(events.some(event => event.event === 'startup_stage_completed' && event.stage === 'listen')).toBe(false)
    await unavailable(page)
    blocker.closeAllConnections()
    await new Promise<void>((done, reject) => blocker.close(error => error ? reject(error) : done()))
    blocking = false
    const recovered = owned.start(binary(), db, 18083, info.outputPath('recovered.log'))
    expect((await owned.ready(recovered, 18083)).needed).toBe(true)
    await bootstrapAfterRetry(page)
  } finally {
    try { await owned.close() } finally {
      if (blocking) { blocker.closeAllConnections(); await new Promise<void>(done => blocker.close(() => done())) }
      await info.attach('startup-processes', { body: JSON.stringify(owned.records), contentType: 'application/json' })
    }
  }
})

// Exercises the production cleanup object with a real Router/DB, NOT complete run().
// Main's single-defer assembly is reviewed separately; no product fault endpoint exists.
for (const mode of ['normal', 'timeout']) {
  test(`ST09 ${mode} owned cleanup preserves account and permits explicit login after restart`, async ({ page }, info) => {
    if (!process.env.E2E_STARTUP_CLEANUP_BINARY) throw new Error('E2E_STARTUP_CLEANUP_BINARY required')
    const owned = new StartupOwned(), db = info.outputPath('startup.db'), cleanupLog = info.outputPath('cleanup.log')
    try {
      const initial = owned.start(resolve(process.env.E2E_STARTUP_CLEANUP_BINARY), db, 18083, cleanupLog, {
        ENWORDS_STARTUP_CLEANUP_HARNESS: '1', ENWORDS_STARTUP_DB: db, ENWORDS_STARTUP_PORT: '18083', ENWORDS_STARTUP_CLEANUP_MODE: mode,
      })
      expect((await owned.ready(initial, 18083)).needed).toBe(true)
      await page.goto('/login'); await bootstrap(page)
      expect(initial.stdin?.write('stop\n')).toBe(true)
      await owned.exited(initial, 15_000)
      expect(initial.signalCode).toBeNull()
      if (mode === 'normal') expect(initial.exitCode).toBe(0)
      else expectedExit(initial)
      const events = logEvents(cleanupLog)
      const observations = events.filter(event => event.event === 'startup_cleanup_observed')
      expect(observations).toHaveLength(1)
      expect(observations[0]).toMatchObject({ mode, pool_closed: mode === 'normal', worker_joined: mode === 'normal' })
      expect(observations[0].trace_id).toMatch(/^tr-[a-zA-Z0-9-]+$/)
      expect(events.filter(event => event.event === 'runtime_stopped')).toHaveLength(mode === 'normal' ? 1 : 0)
      const incomplete = events.filter(event => event.event === 'shutdown_incomplete')
      expect(incomplete).toHaveLength(mode === 'timeout' ? 1 : 0)
      if (mode === 'timeout') expect(incomplete[0].reason).toBe('runtime_stop_timeout')
      for (const event of [...observations, ...incomplete]) {
        for (const key of ['sql', 'path', 'db', 'password', 'token', 'error', 'dsn']) expect(event).not.toHaveProperty(key)
      }
      await unavailable(page)
      const recovered = owned.start(binary(), db, 18083, info.outputPath('recovered.log'))
      expect((await owned.ready(recovered, 18083)).needed).toBe(false)
      await page.getByRole('button', { name: /^重\s*试$/ }).click()
      await expect(page.getByText('首次使用：创建管理员账号')).toHaveCount(0)
      await expect(page.getByRole('button', { name: '开始学习', exact: true })).toBeEnabled()
      await page.getByLabel('用户名', { exact: true }).fill('startup-admin')
      await page.getByLabel('密码', { exact: true }).fill('fixture12345')
      const loggedIn = page.waitForResponse(r => r.url().endsWith('/auth/login') && r.request().method() === 'POST')
      await page.getByRole('button', { name: '开始学习', exact: true }).click()
      expect((await (await loggedIn).json()).code).toBe(0)
      await expect(page).toHaveURL(/\/admin$/)
    } finally {
      try { await owned.close() } finally { await info.attach('startup-processes', { body: JSON.stringify(owned.records), contentType: 'application/json' }) }
    }
  })
}
