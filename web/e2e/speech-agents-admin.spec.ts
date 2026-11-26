import { test, expect } from '@playwright/test'

const agent = {
  id: 'agent-01', display_name: 'Classroom PC', enabled: true,
  capabilities: [{ service_id: 'tts-en', model: 'piper-en', language: 'en', voice: 'default' }],
  created_at: '2026-11-23T00:00:00Z', last_seen_at: '2026-11-23T01:00:00Z',
}
let enrollmentPayload: unknown

test.beforeEach(async ({ page }) => {
  enrollmentPayload = undefined
  await page.addInitScript(() => {
    localStorage.setItem('token', 'admin-test-token')
    localStorage.setItem('role', 'admin')
    localStorage.setItem('nickname', '测试管理员')
    localStorage.setItem('userId', '1')
  })
  await page.route('**/api/v1/**', async route => {
    const request = route.request()
    const path = new URL(request.url()).pathname.replace('/api/v1', '')
    let data: unknown = []
    if (path === '/admin/model-services') data = [{ id: 'tts-en', model: 'piper-en', capabilities: ['tts'] }]
    if (path === '/admin/speech-agents') data = [agent]
    if (path === '/admin/speech-agents/enrollments' && request.method() === 'POST') {
      enrollmentPayload = request.postDataJSON()
      data = { enrollment_token: 'one-time-enrollment-token', expires_in_seconds: 300 }
    }
    if (path === '/admin/speech-agents/agent-01/rotate' && request.method() === 'POST') data = { agent_token: 'one-time-agent-token' }
    if (path === '/admin/speech-agents/agent-01/revoke' && request.method() === 'POST') data = { revoked: true }
    await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ code: 0, msg: 'ok', data }) })
  })
  await page.goto('/admin')
})

test('administrator issues a bounded enrollment token for selected TTS capabilities', async ({ page }) => {
  await expect(page.getByText('本机 TTS 驻留设备', { exact: true })).toBeVisible()
  await page.getByLabel('TTS 服务 ID').fill('tts-en')
  await page.getByLabel('TTS 模型 ID').fill('piper-en')
  await page.getByRole('button', { name: '签发注册令牌' }).click()
  expect(enrollmentPayload).toEqual({ ttl_seconds: 300, capabilities: [
    { service_id: 'tts-en', model: 'piper-en', language: 'en', voice: 'default' },
    { service_id: 'tts-en', model: 'piper-en', language: 'zh', voice: 'default' },
  ] })
  await expect(page.getByLabel('一次性令牌')).toHaveValue('one-time-enrollment-token')
  await expect(page.getByText('注册令牌 300 秒后过期。')).toBeVisible()
})

test('administrator can rotate and revoke a registered resident agent', async ({ page }) => {
  await expect(page.getByText('Classroom PC')).toBeVisible()
  await page.getByRole('button', { name: '轮换令牌' }).click()
  await expect(page.getByLabel('一次性令牌')).toHaveValue('one-time-agent-token')
  await page.getByRole('button', { name: '知道了' }).click()
  await page.getByRole('button', { name: /撤\s*销/ }).click()
  await page.getByRole('button', { name: '撤销设备' }).click()
  await expect(page.getByText('设备已撤销，在途任务已失效')).toBeVisible()
})
