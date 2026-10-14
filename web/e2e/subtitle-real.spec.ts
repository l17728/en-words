import { test, expect } from '@playwright/test'
import { api, fixture, scheduled, login } from './isolated-fixtures'

// Real browser voices + real local synthesis. No ASR, answers or judge calls.
test('real English and local Chinese playback each expose and clear the actual subtitle', async ({ page, request }, info) => {
  const f = await fixture(request, 'real-subtitle', [{ word: 'cat', meanings: ['猫是一种常见的家养动物。'] }])
  const id = await scheduled(request, f)
  for (const [key, value] of Object.entries({ ttsEnRepeat: 3, ttsZhRepeat: 1,
    ttsIntervalMs: 300, ttsZhIntervalMs: 300, ttsSwitchIntervalMs: 300 })) {
    await api(request, '/config/set', f.admin.token, { scope: 'student', studentId: f.student.user_id, key, value, reason: 'real subtitle acceptance' })
  }
  await page.addInitScript(() => {
    const state = { captions: [] as Array<{ text: string; at: number }>, audio: [] as any[], english: [] as any[] }
    ;(window as any).__realSubtitles = state
    new MutationObserver(() => {
      const region = document.querySelector('[aria-label="当前朗读字幕"]')
      const text = region?.lastElementChild?.textContent || ''
      if (state.captions.at(-1)?.text !== text) state.captions.push({ text, at: performance.now() })
    }).observe(document, { subtree: true, childList: true, characterData: true })
    const speak = window.speechSynthesis.speak.bind(window.speechSynthesis)
    window.speechSynthesis.speak = utterance => {
      const entry = { text: utterance.text, start: 0, end: 0, errors: 0 }
      state.english.push(entry)
      utterance.addEventListener('start', () => { entry.start = performance.now() })
      utterance.addEventListener('end', () => { entry.end = performance.now() })
      utterance.addEventListener('error', () => { entry.errors++ })
      speak(utterance)
    }
    const NativeAudio = window.Audio
    ;(window as any).Audio = function(src?: string) {
      const player = new NativeAudio(src); const entry = { start: 0, end: 0, errors: 0 }
      state.audio.push(entry)
      player.addEventListener('playing', () => { entry.start = performance.now() })
      player.addEventListener('ended', () => { entry.end = performance.now() })
      player.addEventListener('error', () => { entry.errors++ })
      return player
    }
  })
  const responses: any[] = []; const asr: string[] = []
  page.on('response', response => {
    if (response.url().endsWith('/speech/synthesize')) responses.push({ status: response.status(), mime: response.headers()['content-type'] })
  })
  page.on('request', r => { if (r.url().endsWith('/transcribe')) asr.push(r.url()) })
  await login(page, f.username)
  await page.goto(`/classroom/${id}?e2e=1`)
  try {
    await expect(page.getByRole('status', { name: '当前朗读字幕' })).toContainText('猫是一种常见的家养动物。', { timeout: 90_000 })
    await page.screenshot({ path: info.outputPath('actual-chinese-caption.png'), fullPage: true })
    await info.attach('actual-chinese-caption', { path: info.outputPath('actual-chinese-caption.png'), contentType: 'image/png' })
    await expect(page.getByText('轮到你读了！大声一点没关系', { exact: true })).toBeVisible({ timeout: 90_000 })
    await expect(page.getByRole('status', { name: '当前朗读字幕' })).toHaveText('')
    const state = await page.evaluate(() => (window as any).__realSubtitles)
    expect(responses).toEqual([{ status: 200, mime: 'audio/wav' }]); expect(asr).toEqual([])
    expect(state.captions.filter((c: any) => c.text).map((c: any) => c.text)).toEqual(['cat', 'cat', 'cat', '猫是一种常见的家养动物。'])
    expect(state.english).toHaveLength(3); expect(state.audio).toHaveLength(1)
    const media = [...state.english, ...state.audio]
    expect(media.every(row => row.start > 0 && row.end > row.start && row.errors === 0)).toBe(true)
    for (const [index, row] of media.entries()) {
      const caption = state.captions.filter((c: any) => c.text)[index]
      expect(caption.at).toBeGreaterThanOrEqual(row.start)
      expect(caption.at).toBeLessThan(row.end)
    }
  } finally {
    await info.attach('real-subtitle-evidence', { body: JSON.stringify({ responses, asr,
      state: await page.evaluate(() => (window as any).__realSubtitles) }), contentType: 'application/json' })
  }
})
