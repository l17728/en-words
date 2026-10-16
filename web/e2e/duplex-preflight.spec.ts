import { test, expect } from './recovery-server'
import { fixture, scheduled, login } from './isolated-fixtures'
import type { Page } from '@playwright/test'

async function audio(page: Page, mode: 'settings' | 'reject' | 'late' | 'module-late') {
  await page.addInitScript(mode => {
    const state = { requests: 0, stops: 0, closed: 0, release: null as null | (() => void), nodes: [] as any[], rejectModule: null as null | (() => void) }
    ;(window as any).__preflight = state
    const track = { readyState: 'live', onended: null, getSettings: () => ({ echoCancellation: false, noiseSuppression: true }), stop: () => state.stops++ }
    const stream = { getTracks: () => [track], getAudioTracks: () => [track] }
    Object.defineProperty(navigator, 'mediaDevices', { configurable: true, value: {
      getSupportedConstraints: () => ({ echoCancellation: true, noiseSuppression: true }),
      getUserMedia: async () => { state.requests++; if (mode === 'reject') throw new DOMException('Denied', 'NotAllowedError'); if (mode === 'late') await new Promise<void>(resolve => { state.release = resolve }); return stream },
    } })
    class Context {
      sampleRate = 16000; destination = {}; audioWorklet = { addModule: async () => { if (mode === 'module-late') await new Promise<void>((_, reject) => { state.rejectModule = () => reject(new Error('Fixture audio module failed')) }) } }
      createMediaStreamSource() { return { connect() {}, disconnect() {} } }
      async resume() {} async close() { state.closed++ }
    }
    class Worklet {
      port = { onmessage: null as any, close() {}, postMessage() {} }
      constructor() { state.nodes.push(this) } connect() {} disconnect() {}
    }
    Object.defineProperty(window, 'AudioContext', { configurable: true, value: Context })
    Object.defineProperty(window, 'AudioWorkletNode', { configurable: true, value: Worklet })
  }, mode)
}
async function pilot(page: Page, enabled: boolean) {
  await page.route('**/duplex-capabilities', route => route.fulfill({ json: { code: 0, data: { pilot_enabled: enabled, native_duplex: false, automatic_teaching: false, mode: 'preflight', reasons: [], limits: {} } } }))
}

test.describe('server pilot disabled', () => {
  test.use({ duplexPilot: false })
test('FD01 disabled pilot cannot request microphone even with query switch', async ({ page, request }) => {
  const f = await fixture(request, 'preflight-off'); const id = await scheduled(request, f)
  await audio(page, 'settings'); await login(page, f.username)
  await page.goto(`/classroom/${id}/voice-preflight?pilot=1`)
  await expect(page.getByText('此课堂尚未开放麦克风预检')).toBeVisible()
  await expect(page.getByRole('button', { name: '开始麦克风预检' })).toBeDisabled()
  expect(await page.evaluate(() => (window as any).__preflight.requests)).toBe(0)
})

})

test('FD01 actual false and unknown remain distinct; capture is local and stop rejects late callbacks', async ({ page, request }) => {
  const f = await fixture(request, 'preflight-settings'); const id = await scheduled(request, f)
  await audio(page, 'settings'); await pilot(page, true); await login(page, f.username)
  const uploads: string[] = []; page.on('request', r => { if (/transcribe|synthesize|voice-text|events/.test(r.url()) && r.method() === 'POST') uploads.push(r.url()) })
  await page.goto(`/classroom/${id}/voice-preflight`)
  await page.getByRole('button', { name: '开始麦克风预检' }).click()
  await expect(page.getByTestId('echoCancellation')).toContainText('实际关闭')
  await expect(page.getByTestId('autoGainControl')).toContainText('实际未知')
  await expect(page.getByText('正在本机预检', { exact: true })).toBeVisible()
  await page.evaluate(() => { const state = (window as any).__preflight; state.oldMessage = state.nodes[0].port.onmessage; state.oldMessage({ data: new Float32Array(320).fill(0.2) }) })
  await expect(page.getByText('声音事件：短起音候选')).toBeVisible()
  await page.getByRole('button', { name: '停止预检' }).click()
  await page.evaluate(() => (window as any).__preflight.oldMessage({ data: new Float32Array(320).fill(0.2) }))
  await expect(page.getByText('已停止', { exact: true })).toBeVisible()
  await expect(page.getByText('声音事件：等待声音')).toBeVisible()
  expect(await page.evaluate(() => ({ stops: (window as any).__preflight.stops, closed: (window as any).__preflight.closed }))).toEqual({ stops: 1, closed: 1 })
  await page.getByRole('button', { name: '开始麦克风预检' }).click()
  await expect(page.getByText('正在本机预检', { exact: true })).toBeVisible()
  await page.evaluate(() => (window as any).__preflight.nodes.at(-1).port.onmessage({ data: { overflow: true } }))
  await expect(page.getByText('音频处理失败，已停止', { exact: true })).toBeVisible()
  expect(uploads).toEqual([])
})

