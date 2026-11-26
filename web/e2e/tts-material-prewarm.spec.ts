import { test, expect } from '@playwright/test'
import { randomUUID } from 'node:crypto'
import { api, fixture, scheduled, password, login } from './isolated-fixtures'


test.describe('TTS material prewarm', () => {
  test.beforeEach(async ({ request }) => {
    await request.post('http://127.0.0.1:18082/__control/reset')
  })

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

  test('progress uses manifest ETag and rejects a different student', async ({ request }) => {
    const etagSuffix = randomUUID().replaceAll('-', '')
    const etagWord = `etagfixture${etagSuffix}`
    const a = await fixture(request, 'tts-etag-a', [{ word: etagWord, meanings: [`猫${etagSuffix}`] }])
    const b = await fixture(request, 'tts-etag-b', [{ word: 'dog', meanings: ['狗'] }])
    const lesson = await scheduled(request, a)
    const first = await request.fetch(`/api/v1/classroom/lessons/${lesson}/speech-materials/progress`, {
      headers: { Authorization: `Bearer ${a.student.token}` },
    })
    expect(first.status()).toBe(200)
    const revision = first.headers()['etag']
    expect(revision).toMatch(/^"[a-f0-9]{64}"$/)
    const unchanged = await request.fetch(`/api/v1/classroom/lessons/${lesson}/speech-materials/progress`, {
      headers: { Authorization: `Bearer ${a.student.token}`, 'If-None-Match': revision },
    })
    expect(unchanged.status()).toBe(304)
    const denied = await request.fetch(`/api/v1/classroom/lessons/${lesson}/speech-materials/progress`, {
      headers: { Authorization: `Bearer ${b.student.token}` },
    })
    expect(denied.status()).toBe(200)
    expect((await denied.json()).code).not.toBe(0)
    await api(request, `/classroom/lessons/${lesson}/speech-materials/ensure`, a.student.token, {})
    await expect.poll(async () => (await api(request, `/classroom/lessons/${lesson}/speech-materials/progress`, a.student.token, undefined, 'GET')).can_start).toBe(true)
    const changed = await request.fetch(`/api/v1/classroom/lessons/${lesson}/speech-materials/progress`, {
      headers: { Authorization: `Bearer ${a.student.token}`, 'If-None-Match': revision },
    })
    expect(changed.status()).toBe(200)
    expect(changed.headers()['etag']).not.toBe(revision)
  })

  test('all ready materials are reused without another upstream synthesis', async ({ request }) => {
    const suffix = randomUUID().replaceAll('-', '')
    const f = await fixture(request, 'tts-cache-hit', [{ word: `cachefixture${suffix}`, meanings: [`n. 缓存样本${suffix}`] }])
    const lesson = await scheduled(request, f)
    await api(request, `/classroom/lessons/${lesson}/speech-materials/ensure`, f.student.token, {})
    await expect.poll(async () => (await api(request, `/classroom/lessons/${lesson}/speech-materials/progress`, f.student.token, undefined, 'GET')).can_start).toBe(true)
    const second = await api(request, `/classroom/lessons/${lesson}/speech-materials/ensure`, f.student.token, {})
    expect(second.ready).toBe(2)
    expect(second.missing).toBe(0)
    const stats = await request.get('http://127.0.0.1:18082/__control/stats')
    expect((await stats.json()).calls).toBe(2)
  })

  test('classroom entry is gated until its current profile materials are ready', async ({ request }) => {
    const suffix = randomUUID().replaceAll('-', '')
    const f = await fixture(request, 'tts-gate', [{ word: `gatefixture${suffix}`, meanings: [`n. 门禁样本${suffix}`] }])
    const lesson = await scheduled(request, f)
    const blocked = await request.fetch(`/api/v1/classroom/lessons/${lesson}/enter`, {
      method: 'POST', headers: { Authorization: `Bearer ${f.student.token}`, 'Content-Type': 'application/json' }, data: {},
    })
    const blockedBody = await blocked.json()
    expect(blockedBody.code).not.toBe(0)
    expect(blockedBody.msg).toBe('speech_materials_pending')
    await api(request, `/classroom/lessons/${lesson}/speech-materials/ensure`, f.student.token, {})
    await expect.poll(async () => (await api(request, `/classroom/lessons/${lesson}/speech-materials/progress`, f.student.token, undefined, 'GET')).can_start).toBe(true)
    const entered = await api(request, `/classroom/lessons/${lesson}/enter`, f.student.token, {})
    expect(entered.ticket).toBeTruthy()
  })

  test('prewarm uses the same normalized English and Chinese text as classroom playback', async ({ request }) => {
    const suffix = randomUUID().replaceAll('-', '')
    const f = await fixture(request, 'tts-normalization', [{ word: `ASAP-${suffix}`, meanings: [`abbr. 尽快-${suffix}`] }])
    const lesson = await scheduled(request, f)
    await api(request, `/classroom/lessons/${lesson}/speech-materials/ensure`, f.student.token, {})
    await expect.poll(async () => (await api(request, `/classroom/lessons/${lesson}/speech-materials/progress`, f.student.token, undefined, 'GET')).can_start).toBe(true)
    const stats = await request.get('http://127.0.0.1:18082/__control/stats')
    const inputs = (await stats.json()).inputs
    expect(inputs.some((input: string) => input.startsWith(`A S A P-${suffix}`))).toBe(true)
    expect(inputs).toContain(`尽快-${suffix}`)
    expect(inputs.some((input: string) => input.includes('ASAP'))).toBe(false)
    expect(inputs.some((input: string) => input.includes('abbr.'))).toBe(false)
  })

  test('two concurrent students share one manifest revision', async ({ request }) => {
    const suffix = randomUUID().replaceAll('-', '')
    const sharedWord = `sharefixture${suffix}`
    const sharedMeaning = `分享样本${suffix}`
    const f = await fixture(request, 'tts-share', [{ word: sharedWord, meanings: [sharedMeaning] }])
    const firstLesson = await scheduled(request, f)
    const secondUsername = `fixture-tts-share-b-${Date.now()}`
    await api(request, '/admin/users', f.admin.token, { username: secondUsername, password, nickname: '第二学生', role: 'student' })
    const secondLogin = await api(request, '/auth/login', undefined, { username: secondUsername, password })
    const secondBook = await api(request, '/wordbooks', f.admin.token, { name: `tts-share-b-${Date.now()}`, grade: 'g7' })
    await api(request, `/wordbooks/${secondBook.ID}/import/commit`, f.admin.token, { entries: [{ word: sharedWord, meanings: [sharedMeaning] }] })
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
    await expect.poll(async () => {
      const progress = await api(request, `/classroom/lessons/${firstLesson}/speech-materials/progress`, f.student.token, undefined, 'GET')
      return progress.can_start
    }, { timeout: 15_000 }).toBe(true)
    await expect.poll(async () => {
      const response = await request.get('http://127.0.0.1:18082/__control/stats')
      return (await response.json()).calls
    }).toBe(2)
  })

  test('profile changes isolate TTS assets and switching back reuses them', async ({ request }) => {
    const suffix = randomUUID().replaceAll('-', '')
    const f = await fixture(request, 'tts-profile', [{ word: `profilefixture${suffix}`, meanings: [`n. 配置样本${suffix}`] }])
    const lesson = await scheduled(request, f)
    const defaultManifest = await api(request, `/classroom/lessons/${lesson}/speech-materials/scan`, f.student.token, {})
    await api(request, `/classroom/lessons/${lesson}/speech-materials/ensure`, f.student.token, {})
    await expect.poll(async () => (await api(request, `/classroom/lessons/${lesson}/speech-materials/progress`, f.student.token, undefined, 'GET')).can_start).toBe(true)
    const profiles = await api(request, '/admin/model-profiles', f.admin.token, undefined, 'GET')
    const active = profiles.find((profile: any) => profile.active) || profiles[0]
    const alt = await api(request, '/admin/model-profiles', f.admin.token, {
      name: `fixture-alt-${Date.now()}`,
      text_service: active.text_service,
      asr_service: active.asr_service,
      tts_zh_service: 'tts-alt',
      tts_en_service: active.tts_en_service,
    })
    await api(request, `/admin/model-profiles/${alt.id}/apply`, f.admin.token, {})
    const alternate = await api(request, `/classroom/lessons/${lesson}/speech-materials/ensure`, f.student.token, {})
    expect(alternate.manifest_revision).not.toBe(defaultManifest.manifest_revision)
    expect(alternate.missing).toBe(1)
    await expect.poll(async () => (await api(request, `/classroom/lessons/${lesson}/speech-materials/progress`, f.student.token, undefined, 'GET')).can_start).toBe(true)
    const alternateStats = await request.get('http://127.0.0.1:18082/__control/stats')
    expect((await alternateStats.json()).calls).toBe(3)
    await api(request, `/admin/model-profiles/${active.id}/apply`, f.admin.token, {})
    const restored = await api(request, `/classroom/lessons/${lesson}/speech-materials/ensure`, f.student.token, {})
    expect(restored.ready).toBe(2)
    expect(restored.missing).toBe(0)
    const restoredStats = await request.get('http://127.0.0.1:18082/__control/stats')
    expect((await restoredStats.json()).calls).toBe(3)
  })

  test('admin forced refresh is restricted and regenerates only current profile asset', async ({ request }) => {
    const suffix = randomUUID().replaceAll('-', '')
    const refreshWord = `refreshfixture${suffix}`
    const f = await fixture(request, 'tts-refresh', [{ word: refreshWord, meanings: [`n. 刷新样本${suffix}`] }])
    const lesson = await scheduled(request, f)
    await api(request, `/classroom/lessons/${lesson}/speech-materials/ensure`, f.student.token, {})
    await expect.poll(async () => (await api(request, `/classroom/lessons/${lesson}/speech-materials/progress`, f.student.token, undefined, 'GET')).can_start).toBe(true)
    const denied = await request.fetch('/api/v1/admin/speech/refresh', {
      method: 'POST', headers: { Authorization: `Bearer ${f.student.token}`, 'Content-Type': 'application/json' },
      data: { text: refreshWord, lang: 'en' },
    })
    expect((await denied.json()).code).not.toBe(0)
    const before = await request.get('http://127.0.0.1:18082/__control/stats')
    expect((await before.json()).calls).toBe(2)
    const refreshed = await request.fetch('/api/v1/admin/speech/refresh', {
      method: 'POST', headers: { Authorization: `Bearer ${f.admin.token}`, 'Content-Type': 'application/json' },
      data: { text: refreshWord, lang: 'en' },
    })
    expect(refreshed.status()).toBe(200)
    expect((await refreshed.json()).code).toBe(0)
    const after = await request.get('http://127.0.0.1:18082/__control/stats')
    expect((await after.json()).calls).toBe(3)
    const cached = await api(request, `/classroom/lessons/${lesson}/speech-materials/ensure`, f.student.token, {})
    expect(cached.ready).toBe(2)
    expect(cached.missing).toBe(0)
  })

  test('failed jobs can be retried without losing failed progress', async ({ request }) => {
    await request.post('http://127.0.0.1:18082/__control/config', { data: { fail: true } })
    const suffix = randomUUID().replaceAll('-', '')
    const f = await fixture(request, 'tts-worker-failure', [{ word: `retryfixture${suffix}`, meanings: [`n. 重试样本${suffix}`] }])
    const lesson = await scheduled(request, f)
    const ensure = await api(request, `/classroom/lessons/${lesson}/speech-materials/ensure`, f.student.token, {})
    expect(ensure.missing).toBe(2)
    await expect.poll(async () => {
      const progress = await api(request, `/classroom/lessons/${lesson}/speech-materials/progress`, f.student.token, undefined, 'GET')
      return progress.failed
    }, { timeout: 15_000 }).toBe(2)
    const first = await api(request, `/classroom/lessons/${lesson}/speech-materials/progress`, f.student.token, undefined, 'GET')
    expect(first.attempts).toBe(2)
    expect(first.failure_codes).toContain('synthesis_failed')
    await api(request, `/classroom/lessons/${lesson}/speech-materials/ensure`, f.student.token, {})
    await expect.poll(async () => {
      const progress = await api(request, `/classroom/lessons/${lesson}/speech-materials/progress`, f.student.token, undefined, 'GET')
      return progress.attempts
    }, { timeout: 15_000 }).toBe(4)
    const stats = await request.get('http://127.0.0.1:18082/__control/stats')
    expect((await stats.json()).calls).toBe(4)

  })

  test('canceled late workers do not overwrite their canceled job state', async ({ request }) => {
    await request.post('http://127.0.0.1:18082/__control/config', { data: { delayMs: 2000 } })
    const suffix = randomUUID().replaceAll('-', '')
    const f = await fixture(request, 'tts-cancel', [{ word: `cancelfixture${suffix}`, meanings: [`取消样本${suffix}`] }])
    const lesson = await scheduled(request, f)
    await api(request, `/classroom/lessons/${lesson}/speech-materials/ensure`, f.student.token, {})
    await expect.poll(async () => (await (await request.get('http://127.0.0.1:18082/__control/stats')).json()).calls, { timeout: 15_000 }).toBe(2)
    const canceled = await api(request, `/classroom/lessons/${lesson}/speech-materials/cancel`, f.student.token, {})
    expect(canceled.canceled).toBe(true)
    await expect.poll(async () => (await api(request, `/classroom/lessons/${lesson}/speech-materials/progress`, f.student.token, undefined, 'GET')).canceled).toBeGreaterThan(0)
    await expect.poll(async () => {
      const progress = await api(request, `/classroom/lessons/${lesson}/speech-materials/progress`, f.student.token, undefined, 'GET')
      return progress.can_start
    }, { timeout: 15_000 }).toBe(true)
    const final = await api(request, `/classroom/lessons/${lesson}/speech-materials/progress`, f.student.token, undefined, 'GET')
    expect(final.canceled).toBe(1)
  })

  test('browser classroom keeps a fixed preparation status region', async ({ page, request }) => {
    await request.post('http://127.0.0.1:18082/__control/config', { data: { delayMs: 900 } })
    const suffix = randomUUID().replaceAll('-', '')
    const f = await fixture(request, 'tts-ui', [{ word: `delayfixture${suffix}`, meanings: [`延迟样本${suffix}`] }])
    const lesson = await scheduled(request, f)
    await page.addInitScript(() => { (window as any).__TEST_VOICE__ = true })
    await login(page, f.username)
    await page.goto(`/classroom/${lesson}`)
    const status = page.getByRole('status', { name: '正在准备语音素材' })
    await expect(status).toHaveCount(1)
    const box = await status.boundingBox()
    if (box) expect(box.height).toBeGreaterThan(20)
  })

  test('classroom rejects progress from a different manifest revision', async ({ page, request }) => {
    await request.post('http://127.0.0.1:18082/__control/config', { data: { delayMs: 1500 } })
    const suffix = randomUUID().replaceAll('-', '')
    const f = await fixture(request, 'tts-stale-progress', [{ word: `staleprogress${suffix}`, meanings: [`旧版本样本${suffix}`] }])
    const lesson = await scheduled(request, f)
    await login(page, f.username)
    await page.route(`**/api/v1/classroom/lessons/${lesson}/speech-materials/progress`, async route => {
      const response = await route.fetch()
      const envelope = await response.json()
      envelope.data.manifest_revision = 'f'.repeat(64)
      await route.fulfill({ response, body: JSON.stringify(envelope) })
    })
    await page.goto(`/classroom/${lesson}`)
    await expect(page.getByText('课程语音素材清单已变化，请重新准备')).toBeVisible({ timeout: 15_000 })
    await expect(page.getByText('课堂进行中')).toHaveCount(0)
  })

  test('failed generation is visible and requires an explicit text fallback', async ({ page, request }) => {
    await request.post('http://127.0.0.1:18082/__control/config', { data: { fail: true } })
    const suffix = randomUUID().replaceAll('-', '')
    const f = await fixture(request, 'tts-fallback', [{ word: `fallbackfixture${suffix}`, meanings: [`n. 降级样本${suffix}`] }])
    const lesson = await scheduled(request, f)
    await login(page, f.username)
    await page.goto(`/classroom/${lesson}`)
    await expect(page.getByRole('status', { name: '正在准备语音素材' })).toContainText('语音素材生成失败', { timeout: 15_000 })
    await expect(page.getByRole('button', { name: '使用文字继续' })).toBeVisible()
    await page.getByRole('button', { name: '使用文字继续' }).click()
    await expect(page.getByText('已连接')).toBeVisible({ timeout: 15_000 })
    await expect.poll(() => page.evaluate(() => Object.entries(sessionStorage).some(([key, value]) => key.startsWith('enwords.text-only.') && value === 'pending'))).toBe(true)
  })
})
