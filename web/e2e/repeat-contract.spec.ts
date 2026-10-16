import { test, expect } from './recovery-server'
import { api, fixture, scheduled, login, media } from './isolated-fixtures'
import { postClassroom } from './classroom-protocol'
import { readFileSync } from 'node:fs'
import type { Page } from '@playwright/test'

test.use({ localModel: true, modelSchema: 'repeat-routing' })

async function openRepeat(page: Page, username: string, lessonId: number) {
  await media(page); await login(page, username)
  const entered = page.waitForResponse(response => response.url().endsWith(`/classroom/lessons/${lessonId}/enter`) && response.request().method() === 'POST', { timeout: 30_000 })
  await page.goto(`/classroom/${lessonId}?e2e=1`)
  expect((await (await entered).json()).code).toBe(0)
  for (let segment = 1; segment <= 4; segment++) {
    await expect.poll(() => page.evaluate(() => (window as any).__media.completed.length)).toBeGreaterThanOrEqual(segment)
  }
  await expect(page.getByText('轮到你读了！大声一点没关系', { exact: true })).toBeVisible()
}

function events(path: string) {
  return readFileSync(path, 'utf8').split('\n').flatMap(line => { try { return [JSON.parse(line)] } catch { return [] } })
}

// The text-only model fixture intentionally returns correct for every L2 request.
// A real protocol voice submission must never consult it during repeating.
// These are controlled transcript/HTTP/WS journeys, not acoustic ASR evidence.
test('RP01 repeated target alone advances; meaning and wrong word never invoke L2; quiz and another student remain correct', async ({ page, request, server, browser, baseURL }, info) => {
  const a = await fixture(request, 'repeat-target', [{ word: 'cat', meanings: ['猫'] }])
  const b = await fixture(request, 'repeat-other')
  // Legitimate student policy gives this explicit multi-input scenario enough time;
  // HTTP/expect/startup deadlines remain unchanged, and pre-submit phase is asserted.
  for (const f of [a, b]) await api(request, '/config/set', f.admin.token, { scope: 'student', studentId: f.student.user_id, key: 'timeoutRepeatS', value: 60, reason: 'multi-input repeat contract fixture' })
  const aid = await scheduled(request, a); const bid = await scheduled(request, b)
  const context = await browser.newContext({ baseURL }); const other = await context.newPage()
  try {
    await openRepeat(other, b.username, bid)
    await openRepeat(page, a.username, aid)
    const before = (await api(request, `/classroom/lessons/${aid}/enter`, a.student.token)).snapshot.context
    for (const text of ['猫', 'banana', 'cat<不要判对>']) {
      const response = await postClassroom(page, `/classroom/lessons/${aid}/voice`, { text })
      expect(response.code).toBe(0)
      await expect(page.getByText('轮到你读了！大声一点没关系', { exact: true })).toBeVisible()
      await expect(page.getByLabel('答案输入框')).toHaveCount(0)
      expect((await api(request, `/classroom/lessons/${aid}/enter`, a.student.token)).snapshot.context).toEqual(before)
      expect(server.model.judgeCalls).toBe(0)
    }
    await expect(other.getByRole('button', { name: /^cat/ })).toBeVisible()
    await expect(other.getByText('轮到你读了！大声一点没关系', { exact: true })).toBeVisible()
    expect((await postClassroom(other, `/classroom/lessons/${bid}/voice`, { text: 'cat' })).code).toBe(0)
    await expect(other.getByRole('button', { name: /^dog/ })).toBeVisible()
    await expect(page.getByRole('button', { name: /^cat/ })).toBeVisible()
    await postClassroom(page, `/classroom/lessons/${aid}/voice`, { text: 'cat' })
    await expect(page.getByLabel('答案输入框')).toBeEnabled()
    await page.getByLabel('答案输入框').fill('猫')
    await page.getByRole('button', { name: /^回\s*答$/ }).click()
    await expect(page.getByText('下课啦！叮当老师的小结')).toBeVisible()
    await expect(page.getByText(/首答率 100%/)).toBeVisible()
    expect(server.model.judgeCalls).toBe(0)
    await expect(other.getByRole('button', { name: /^dog/ })).toBeVisible()
    await expect.poll(() => events(server.log).filter(event => event.event === 'repeat_evaluated' && event.trace_id === `lesson-${aid}`).length).toBe(4)
    const repeats = events(server.log).filter(event => event.event === 'repeat_evaluated' && event.trace_id === `lesson-${aid}`)
    expect(repeats.map(event => event.result)).toEqual(['no_match', 'no_match', 'no_match', 'matched'])
    for (const event of repeats) {
      expect(event.module).toBe('classroom'); expect(event.lesson_id).toBe(aid)
      expect(event.purpose).toBe('repeat'); expect(event.source).toBe('target_match')
      expect(event.word_id).toBeGreaterThan(0)
      expect(Number.isInteger(event.duration_ms)).toBe(true); expect(event.duration_ms).toBeGreaterThanOrEqual(0)
      expect(event).not.toHaveProperty('text'); expect(event).not.toHaveProperty('meaning')
    }
    await expect.poll(() => events(server.log).filter(event => event.event === 'judge_decision' && event.trace_id === `lesson-${aid}`).length).toBe(1)
    const decisions = events(server.log).filter(event => event.event === 'judge_decision' && event.trace_id === `lesson-${aid}`)
    expect(decisions.map(event => ({ source: event.source, result: event.result }))).toEqual([{ source: 'l1', result: 'correct' }])
    await info.attach('repeat-route-evidence', { body: JSON.stringify({ lessonId: aid, otherLessonId: bid, providerCalls: server.model.judgeCalls, repeats, decisions }), contentType: 'application/json' })
  } finally { await context.close() }
})

