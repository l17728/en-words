import { test, expect } from '@playwright/test'
import { fixture, login } from './isolated-fixtures'
test('login renders product and account controls', async ({ page, request }) => {
  await fixture(request, 'login-render')
  await page.goto('/login')
  await expect(page.getByText('叮当words')).toBeVisible()
  await expect(page.getByRole('button', { name: '开始学习', exact: true })).toBeVisible()
  await expect(page.getByLabel('用户名', { exact: true })).toBeVisible()
})
test('real login, book selection, placement and dashboard schedule', async ({ page, request }) => {
  const f = await fixture(request, 'smoke'); await login(page, f.username)
  await page.goto('/books')
  await page.locator('.ant-card', { hasText: f.book.Name }).getByRole('button', { name: '选择这个词库' }).click()
  await page.getByRole('button', { name: '开始摸底' }).click()
  await expect(page.getByRole('button', { name: 'cat', exact: true })).toBeVisible()
  await page.getByRole('button', { name: '完成摸底，生成学习计划' }).click()
  await expect(page.getByRole('dialog', { name: '摸底完成！' })).toBeVisible()
  await page.locator('.ant-modal-confirm .ant-btn-primary').click()
  await page.getByRole('button', { name: `查看 ${f.book.Name} 的课程` }).click()
  await expect(page.getByRole('button', { name: '进入课堂', exact: true })).toBeEnabled()
})
