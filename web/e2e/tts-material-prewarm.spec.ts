import { test, expect } from '@playwright/test'
import { api, fixture, scheduled, password, login } from './isolated-fixtures'

test.describe('TTS material prewarm', () => {
  test('scan exposes a stable manifest and progress contract', async ({ request }) => {
    const f = await fixture(request, 'tts-scan', [{ word: 'cat', meanings: ['猫'] }])
    const lesson = await scheduled(request, f)
    const scan = await api(request, `/classroom/lessons/${lesson}/speech-materials/scan`, f.student.token, {})
    expect(scan.lesson_id).toBe(lesson)
    expect(scan.manifest_revision).toMatch(/^[a-f0-9]{64}$/)
    expect(scan.total).toBe(2)
    expect(scan).toHaveProperty('ready')
    const progress = await api(request, `/classroom/lessons/${lesson}/speech-materials/progress`, f.student.token, undefined, 'GET')
    expect(progress.manifest_revision).toBe(scan.manifest_revision)
    expect(progress.total).toBe(2)
  })

  test('two concurrent students share one manifest revision', async ({ request }) => {
    const f = await fixture(request, 'tts-share', [{ word: 'cat', meanings: ['猫'] }])
    const firstLesson = await scheduled(request, f)
    const second = await api(request, '/admin/users', f.admin.token, { username: `fixture-tts-share-b-${Date.now()}`, password, nickname: '第二学生', role: 'student' })
    const secondLogin = await api(request, '/auth/login', undefined, { username: second.username, password })
    const secondBook = await api(request, '/wordbooks', f.admin.token, { name: `tts-share-b-${Date.now()}`, grade: 'g7' })
    await api(request, `/wordbooks/${secondBook.ID}/import/commit`, f.admin.token, { entries: [{ word: 'cat', meanings: ['猫'] }] })
    await api(request, `/wordbooks/${secondBook.ID}/publish`, f.admin.token, {})
    await api(request, `/student/books/${secondBook.ID}/enable`, secondLogin.token, {})
    await api(request, `/student/books/${secondBook.ID}/placement`, secondLogin.token, { knownWordIds: [], finish: true })
    const lessons = await api(request, `/student/lessons?bookId=${secondBook.ID}`, secondLogin.token, undefined, 'GET')
    const [a, b] = await Promise.all([
      api(request, `/classroom/lessons/${firstLesson}/speech-materials/ensure`, f.student.token, {}),
      api(request, `/classroom/lessons/${lessons[0].ID}/speech-materials/ensure`, secondLogin.token, {}),
    ])
    expect(a.manifest_revision).toBe(b.manifest_revision)
    expect(a.total).toBe(2); expect(b.total).toBe(2)
  })

  test('browser classroom keeps a fixed preparation status region', async ({ page, request }) => {
    const f = await fixture(request, 'tts-ui', [{ word: 'cat', meanings: ['猫'] }])
    const lesson = await scheduled(request, f)
    await page.addInitScript(() => { (window as any).__TEST_VOICE__ = true })
    await login(page, f.username)
    await page.goto(`/classroom/${lesson}`)
    const status = page.getByRole('status', { name: '正在准备语音素材' })
    await expect(status).toHaveCount(1)
    const box = await status.boundingBox()
    if (box) expect(box.height).toBeGreaterThan(20)
  })
})
