import { test, expect } from '@playwright/test'
import { api, login } from './isolated-fixtures'

test('repairing a broken default profile preserves the administrator selected profile', async ({ request, page }) => {
  const status = await api(request, '/bootstrap/status', undefined, undefined, 'GET')
  if (status.needed) {
    await api(request, '/bootstrap', undefined, { username: 'isolated-admin', password: 'fixture12345', nickname: '测试管理员' })
  }
  const admin = await api(request, '/auth/login', undefined, { username: 'isolated-admin', password: 'fixture12345' })

  const alternate = await api(request, '/admin/model-profiles', admin.token, {
    name: 'selected-profile',
    text_service: 'text-selected',
    asr_service: 'asr-selected',
    tts_zh_service: 'tts-zh-selected',
    tts_en_service: 'tts-en-selected',
    active: false,
  })
  await api(request, `/admin/model-profiles/${alternate.id}/apply`, admin.token, {})

  // The isolated profile API has no Model Console configured, so this safely
  // simulates a legacy/corrupt persisted default without external services.
  await api(request, '/admin/model-profiles', admin.token, {
    name: 'default', text_service: '', asr_service: '', tts_zh_service: '', tts_en_service: '', active: false,
  })

  const profiles = await api(request, '/admin/model-profiles', admin.token, undefined, 'GET')
  expect(profiles.filter((profile: { active: boolean }) => profile.active)).toHaveLength(1)
  expect(profiles.find((profile: { name: string }) => profile.name === 'default')).toMatchObject({
    text_service: 'vl', asr_service: 'asr', tts_zh_service: 'tts', tts_en_service: 'tts-en', active: false,
  })
  expect(profiles.find((profile: { name: string }) => profile.name === 'selected-profile')?.active).toBe(true)

  await login(page, 'isolated-admin')
  const selectedRow = page.locator('.ant-list-item').filter({ hasText: 'selected-profile' })
  const defaultRow = page.locator('.ant-list-item').filter({ hasText: 'default' })
  await expect(selectedRow.locator('button.ant-btn-primary')).toHaveCount(1)
  await expect(defaultRow.locator('button.ant-btn-primary')).toHaveCount(0)
})
