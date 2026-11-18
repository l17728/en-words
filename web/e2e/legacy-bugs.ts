import { test, expect } from '@playwright/test'
import { api, fixture, scheduled, login, media } from './isolated-fixtures'

test('BUG-1+2 immediate selection uses current auth and UTF-8 nickname', async ({ page, request }) => {
  const f = await fixture(request, 'auth'); await login(page, f.username)
  await expect(page.getByText('今天也想见叮当老师').first()).toContainText('虫虫')
  await page.goto('/books')
  const response = page.waitForResponse(r => r.url().endsWith(`/student/books/${f.book.ID}/enable`))
  await page.locator('.ant-card', { hasText: f.book.Name }).getByRole('button', { name: '选择这个词库' }).click()
  expect((await (await response).json()).code).toBe(0)
  await expect(page.getByRole('button', { name: '开始摸底' })).toBeVisible()
  await expect(page.getByText('unauthorized', { exact: true })).toHaveCount(0)
})
test('BUG-5 re-placement replaces schedule and retains known words', async ({ page, request }) => {
  const f = await fixture(request, 'replace'); const old = await scheduled(request, f)
  await login(page, f.username); await page.goto(`/placement/${f.book.ID}`)
  await page.getByRole('button', { name: /^cat(?:，已标记认识)?$/ }).click()
  await page.getByRole('button', { name: '完成摸底，生成学习计划' }).click()
  await expect(page.getByRole('dialog', { name: '摸底完成！' })).toBeVisible()
  const rows = await api(request, `/student/lessons?bookId=${f.book.ID}`, f.student.token, undefined, 'GET')
  const active = rows.filter((l: any) => l.Status === 'scheduled')
  expect(active).toHaveLength(1); expect(active[0].ID).not.toBe(old)
  await page.goto(`/placement/${f.book.ID}`)
  await expect(page.getByRole('button', { name: /^cat(?:，已标记认识)?$/ })).toHaveAttribute('aria-pressed', 'true')
})
test('BUG-6+8 draft restores and can toggle, return to book square', async ({ page, request }) => {
  const f = await fixture(request, 'draft')
  await api(request, `/student/books/${f.book.ID}/enable`, f.student.token, {})
  await login(page, f.username); await page.goto(`/placement/${f.book.ID}`)
  const saved = page.waitForResponse(r => r.url().endsWith(`/books/${f.book.ID}/placement`) && r.request().method() === 'POST')
  await page.getByRole('button', { name: /^cat(?:，已标记认识)?$/ }).click()
  await page.getByRole('button', { name: /返回词库广场/ }).click(); await saved
  await page.goto(`/placement/${f.book.ID}`)
  await page.reload()
  await expect(page.getByRole('button', { name: /^cat(?:，已标记认识)?$/ })).toHaveAttribute('aria-pressed', 'true')
  const dog = page.getByRole('button', { name: /^dog(?:，已标记认识)?$/ })
  await dog.click(); await expect(dog).toHaveAttribute('aria-pressed', 'true')
  await dog.click(); await expect(dog).toHaveAttribute('aria-pressed', 'false')
  await page.getByRole('button', { name: /返回词库广场/ }).click(); await expect(page).toHaveURL(/\/books$/)
})
test('BUG-3+4+7 book card enters classroom, word and Chinese survive reentry', async ({ page, request }) => {
  const f = await fixture(request, 'reenter'); await scheduled(request, f)
  await media(page, 'hold'); await login(page, f.username)
  await page.getByRole('button', { name: `查看 ${f.book.Name} 的课程` }).click()
  await page.getByRole('button', { name: '进入课堂', exact: true }).click()
  await expect(page.getByText('已连接', { exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: /^cat/ })).toBeVisible(); await expect(page.getByText('猫', { exact: true })).toBeVisible()
  const path = new URL(page.url()).pathname
  await page.getByRole('button', { name: /^退\s*出$/ }).click()
  await page.getByRole('dialog').getByRole('button', { name: /^退\s*出$/ }).click()
  await expect(page).toHaveURL(/\/dashboard$/); await page.goto(path)
  await expect(page.getByText('已连接', { exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: /^cat/ })).toBeVisible(); await expect(page.getByText('猫', { exact: true })).toBeVisible()
})
test('BUG-9 only enabled books, empty schedule gives placement guidance', async ({ page, request }) => {
  const f = await fixture(request, 'zero'); await api(request, `/student/books/${f.book.ID}/enable`, f.student.token, {})
  await login(page, f.username)
  await expect(page.getByRole('button', { name: /^查看 .+ 的课程$/ })).toHaveCount(1)
  await page.getByRole('button', { name: `查看 ${f.book.Name} 的课程` }).click()
  await expect(page.getByRole('button', { name: /摸底/ }).first()).toBeVisible()
  expect(await api(request, `/student/lessons?bookId=${f.book.ID}`, f.student.token, undefined, 'GET')).toEqual([])
})
test('D21 five teaching words precede full-pool final quiz, MOCK speech', async ({ page, request }) => {
  const entries = [{ word: 'cat', meanings: ['猫'] }, { word: 'dog', meanings: ['狗'] }, { word: 'apple', meanings: ['苹果'] }, { word: 'bee', meanings: ['蜜蜂'] }, { word: 'fish', meanings: ['鱼'] }]
  const f = await fixture(request, 'd21', entries); const id = await scheduled(request, f)
  await media(page); await login(page, f.username)
  const frames: any[] = []
  page.on('websocket', ws => ws.on('framereceived', ({ payload }) => { try { frames.push(JSON.parse(String(payload))) } catch {} }))
  await page.goto(`/classroom/${id}`)
  const taught: string[] = []; const tested: string[] = []
  for (let i = 0; i < entries.length; i++) {
    await expect(page.getByText('轮到你读了！大声一点没关系', { exact: true })).toBeVisible()
    expect(frames.some(f => f.action?.type === 'start_quiz')).toBe(false)
    const word = (await page.getByRole('button', { name: /^(cat|dog|apple|bee|fish)$/ }).textContent())!.trim(); taught.push(word)
    await expect.poll(() => page.evaluate(() => !!(window as any).__media.recognition)).toBe(true)
    await page.evaluate(text => (window as any).__media.recognition.onresult({ results: [[{ transcript: text }]] }), word)
    await expect(page.getByText('轮到你读了！大声一点没关系', { exact: true })).toBeHidden()
  }
  expect(new Set(taught)).toEqual(new Set(entries.map(e => e.word)))
  for (let i = 0; i < entries.length; i++) {
    await expect(page.getByLabel('答案输入框')).toBeVisible()
    const word = (await page.getByRole('button', { name: /^(cat|dog|apple|bee|fish)$/ }).textContent())!.trim(); tested.push(word)
    const count = frames.filter(f => f.type === 'judge_result').length
    await page.getByLabel('答案输入框').fill(entries.find(e => e.word === word)!.meanings[0])
    await page.getByRole('button', { name: /^回\s*答$/ }).click()
    await expect.poll(() => frames.filter(f => f.type === 'judge_result').length).toBe(count + 1)
  }
  expect(new Set(tested)).toEqual(new Set(taught)); await expect(page.getByText('下课啦！叮当老师的小结')).toBeVisible()
})
test('BUG-10 local voice selection and four actual MOCK start/end callbacks', async ({ page, request }) => {
  const f = await fixture(request, 'tts'); const id = await scheduled(request, f)
  await media(page); await login(page, f.username); await page.goto(`/classroom/${id}?e2e=1`)
  await expect(page.getByText('轮到你读了！大声一点没关系', { exact: true })).toBeVisible()
  const segments = await page.evaluate(() => (window as any).__ttsDebug)
  expect(segments).toHaveLength(4); expect(segments.map((s: any) => s.lang)).toEqual(['en-US', 'en-US', 'en-US', 'zh-CN'])
  for (const s of segments) { expect(s.local).toBe(true); expect(s.startMs).not.toBeNull(); expect(s.status).toBe('completed') }
})