test('FD06 denied microphone remains recoverable and exposes text return', async ({ page, request }) => {
  const f = await fixture(request, 'preflight-denied'); const id = await scheduled(request, f)
  await audio(page, 'reject'); await pilot(page, true); await login(page, f.username)
  await page.goto(`/classroom/${id}/voice-preflight`)
  await page.getByRole('button', { name: '开始麦克风预检' }).click()
  await expect(page.getByRole('alert').filter({ hasText: '无法使用麦克风' })).toBeVisible()
  await expect(page.getByRole('button', { name: '开始麦克风预检' })).toBeEnabled()
  await expect(page.getByRole('link', { name: '返回课堂，使用文字继续' })).toHaveAttribute('href', `/classroom/${id}`)
})

test('FD11 permission resolved after cancellation closes track and never starts old session', async ({ page, request }) => {
  const f = await fixture(request, 'preflight-late'); const id = await scheduled(request, f)
  await audio(page, 'late'); await pilot(page, true); await login(page, f.username)
  await page.goto(`/classroom/${id}/voice-preflight`)
  await page.getByRole('button', { name: '开始麦克风预检' }).click()
  await expect(page.getByText('等待麦克风权限', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: '停止预检' }).click()
  await page.evaluate(() => (window as any).__preflight.release())
  await expect.poll(() => page.evaluate(() => (window as any).__preflight.stops)).toBe(1)
  await expect(page.getByText('已停止', { exact: true })).toBeVisible()
  expect(await page.evaluate(() => (window as any).__preflight.nodes.length)).toBe(0)
})

test('FD01 real capability route gates owner and exposes no remote credentials', async ({ page, request }) => {
  const a = await fixture(request, 'preflight-owner'); const b = await fixture(request, 'preflight-other')
  const id = await scheduled(request, a)
  const path = `/api/v1/classroom/lessons/${id}/duplex-capabilities`
  const anonymous = await request.get(path)
  expect(anonymous.status()).toBe(200); expect((await anonymous.json()).code).toBe(401)
  const foreign = await request.get(path, { headers: { Authorization: `Bearer ${b.student.token}` } })
  expect(foreign.status()).toBe(200); expect((await foreign.json()).code).toBe(403)
  const response = await request.get(path, { headers: { Authorization: `Bearer ${a.student.token}` } })
  expect(response.status()).toBe(200)
  const { data } = await response.json()
  expect(Object.keys(data).sort()).toEqual(['automatic_teaching', 'limits', 'mode', 'native_duplex', 'pilot_enabled', 'reasons'])
  expect(data).toMatchObject({ pilot_enabled: true, native_duplex: false, automatic_teaching: false, mode: 'preflight' })
  await audio(page, 'settings'); await login(page, a.username); await page.goto(`/classroom/${id}/voice-preflight`)
  await expect(page.getByRole('button', { name: '开始麦克风预检' })).toBeEnabled()
})

test('FD11 switching owned lesson resets active preflight and processor failure stops all resources', async ({ page, request }) => {
  const a = await fixture(request, 'preflight-switch-a'); const b = await fixture(request, 'preflight-switch-b')
  const first = await scheduled(request, a); const second = await scheduled(request, { ...b, student: a.student })
  await audio(page, 'settings'); await login(page, a.username)
  await page.goto(`/classroom/${first}/voice-preflight`)
  await page.getByRole('button', { name: '开始麦克风预检' }).click()
  await expect(page.getByText('正在本机预检', { exact: true })).toBeVisible()
  await page.evaluate(id => { history.pushState({}, '', `/classroom/${id}/voice-preflight`); window.dispatchEvent(new PopStateEvent('popstate')) }, second)
  await expect(page.getByRole('button', { name: '开始麦克风预检' })).toBeEnabled()
  await expect(page.getByText('未开始', { exact: true })).toBeVisible()
  expect(await page.evaluate(() => (window as any).__preflight.stops)).toBe(1)
  await page.getByRole('button', { name: '开始麦克风预检' }).click()
  await expect(page.getByText('正在本机预检', { exact: true })).toBeVisible()
  await page.evaluate(() => (window as any).__preflight.nodes.at(-1).onprocessorerror())
  await expect(page.getByText('音频处理失败，已停止', { exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: '开始麦克风预检' })).toBeEnabled()
  expect(await page.evaluate(() => (window as any).__preflight.stops)).toBe(2)
})

test('FD11 lost authorization before rejected audio initialization releases microphone immediately', async ({ page, request }) => {
  const f = await fixture(request, 'preflight-module-reject'); const id = await scheduled(request, f)
  await audio(page, 'module-late'); await pilot(page, true); await login(page, f.username)
  await page.goto(`/classroom/${id}/voice-preflight`)
  await page.getByRole('button', { name: '开始麦克风预检' }).click()
  await expect.poll(() => page.evaluate(() => !!(window as any).__preflight.rejectModule)).toBe(true)
  await page.evaluate(() => {
    // Token fact changes before React's later session teardown; rejection must release owned media itself.
    const token = localStorage.getItem('token'); (window as any).__preflight.restore = () => localStorage.setItem('token', token!)
    localStorage.removeItem('token'); (window as any).__preflight.rejectModule()
  })
  await expect.poll(() => page.evaluate(() => (window as any).__preflight.stops)).toBe(1)
  expect(await page.evaluate(() => (window as any).__preflight.closed)).toBe(1)
  await page.evaluate(() => (window as any).__preflight.restore())
})
