import { expect, type Page, type APIRequestContext } from '@playwright/test'

export const password = 'fixture12345'
export async function api(request: APIRequestContext, path: string, token?: string, data?: unknown, method = 'POST'): Promise<any> {
  const response = await request.fetch(`/api/v1${path}`, { method, headers: token ? { Authorization: `Bearer ${token}` } : {}, ...(data === undefined ? {} : { data }) })
  const body = await response.json()
  expect(body.code, `${method} ${path}: ${body.msg}`).toBe(0)
  return body.data
}
export async function fixture(request: APIRequestContext, suffix: string, entries = [{ word: 'cat', meanings: ['猫'] }, { word: 'dog', meanings: ['狗'] }]) {
  if ((await api(request, '/bootstrap/status', undefined, undefined, 'GET')).needed) {
    await api(request, '/bootstrap', undefined, { username: 'isolated-admin', password, nickname: '测试管理员' })
  }
  const admin = await api(request, '/auth/login', undefined, { username: 'isolated-admin', password })
  const username = `fixture-${suffix}-${Date.now()}`
  await api(request, '/admin/users', admin.token, { username, password, nickname: '虫虫', role: 'student' })
  const student = await api(request, '/auth/login', undefined, { username, password })
  const book = await api(request, '/wordbooks', admin.token, { name: username, grade: 'g7' })
  await api(request, `/wordbooks/${book.ID}/import/commit`, admin.token, { entries })
  await api(request, `/wordbooks/${book.ID}/publish`, admin.token, {})
  return { admin, student, book, username, entries }
}
export async function scheduled(request: APIRequestContext, f: Awaited<ReturnType<typeof fixture>>) {
  await api(request, `/student/books/${f.book.ID}/enable`, f.student.token, {})
  await api(request, `/student/books/${f.book.ID}/placement`, f.student.token, { knownWordIds: [], finish: true })
  const lessons = await api(request, `/student/lessons?bookId=${f.book.ID}`, f.student.token, undefined, 'GET')
  expect(lessons.filter((l: any) => l.Status === 'scheduled')).toHaveLength(1)
  return lessons.find((l: any) => l.Status === 'scheduled').ID as number
}
export async function login(page: Page, username: string) {
  await page.goto('/login')
  await page.getByLabel('用户名', { exact: true }).fill(username)
  await page.getByLabel('密码', { exact: true }).fill(password)
  await page.getByRole('button', { name: '开始学习', exact: true }).click()
  await expect(page).toHaveURL(username === 'isolated-admin' ? /\/admin$/ : /\/dashboard$/)
}
export async function media(page: Page, mode = 'success', duration = 80) {
  await page.addInitScript(({ mode, duration }) => {
    const state = { spoken: [] as any[], utterances: [] as any[], completed: [] as any[], canceled: 0, recognition: null as any, mode }
    ;(window as any).__media = state
    class Utterance { constructor(public text: string) {} }
    const timers = new Set<ReturnType<typeof setTimeout>>()
    Object.defineProperty(window, 'SpeechSynthesisUtterance', { configurable: true, value: Utterance })
    Object.defineProperty(window, 'speechSynthesis', { configurable: true, value: {
      getVoices: () => state.mode === 'no_voice' ? [] : ['en-US', 'zh-CN'].map(lang => ({ name: `MOCK ${lang}`, lang, localService: true })),
      speak: (u: any) => {
        state.spoken.push({ text: u.text, lang: u.lang }); state.utterances.push(u)
        if (state.mode === 'throw') throw new Error('Fixture synthesis failure')
        if (state.mode === 'timeout' || state.mode === 'hold' || (state.mode === 'hold_second' && state.spoken.length > 1)) return
        u.onstart?.()
        const timer = setTimeout(() => {
          timers.delete(timer)
          if (state.mode === 'error') { u.onerror?.(); u.onend?.(); return }
          state.completed.push({ text: u.text, lang: u.lang }); u.onend?.()
        }, duration)
        timers.add(timer)
      },
      cancel: () => { state.canceled++; for (const timer of timers) clearTimeout(timer); timers.clear() },
    } })
    class Recognition {
      onstart?: () => void
      starts = 0
      start() { this.starts++; state.recognition = this; this.onstart?.() }
      stop() { if (state.recognition === this) state.recognition = null }
      abort() { this.stop() }
    }
    ;(window as any).SpeechRecognition = Recognition
    ;(window as any).webkitSpeechRecognition = Recognition
  }, { mode, duration })
}
