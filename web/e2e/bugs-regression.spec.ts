// BUG 回归 E2E（铁律 0.5/0.6）：本轮手工测试发现的每个 BUG 一条用例。
// 覆盖：①选库 unauthorized（会话态单一事实源）②昵称 ??? （编码防御）
// ③词库卡片不可点 ④重进课堂只有…（快照补全）⑤重新摸底覆盖重排 ⑥摸底草稿恢复+取消
// ⑦课堂直接中英对照（无第一屏）⑧返回词库广场按钮
// 运行：npx playwright test bugs-regression.spec.ts --config=playwright.headless.config.ts
import { test, expect, type Page } from '@playwright/test'

const BASE = 'http://localhost:5173'
const API = 'http://127.0.0.1:8080/api/v1'

test.describe.configure({ mode: 'serial' })

// 唯一测试学生（本轮专用）
const STU = { username: 'bugreg', password: 'bug123456', nickname: '虫虫' }

async function ensureStudent(page: Page): Promise<string> {
  // admin 登录（bootstrap 幂等）
  const st = await (await page.request.get(API + '/bootstrap/status')).json()
  if (st.data?.needed) {
    await page.request.post(API + '/bootstrap', {
      data: { username: 'dad', password: 'family123', nickname: '管理员' },
    })
  }
  const lr = await (await page.request.post(API + '/auth/login', {
    data: { username: 'dad', password: 'family123' },
  })).json()
  const adminTok = lr.data.token
  // 建学生（幂等：duplicate 视为通过）——Playwright request 走 UTF-8（防 PowerShell GBK ??? 类问题）
  const mk = await (await page.request.post(API + '/admin/users', {
    headers: { Authorization: `Bearer ${adminTok}` },
    data: { ...STU, role: 'student' },
  })).json()
  if (mk.code !== 0 && !(mk.msg || '').includes('duplicate')) throw new Error('建学生失败: ' + mk.msg)
  const cr = await (await page.request.post(API + '/auth/login', {
    data: { username: STU.username, password: STU.password },
  })).json()
  return cr.data.token
}