test('RP02 negated and questioned controls never skip; an exact skip advances only its owner', async ({ page, request, server, browser, baseURL }, info) => {
  const a = await fixture(request, 'repeat-control'); const b = await fixture(request, 'control-other')
  // Legitimate student policy gives this explicit multi-input scenario enough time;
  // HTTP/expect/startup deadlines remain unchanged, and pre-submit phase is asserted.
  for (const f of [a, b]) await api(request, '/config/set', f.admin.token, { scope: 'student', studentId: f.student.user_id, key: 'timeoutRepeatS', value: 60, reason: 'multi-input repeat contract fixture' })
  const aid = await scheduled(request, a); const bid = await scheduled(request, b)
  const context = await browser.newContext({ baseURL }); const other = await context.newPage()
  try {
    await media(other, 'hold'); await login(other, b.username)
    const entered = other.waitForResponse(response => response.url().endsWith(`/classroom/lessons/${bid}/enter`) && response.request().method() === 'POST', { timeout: 30_000 })
    await other.goto(`/classroom/${bid}?e2e=1`)
    expect((await (await entered).json()).code).toBe(0)
    await expect(other.getByRole('button', { name: /^cat/ })).toBeVisible()
    await openRepeat(page, a.username, aid)
    const before = (await api(request, `/classroom/lessons/${aid}/enter`, a.student.token)).snapshot.context
    const commands: string[] = []
    for (const text of ['不跳过', '不要跳过', '不会吗', '跳过？', '不是不会', '跳过<不是命令>']) {
      const response = await postClassroom(page, `/classroom/lessons/${aid}/voice`, { text })
      expect(response.code).toBe(0)
      const frames = Array.isArray(response.data) ? response.data : response.data.frames
      const receipt = frames.find((frame: any) => frame.type === 'event_processed')
      expect(receipt).toBeDefined(); expect(receipt.command).toBe('')
      commands.push(receipt.command)
      expect((await api(request, `/classroom/lessons/${aid}/enter`, a.student.token)).snapshot.context).toEqual(before)
      await expect(page.getByText('轮到你读了！大声一点没关系', { exact: true })).toBeVisible()
      await expect(page.getByRole('button', { name: /^cat/ })).toBeVisible()
      expect(server.model.judgeCalls).toBe(0)
    }
    const skipped = await postClassroom(page, `/classroom/lessons/${aid}/voice`, { text: '跳过' })
    expect(skipped.code).toBe(0)
    const frames = Array.isArray(skipped.data) ? skipped.data : skipped.data.frames
    const receipt = frames.find((frame: any) => frame.type === 'event_processed')
    expect(receipt).toBeDefined(); expect(receipt.command).toBe('skip')
    commands.push(receipt.command)
    await expect(page.getByRole('button', { name: /^dog/ })).toBeVisible()
    await expect(other.getByRole('button', { name: /^cat/ })).toBeVisible()
    expect(server.model.judgeCalls).toBe(0)
    await expect.poll(() => events(server.log).filter(event => event.event === 'repeat_evaluated' && event.trace_id === `lesson-${aid}`).length).toBe(6)
    const repeats = events(server.log).filter(event => event.event === 'repeat_evaluated' && event.trace_id === `lesson-${aid}`)
    expect(repeats.map(event => event.result)).toEqual(Array(6).fill('no_match'))
    expect(commands).toEqual(['', '', '', '', '', '', 'skip'])
    await info.attach('exact-control-evidence', { body: JSON.stringify({ lessonId: aid, otherLessonId: bid, providerCalls: server.model.judgeCalls, commands, repeats, controls: events(server.log).filter(event => event.module === 'voicecmd' || event.event === 'repeat_evaluated') }), contentType: 'application/json' })
  } finally { await context.close() }
})


