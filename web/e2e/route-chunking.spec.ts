import { expect, test } from '@playwright/test'

test.beforeEach(async ({ context }) => {
  await context.route('**/api/v1/bootstrap/status', (route) => route.fulfill({ json: { code: 0, msg: 'ok', data: { needed: false } } }))
  await context.route('**/api/v1/**', async (route) => {
    const url = new URL(route.request().url())
    const data = url.pathname.endsWith('/student/stats')
      ? { learned: 0, graduated: 0, remaining: 0, placement_known: 0, finished_lessons: 0, streak_days: 0, timezone: 'Asia/Shanghai', recent_lessons: [] }
      : []
    await route.fulfill({ json: { code: 0, msg: 'ok', data } })
  })
})

test('recovers after one transient route chunk failure and preserves the session', async ({ page }) => {
  let chunkRequests = 0
  let documentRequests = 0
  page.on('request', (request) => { if (request.isNavigationRequest() && request.frame() === page.mainFrame()) documentRequests++ })
  await page.addInitScript(() => { localStorage.setItem('token', 'session-preserved') })
  await page.route('**/assets/Login-*.js', async (route) => {
    chunkRequests++
    if (chunkRequests === 1) await route.abort('failed')
    else await route.continue()
  })

  await page.goto('/login')
  await expect(page.getByRole('alert').getByRole('heading', { name: '页面暂时无法加载' })).toBeVisible()
  await page.getByRole('button', { name: '重试' }).click()
  await expect(page.getByText('叮当words')).toBeVisible()
  expect(documentRequests).toBe(2)
  expect(await page.evaluate(() => localStorage.getItem('token'))).toBe('session-preserved')
  expect(chunkRequests).toBe(2)

  await page.route('**/assets/Dashboard-*.js', (route) => route.abort('failed'))
  await page.goto('/dashboard')
  await expect(page.getByRole('alert').getByText('更新后仍无法加载此页面，请稍后重试。')).toBeVisible()
  await expect(page.getByRole('button', { name: '重试' })).toHaveCount(0)
  expect(documentRequests).toBe(3)
})

test('does not loop reloads when the route chunk still fails after the bounded retry', async ({ page }) => {
  let documentRequests = 0
  let chunkRequests = 0
  page.on('request', (request) => { if (request.isNavigationRequest() && request.frame() === page.mainFrame()) documentRequests++ })
  await page.route('**/assets/Login-*.js', async (route) => { chunkRequests++; await route.abort('failed') })

  await page.goto('/login')
  await expect(page.getByRole('alert').getByRole('heading', { name: '页面暂时无法加载' })).toBeVisible()
  await page.getByRole('button', { name: '重试' }).click()
  await expect(page.getByRole('alert').getByText('更新后仍无法加载此页面，请稍后重试。')).toBeVisible()
  await expect(page.getByRole('button', { name: '重试' })).toHaveCount(0)
  await page.waitForTimeout(300)
  expect(documentRequests).toBe(2)
  expect(chunkRequests).toBe(2)
})

test('keeps one retry available when browser session storage is blocked', async ({ page }) => {
  let documentRequests = 0
  let chunkRequests = 0
  page.on('request', (request) => { if (request.isNavigationRequest() && request.frame() === page.mainFrame()) documentRequests++ })
  await page.addInitScript(() => Object.defineProperty(window, 'sessionStorage', {
    configurable: true,
    get() { throw new DOMException('Storage access is denied', 'SecurityError') },
  }))
  await page.route('**/assets/Login-*.js', async (route) => { chunkRequests++; await route.abort('failed') })

  await page.goto('/login')
  await expect(page.getByRole('alert').getByRole('heading', { name: '页面暂时无法加载' })).toBeVisible()
  await page.getByRole('button', { name: '重试' }).click()
  await expect(page.getByRole('alert').getByText('更新后仍无法加载此页面，请稍后重试。')).toBeVisible()
  expect(documentRequests).toBe(2)
  expect(chunkRequests).toBe(2)
})

test('revalidates HTML while caching hashed JavaScript chunks immutably', async ({ page }) => {
  const entryResponse = page.waitForResponse((response) => /\/assets\/index-[\w-]+\.js/.test(response.url()))
  const documentResponse = page.goto('/login')
  const [entry, document] = await Promise.all([entryResponse, documentResponse])
  expect(document?.headers()['cache-control']).toContain('no-cache')
  expect(entry.headers()['cache-control']).toContain('immutable')
})

test('keeps accessible navigation controls visible while a route chunk is pending', async ({ page }) => {
  let unblock: (() => void) | undefined
  let signalStarted: (() => void) | undefined
  const blocked = new Promise<void>((resolve) => { unblock = resolve })
  const started = new Promise<void>((resolve) => { signalStarted = resolve })
  await page.route('**/assets/Login-*.js', async (route) => {
    signalStarted?.()
    await blocked
    await route.continue()
  })

  const navigation = page.goto('/login')
  await started
  await expect(page.getByRole('status')).toHaveText('正在加载页面…')
  await expect(page.getByRole('button', { name: '返回' })).toBeVisible()
  await expect(page.getByRole('link', { name: '返回学习首页' })).toBeVisible()
  unblock?.()
  await navigation
  await expect(page.getByText('叮当words')).toBeVisible()
})