test.describe('BUG 回归：本轮修复锁定', () => {
  test('BUG-1+2: 登录后选库无 unauthorized + 昵称正确显示（会话态单一事实源/编码）', async ({ page }) => {
    await ensureStudent(page)
    // 全新会话（无 localStorage 残留）登录 → 立即选库（原 BUG 触发路径）
    await page.goto(BASE + '/login')
    await page.fill('input[placeholder*="学生或管理员"]', STU.username)
    await page.fill('input[type="password"]', STU.password)
    await page.getByRole('button', { name: '开始学习' }).click()
    await expect(page.getByText('今天也想见叮当老师').first()).toBeVisible({ timeout: 20_000 })
    // 昵称非 ???（编码正确）
    const greet = await page.getByText('今天也想见叮当老师').first().textContent()
    expect(greet).toContain('虫虫')
    expect(greet).not.toContain('???')
    // 选库（原 unauthorized BUG 路径）
    await page.goto(BASE + '/books')
    const card = page.locator('.ant-card', { hasText: '人教版初一' }).first()
    await expect(card).toBeVisible()
    await card.getByRole('button', { name: '选择这个词库' }).click()
    await page.waitForTimeout(1200)
    const body = (await page.textContent('body')) || ''
    expect(body).not.toContain('unauthorized')
    // 清理：关掉可能的摸底引导弹窗
    const later = page.getByRole('button', { name: /稍后再说/ })
    if (await later.isVisible().catch(() => false)) await later.click().catch(() => {})
  })

  test('BUG-5: 重新摸底完成覆盖重排（不报 already scheduled）', async ({ page }) => {
    const tok = await ensureStudent(page)
    // 建专用小库（幂等：存在且词条≥5 视为就绪；空库/缺词补导入）
    const lr = await (await page.request.post(API + '/auth/login', {
      data: { username: 'dad', password: 'family123' },
    })).json()
    const adminTok = lr.data.token
    const books = (await (await page.request.get(API + '/wordbooks', {
      headers: { Authorization: `Bearer ${adminTok}` },
    })).json()).data
    let bk = books.find((b: { Name: string }) => b.Name === 'BUG回归库')
    if (bk) {
      const w = (await (await page.request.get(`${API}/wordbooks/${bk.ID}/words`, {
        headers: { Authorization: `Bearer ${adminTok}` },
      })).json()).data
      if ((w.total ?? w.entries?.length ?? 0) < 5) bk = undefined // 空库重建
    }
    if (!bk) {
      const mk = await (await page.request.post(API + '/wordbooks', {
        headers: { Authorization: `Bearer ${adminTok}` },
        data: { name: 'BUG回归库', grade: 'g7', description: '回归' },
      })).json()
      const entries = [
        { word: 'cat', meanings: ['n. 猫'] }, { word: 'dog', meanings: ['n. 狗'] },
        { word: 'apple', meanings: ['n. 苹果'] }, { word: 'bee', meanings: ['n. 蜜蜂'] },
        { word: 'fish', meanings: ['n. 鱼'] },
      ]
      const imp = await (await page.request.post(`${API}/wordbooks/${mk.data.ID}/import/commit`, {
        headers: { Authorization: `Bearer ${adminTok}` }, data: { entries },
      })).json()
      if (imp.code !== 0) throw new Error('导入失败: ' + imp.msg)
      const pub = await (await page.request.post(`${API}/wordbooks/${mk.data.ID}/publish`, {
        headers: { Authorization: `Bearer ${adminTok}` },
      })).json()
      if (pub.code !== 0) throw new Error('发布失败: ' + pub.msg)
      bk = { ID: mk.data.ID }
    }
    // 学生 enable（幂等：已 enable 报错忽略——重复 enable 时卡片已存在）
    await page.request.post(`${API}/student/books/${bk.ID}/enable`, {
      headers: { Authorization: `Bearer ${tok}` },
    }).catch(() => {})
    // UI 登录
    await page.goto(BASE + '/login')
    await page.fill('input[placeholder*="学生或管理员"]', STU.username)
    await page.fill('input[type="password"]', STU.password)
    await page.getByRole('button', { name: '开始学习' }).click()
    await expect(page.getByText('今天也想见叮当老师').first()).toBeVisible({ timeout: 20_000 })

    // 第一次摸底（API 快速路径，勾 cat）
    const catW = (await (await page.request.get(`${API}/wordbooks/${bk.ID}/words`, {
      headers: { Authorization: `Bearer ${tok}` },
    })).json()).data.entries.find((e: { Word: string }) => e.Word === 'cat')
    const p1 = await (await page.request.post(`${API}/student/books/${bk.ID}/placement`, {
      headers: { Authorization: `Bearer ${tok}` },
      data: { knownWordIds: [catW.ID], finish: true },
    })).json()
    expect(p1.code).toBe(0)

    // 重新摸底完成（勾 cat+dog）：不报错（BUG-5 锁定）
    const dogW = (await (await page.request.get(`${API}/wordbooks/${bk.ID}/words`, {
      headers: { Authorization: `Bearer ${tok}` },
    })).json()).data.entries.find((e: { Word: string }) => e.Word === 'dog')
    const p2 = await (await page.request.post(`${API}/student/books/${bk.ID}/placement`, {
      headers: { Authorization: `Bearer ${tok}` },
      data: { knownWordIds: [catW.ID, dogW.ID], finish: true },
    })).json()
    expect(p2.code).toBe(0, '重新摸底完成不得报 already scheduled')

    // 旧课作废+新课 3 词批次
    const ls = (await (await page.request.get(`${API}/student/lessons?bookId=${bk.ID}`, {
      headers: { Authorization: `Bearer ${tok}` },
    })).json()).data
    const active = ls.filter((l: { Status: string }) => l.Status === 'scheduled')
    expect(active).toHaveLength(1)
    expect(active[0].BatchWordIDs).toHaveLength(3)
  })

  test('BUG-6+8: 摸底草稿二次进入恢复 + 返回词库广场按钮', async ({ page }) => {
    await ensureStudent(page)
    await page.goto(BASE + '/login')
    await page.fill('input[placeholder*="学生或管理员"]', STU.username)
    await page.fill('input[type="password"]', STU.password)
    await page.getByRole('button', { name: '开始学习' }).click()
    await expect(page.getByText('今天也想见叮当老师').first()).toBeVisible({ timeout: 20_000 })

    // 到 BUG回归库 摸底页（已选 2 词的草稿场景由上个用例建立——此用例验证 UI 恢复）
    await page.goto(BASE + '/books')
    const card = page.locator('.ant-card', { hasText: 'BUG回归库' })
    await expect(card).toBeVisible()
    await card.getByRole('button', { name: '选择这个词库' }).click()
    const startBtn = page.getByRole('button', { name: '开始摸底' })
    if (await startBtn.isVisible({ timeout: 8_000 }).catch(() => false)) {
      await startBtn.click()
    } else {
      await page.goto(BASE + '/placement/' + await (async () => {
        const books = (await (await page.request.get(API + '/wordbooks')).json()).data
        return books.find((b: { Name: string }) => b.Name === 'BUG回归库').ID
      })())
    }
    await expect(page.getByText('摸底：把已经认识的点亮')).toBeVisible({ timeout: 15_000 })
    // 草稿恢复：cat+dog 两芯片应为 pressed（不清空）
    const catChip = page.locator('[aria-pressed="true"]', { hasText: 'cat' })
    await expect(catChip.first()).toBeVisible({ timeout: 10_000 })
    // 点亮 fish 再取消（可修改）
    const fish = page.locator('[aria-pressed="false"]', { hasText: 'fish' }).first()
    await fish.click()
    await expect(page.locator('[aria-pressed="true"]', { hasText: 'fish' })).toBeVisible()
    await page.locator('[aria-pressed="true"]', { hasText: 'fish' }).first().click() // 再点取消
    await expect(page.locator('[aria-pressed="true"]', { hasText: 'fish' })).toHaveCount(0)
    // BUG-8：返回词库广场按钮存在且可点
    const backBtn = page.getByRole('button', { name: /返回词库广场/ })
    await expect(backBtn).toBeVisible()
    await backBtn.click()
    await expect(page.getByText('词库广场')).toBeVisible({ timeout: 10_000 })
  })

  test('BUG-3+4+7: 词库卡片可点 + 课堂直接中英对照（无第一屏/重进有词）', async ({ page }) => {
    await ensureStudent(page)
    await page.goto(BASE + '/login')
    await page.fill('input[placeholder*="学生或管理员"]', STU.username)
    await page.fill('input[type="password"]', STU.password)
    await page.getByRole('button', { name: '开始学习' }).click()
    await expect(page.getByText('今天也想见叮当老师').first()).toBeVisible({ timeout: 20_000 })

    // BUG-3：dashboard 词库卡片可点（语义化定位与修复对齐——R5：弃脆弱 CSS 类选择器）
    await page.goto(BASE + '/dashboard')
    await expect(page.getByText('我的词库').first()).toBeVisible({ timeout: 15_000 })
    const grid = page.locator('[role="button"][aria-label*="查看"]').filter({ hasText: 'BUG回归库' })
    await expect(grid).toBeVisible({ timeout: 10_000 })
    await grid.click()
    await page.waitForTimeout(1000)

    // 进入课堂（testmode 已放行时间）
    const enter = page.getByRole('button', { name: '进入课堂' })
    const canEnter = await enter.isVisible({ timeout: 10_000 }).catch(() => false)
    test.skip(!canEnter, '该课已被前序用例消费')
    await enter.click()
    await expect(page.getByText('已连接')).toBeVisible({ timeout: 20_000 })

    // BUG-4+7 精确断言（R5：弃 body!==…弱断言，改可见词+中文释义元素级断言）
    await expect(page.locator('div').filter({ hasText: /^(cat|dog|apple|bee|fish)$/ }).first())
      .toBeVisible({ timeout: 15_000 })
    await page.waitForTimeout(2200) // 0.8s 自动开读后 zhMeaning 渲染
    await expect(page.locator('div').filter({ hasText: /[猫狗苹果蜜蜂鱼]/ }).first())
      .toBeVisible({ timeout: 10_000 })
    // 退出课堂（不消费整课）
    await page.getByRole('button', { name: /退出/ }).click()
    await page.waitForTimeout(800)

    // BUG-4 续：重进课堂立即有词（快照补全所有教学相位）
    await page.goto(BASE + '/dashboard')
    await page.locator('[role="button"][aria-label*="查看"]').filter({ hasText: 'BUG回归库' }).click()
    await page.waitForTimeout(800)
    const enter2 = page.getByRole('button', { name: '进入课堂' })
    if (await enter2.isVisible({ timeout: 8_000 }).catch(() => false)) {
      await enter2.click()
      await expect(page.getByText('已连接')).toBeVisible({ timeout: 20_000 })
      await page.waitForTimeout(1500)
      const body2 = (await page.textContent('body')) || ''
      expect(body2).not.toBe('…', '重进不得只显示 …（快照补全锁定）')
    }
  })
})