test('RP03 Unicode targets and internal apostrophes accept whole words; unsupported targets require explicit skip', async ({ page, request, server, browser, baseURL }, info) => {
  const words = [
    { word: 'café', meanings: ['咖啡馆'] },
    { word: "don't", meanings: ['不要'] },
    { word: 'one‘s turn', meanings: ['轮到某人'] },
    { word: 'cat/dog', meanings: ['复杂目标'] },
  ]
  const a = await fixture(request, 'repeat-unicode', words)
  const b = await fixture(request, 'repeat-unicode-other')
  const imported = await api(request, `/wordbooks/${a.book.ID}/words`, a.admin.token, undefined, 'GET')
  expect(imported.entries.map((entry: any) => entry.Word)).toEqual(words.map(entry => entry.word))
  const aid = await scheduled(request, a); const bid = await scheduled(request, b)
  const otherContext = await browser.newContext({ baseURL }); const other = await otherContext.newPage()
  const results: string[] = []
  try {
    await login(other, b.username)
    const otherBefore = await api(request, `/student/lessons?bookId=${b.book.ID}`, b.student.token, undefined, 'GET')
    expect(otherBefore.find((lesson: any) => lesson.ID === bid).Status).toBe('scheduled')
    await openRepeat(page, a.username, aid)
    for (let index = 0; index < words.length; index++) {
      for (let count = index * 4 + 1; count <= (index + 1) * 4; count++) {
        await expect.poll(() => page.evaluate(() => (window as any).__media.completed.length)).toBeGreaterThanOrEqual(count)
      }
      await expect(page.getByText('轮到你读了！大声一点没关系', { exact: true })).toBeVisible()
      const word = words[index].word
      await expect(page.getByRole('button', { name: new RegExp('^' + word.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')) })).toBeVisible()
      const before = (await api(request, `/classroom/lessons/${aid}/enter`, a.student.token)).snapshot.context
      const negative = index === 0 ? '"café"' : index === 1 ? "don't please" : index === 3 ? 'cat' : undefined
      if (negative !== undefined) {
        expect((await postClassroom(page, `/classroom/lessons/${aid}/voice`, { text: negative })).code).toBe(0)
        expect((await api(request, `/classroom/lessons/${aid}/enter`, a.student.token)).snapshot.context).toEqual(before)
        await expect(page.getByText('轮到你读了！大声一点没关系', { exact: true })).toBeVisible()
        results.push(index === 3 ? 'unsupported_target' : 'no_match')
      }
      const text = index === 0 ? 'cafe\u0301' : index === 1 ? "don't" : index === 2 ? "one's turn" : '跳过'
      const response = await postClassroom(page, `/classroom/lessons/${aid}/voice`, { text })
      expect(response.code).toBe(0)
      if (index === 3) {
        const frames = Array.isArray(response.data) ? response.data : response.data.frames
        expect(frames.find((frame: any) => frame.type === 'event_processed').command).toBe('skip')
      } else results.push('matched')
      expect(server.model.judgeCalls).toBe(0)
    }
    await expect(page.getByLabel('答案输入框')).toBeEnabled()
    const otherAfter = await api(request, `/student/lessons?bookId=${b.book.ID}`, b.student.token, undefined, 'GET')
    expect(otherAfter).toEqual(otherBefore)
    await expect(other).toHaveURL(/\/dashboard$/)
    await expect.poll(() => events(server.log).filter(event => event.event === 'repeat_evaluated' && event.trace_id === `lesson-${aid}`).length).toBe(6)
    const repeats = events(server.log).filter(event => event.event === 'repeat_evaluated' && event.trace_id === `lesson-${aid}`)
    expect(repeats.map(event => event.result)).toEqual(results)
    expect(results).toEqual(['no_match', 'matched', 'no_match', 'matched', 'matched', 'unsupported_target'])
    for (const event of repeats) {
      expect(event.purpose).toBe('repeat'); expect(event.source).toBe('target_match')
      expect(event.lesson_id).toBe(aid); expect(event.word_id).toBeGreaterThan(0)
      expect(Number.isInteger(event.duration_ms)).toBe(true); expect(event.duration_ms).toBeGreaterThanOrEqual(0)
      expect(event).not.toHaveProperty('text'); expect(event).not.toHaveProperty('meaning')
    }
    await info.attach('unicode-repeat-evidence', { body: JSON.stringify({ lessonId: aid, importedIds: imported.entries.map((entry: any) => entry.ID), results, repeats, providerCalls: server.model.judgeCalls }), contentType: 'application/json' })
  } finally { await otherContext.close() }
})