test('does not mount a delayed classroom chunk after account replacement and navigation away', async ({ page }) => {
  await page.addInitScript(() => {
    localStorage.setItem('token', 'old-classroom-token')
    localStorage.setItem('role', 'student')
    localStorage.setItem('nickname', 'Old Student')
    localStorage.setItem('userId', '41')
  })
  let unblock: (() => void) | undefined
  let signalStarted: (() => void) | undefined
  const blocked = new Promise<void>((resolve) => { unblock = resolve })
  const started = new Promise<void>((resolve) => { signalStarted = resolve })
  await page.route('**/assets/Classroom-*.js', async (route) => {
    signalStarted?.()
    await blocked
    await route.continue()
  })
  const classroomRequests: string[] = []
  let websocketCount = 0
  page.on('request', (request) => {
    if (new URL(request.url()).pathname.startsWith('/api/v1/classroom/')) classroomRequests.push(request.url())
  })
  page.on('websocket', () => { websocketCount++ })

  await page.goto('/classroom/11')
  await started
  await expect(page.getByRole('status')).toHaveText('正在加载页面…')
  const replacement = await page.context().newPage()
  try {
    await replacement.goto('/login')
    await replacement.evaluate(() => localStorage.setItem('token', 'new-classroom-token'))
    await page.getByRole('link', { name: '返回学习首页' }).click()
    await expect(page).toHaveURL(/\/dashboard$/)
    await expect(page.locator('body')).toContainText('今天也想见叮当老师吗')
  } finally {
    unblock?.()
    await replacement.close()
  }
  await page.waitForTimeout(250)
  expect(classroomRequests).toEqual([])
  expect(websocketCount).toBe(0)
  await expect(page.locator('body')).toContainText('今天也想见叮当老师吗')
})

test('redirects guests to login and non-admin users away from admin routes', async ({ page }) => {
  await page.goto('/dashboard')
  await expect(page).toHaveURL(/\/login$/)
  await expect(page.locator('body')).toContainText('叮当words')

  await page.evaluate(() => {
    localStorage.setItem('token', 'student-token')
    localStorage.setItem('role', 'student')
    localStorage.setItem('nickname', 'Route Student')
    localStorage.setItem('userId', '18')
  })
  await page.goto('/admin')
  await expect(page).toHaveURL(/\/dashboard$/)
  await expect(page.locator('body')).toContainText('今天也想见叮当老师吗')
})

test('browser back restores the previous lazy route without losing the session', async ({ page }) => {
  await page.addInitScript(() => {
    localStorage.setItem('token', 'history-token')
    localStorage.setItem('role', 'student')
    localStorage.setItem('nickname', 'History Student')
    localStorage.setItem('userId', '19')
  })
  await page.goto('/dashboard')
  await page.getByRole('button', { name: '词库广场' }).click()
  await expect(page).toHaveURL(/\/books$/)
  await expect(page.locator('body')).toContainText('词库广场')
  await page.goBack()
  await expect(page).toHaveURL(/\/dashboard$/)
  await expect(page.locator('body')).toContainText('今天也想见叮当老师吗')
  expect(await page.evaluate(() => localStorage.getItem('token'))).toBe('history-token')
})

test('account replacement closes a page-owned modal before the new route tree mounts', async ({ page }) => {
  await page.addInitScript(() => {
    localStorage.setItem('token', 'admin-old-token')
    localStorage.setItem('role', 'admin')
    localStorage.setItem('nickname', 'Admin')
    localStorage.setItem('userId', '20')
  })
  await page.route('**/api/v1/wordbooks', (route) => route.fulfill({ json: {
    code: 0, msg: 'ok', data: [{ ID: 7, Name: '演示词库', Grade: 'g7', WordCount: 1, Description: 'E2E' }],
  } }))
  await page.goto('/books')
  await page.getByRole('button', { name: '删除', exact: true }).click()
  await expect(page.getByRole('dialog').locator('.ant-modal-confirm-title')).toHaveText('确认删除词库')

  const replacement = await page.context().newPage()
  try {
    await replacement.goto('/login')
    await replacement.evaluate(() => localStorage.setItem('token', 'admin-new-token'))
    await expect(page.getByRole('dialog')).toHaveCount(0)
    expect(await page.evaluate(() => localStorage.getItem('token'))).toBe('admin-new-token')
  } finally {
    await replacement.close()
  }
})

const directRoutes = [
  ['/login', 'Login', '叮当words'], ['/books', 'Books', '词库广场'], ['/dashboard', 'Dashboard', '今天也想见叮当老师吗'],
  ['/placement/7', 'Placement', '摸底：把已经认识的点亮'], ['/classroom/11', 'Classroom', '课堂'],
  ['/classroom/11/voice-preflight', 'VoicePreflight', '麦克风'], ['/stats', 'Stats', '学习统计'],
  ['/admin', 'Admin', '叮当words 管理'], ['/settings', 'Settings', '学习设置'],
] as const

for (const [routePath, chunkName, expectedContent] of directRoutes) {
  test(`direct route ${routePath} loads its own ${chunkName} chunk`, async ({ page }) => {
    await page.addInitScript(() => {
      localStorage.setItem('token', 'route-test-token')
      localStorage.setItem('role', 'admin')
      localStorage.setItem('nickname', 'Route Test')
      localStorage.setItem('userId', '17')
    })
    const chunkResponse = page.waitForResponse((response) => new RegExp(`/assets/${chunkName}-[\\w-]+\\.js`).test(new URL(response.url()).pathname))
    await page.goto(routePath)
    expect((await chunkResponse).status()).toBe(200)
    expect(new URL(page.url()).pathname).toBe(routePath)
    await expect(page.getByRole('heading', { name: '页面暂时无法加载' })).toHaveCount(0)
    await expect(page.locator('body')).toContainText(expectedContent)
  })
}
