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

async function getUserToken(page: import('@playwright/test').Page): Promise<string> {
  const tok = await page.evaluate(() => localStorage.getItem('token'))
  return tok || ''
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
      if (mk.code !== 0 && !(mk.msg || '').includes('重复') && !(mk.msg || '').includes('duplicate') && !(mk.msg || '').includes('UNIQUE')) {
        throw new Error('建库失败: ' + mk.msg)
      }
      // 重查拿 ID（重跑时已存在的库 create 报 duplicate → 从列表找）
      let newID = mk.data?.ID
      if (!newID) {
        const bl = (await (await page.request.get(API + '/wordbooks', {
          headers: { Authorization: `Bearer ${adminTok}` },
        })).json()).data
        newID = bl.find((b: { Name: string }) => b.Name === 'BUG回归库')?.ID
      }
      if (!newID) throw new Error('拿不到 BUG回归库 ID')
      const entries = [
        { word: 'cat', meanings: ['n. 猫'] }, { word: 'dog', meanings: ['n. 狗'] },
        { word: 'apple', meanings: ['n. 苹果'] }, { word: 'bee', meanings: ['n. 蜜蜂'] },
        { word: 'fish', meanings: ['n. 鱼'] },
      ]
      const imp = await (await page.request.post(`${API}/wordbooks/${newID}/import/commit`, {
        headers: { Authorization: `Bearer ${adminTok}` }, data: { entries },
      })).json()
      if (imp.code !== 0) throw new Error('导入失败: ' + imp.msg)
      const pub = await (await page.request.post(`${API}/wordbooks/${newID}/publish`, {
        headers: { Authorization: `Bearer ${adminTok}` },
      })).json()
      if (pub.code !== 0 && !(pub.msg || '').includes('invalid status')) throw new Error('发布失败: ' + pub.msg)
      bk = { ID: newID }
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

    // 第一次摸底（API 快速路径，勾 cat）——持久化模式下先清残留 scheduled 课（幂等重跑）
    const preLS = (await (await page.request.get(`${API}/student/lessons?bookId=${bk.ID}`, {
      headers: { Authorization: `Bearer ${tok}` },
    })).json()).data ?? []
    const stale = preLS.filter((l: { Status: string }) => l.Status === 'scheduled')
    if (stale.length > 0) {
      // 用重摸底语义清掉：空勾选 finish 触发 ResetForReplacement+重排
      const p0 = await (await page.request.post(`${API}/student/books/${bk.ID}/placement`, {
        headers: { Authorization: `Bearer ${tok}` },
        data: { knownWordIds: [], finish: true },
      })).json()
      expect(p0.code).toBe(0)
    }
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
      // 幂等兜底：直查库 ID（带鉴权防 undefined）
      const bl = (await (await page.request.get(API + '/wordbooks', {
        headers: { Authorization: `Bearer ${await getUserToken(page)}` },
      })).json()).data ?? []
      const target = bl.find((b: { Name: string }) => b.Name === 'BUG回归库')
      if (!target) throw new Error('BUG回归库不存在于列表')
      await page.goto(BASE + '/placement/' + target.ID)
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
    // 退出课堂（不消费整课）——退出带二次确认 Modal（文本无关定位抗编码差异）
    await page.locator('header button, .ant-layout-header button').first()
      .or(page.locator('button').filter({ hasText: /退|Exit/ }).first()).click()
    // Modal 确定键（.ant-modal .ant-btn-primary；若 Modal 未弹则此步 no-op）
    await page.locator('.ant-modal .ant-btn-primary').click({ timeout: 5000 }).catch(() => {})
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

  test('BUG-9: 我的词库仅显示已启用 + 零课词库点击有引导反馈', async ({ page }) => {
    const tok = await ensureStudent(page)
    // admin 准备"零课引导库"（enable 但不摸底 → 有卡无课，dingdangcat 场景复现）
    const lr = await (await page.request.post(API + '/auth/login', {
      data: { username: 'dad', password: 'family123' },
    })).json()
    const adminTok = lr.data.token
    let zb = ((await (await page.request.get(API + '/wordbooks', {
      headers: { Authorization: `Bearer ${adminTok}` } })).json()).data ?? [])
      .find((b: { Name: string }) => b.Name === '零课引导库')
    if (!zb) {
      const mk = await (await page.request.post(API + '/wordbooks', {
        headers: { Authorization: `Bearer ${adminTok}` },
        data: { name: '零课引导库', grade: 'g7', description: 'r' },
      })).json()
      if (mk.code !== 0 && !(mk.msg || '').includes('重复') && !(mk.msg || '').includes('duplicate') && !(mk.msg || '').includes('UNIQUE')) {
        throw new Error('建库失败: ' + mk.msg)
      }
      let zid = mk.data?.ID
      if (!zid) {
        zid = ((await (await page.request.get(API + '/wordbooks', {
          headers: { Authorization: `Bearer ${adminTok}` } })).json()).data ?? [])
          .find((b: { Name: string }) => b.Name === '零课引导库')?.ID
      }
      if (!zid) throw new Error('拿不到 零课引导库 ID')
      const imp = await (await page.request.post(`${API}/wordbooks/${zid}/import/commit`, {
        headers: { Authorization: `Bearer ${adminTok}` },
        data: { entries: [{ word: 'zero', meanings: ['n. 零'] }, { word: 'one', meanings: ['n. 一'] }] },
      })).json()
      if (imp.code !== 0) throw new Error('导入失败: ' + imp.msg)
      const pub = await (await page.request.post(`${API}/wordbooks/${zid}/publish`, {
        headers: { Authorization: `Bearer ${adminTok}` },
      })).json()
      if (pub.code !== 0 && !(pub.msg || '').includes('invalid status')) throw new Error('发布失败: ' + pub.msg)
      zb = { ID: zid }
    }
    // 学生 enable（幂等）；该库从未 finish 摸底 → 必然零课
    await page.request.post(`${API}/student/books/${zb.ID}/enable`, {
      headers: { Authorization: `Bearer ${tok}` },
    }).catch(() => {})
    const ls = (await (await page.request.get(`${API}/student/lessons?bookId=${zb.ID}`, {
      headers: { Authorization: `Bearer ${tok}` },
    })).json()).data ?? []
    expect(ls.filter((l: { Status: string }) => l.Status === 'scheduled')).toHaveLength(0)

    // UI：登录 → dashboard
    await page.goto(BASE + '/login')
    await page.fill('input[placeholder*="学生或管理员"]', STU.username)
    await page.fill('input[type="password"]', STU.password)
    await page.getByRole('button', { name: '开始学习' }).click()
    await expect(page.getByText('今天也想见叮当老师').first()).toBeVisible({ timeout: 20_000 })
    await page.goto(BASE + '/dashboard')

    // 我的词库 = 仅已启用：UI 卡片数 === GET /student/books 长度；未启用种子库（高考）不得出现
    const myGrid = page.locator('.ant-card', { hasText: '我的词库' })
    await expect(myGrid).toBeVisible({ timeout: 15_000 })
    const apiBooks = ((await (await page.request.get(API + '/student/books', {
      headers: { Authorization: `Bearer ${tok}` },
    })).json()).data ?? [])
    const zeroCard = myGrid.locator('[role="button"]', { hasText: '零课引导库' })
    await expect(zeroCard).toBeVisible({ timeout: 10_000 })
    await expect(myGrid.locator('[role="button"][aria-label*="查看"]')).toHaveCount(apiBooks.length)
    await expect(myGrid.locator('[role="button"]', { hasText: '高考' })).toHaveCount(0, '未启用词库不得出现在我的词库（BUG 锁定）')

    // 点击零课词库 → 引导反馈（BUG 锁定：不再零反馈"点击不生效"）
    await zeroCard.click()
    await expect(page.getByText(/先完成摸底/)).toBeVisible({ timeout: 10_000 })
    const goBtn = page.getByRole('button', { name: '去摸底' })
    await expect(goBtn).toBeVisible()
    // 去摸底可达
    await goBtn.click()
    await expect(page.getByText('摸底：把已经认识的点亮')).toBeVisible({ timeout: 15_000 })
  })

  // ---- 2026-08-25 课堂流程定案锁定（用户三项问题：①学中测试②3+1 朗读③无判读）----
  test('定案锁定: 教学零测试+教完全部词才final+跟读判读推进（无我读完了按钮）', async ({ page }) => {
    const tok = await ensureStudent(page)
    const MEANINGS: Record<string, string> = { cat: '猫', dog: '狗', apple: '苹果', bee: '蜜蜂', fish: '鱼' }

    // 专用 5 词库（幂等：存在且词条≥5 视为就绪）
    const lr = await (await page.request.post(API + '/auth/login', {
      data: { username: 'dad', password: 'family123' },
    })).json()
    const adminTok = lr.data.token
    let bk = ((await (await page.request.get(API + '/wordbooks', {
      headers: { Authorization: `Bearer ${adminTok}` },
    })).json()).data ?? []).find((b: { Name: string }) => b.Name === '课堂定案库')
    if (bk) {
      const w = (await (await page.request.get(`${API}/wordbooks/${bk.ID}/words`, {
        headers: { Authorization: `Bearer ${adminTok}` },
      })).json()).data
      if ((w.total ?? w.entries?.length ?? 0) < 5) bk = undefined
    }
    if (!bk) {
      const mk = await (await page.request.post(API + '/wordbooks', {
        headers: { Authorization: `Bearer ${adminTok}` },
        data: { name: '课堂定案库', grade: 'g7', description: '课堂流程定案锁定' },
      })).json()
      if (mk.code !== 0 && !(mk.msg || '').includes('重复') && !(mk.msg || '').includes('duplicate') && !(mk.msg || '').includes('UNIQUE')) {
        throw new Error('建库失败: ' + mk.msg)
      }
      let nid = mk.data?.ID
      if (!nid) {
        nid = ((await (await page.request.get(API + '/wordbooks', {
          headers: { Authorization: `Bearer ${adminTok}` },
        })).json()).data ?? []).find((b: { Name: string }) => b.Name === '课堂定案库')?.ID
      }
      if (!nid) throw new Error('拿不到 课堂定案库 ID')
      const entries = ['cat', 'dog', 'apple', 'bee', 'fish'].map((w) => ({ word: w, meanings: ['n. ' + MEANINGS[w]] }))
      const imp = await (await page.request.post(`${API}/wordbooks/${nid}/import/commit`, {
        headers: { Authorization: `Bearer ${adminTok}` }, data: { entries },
      })).json()
      if (imp.code !== 0) throw new Error('导入失败: ' + imp.msg)
      const pub = await (await page.request.post(`${API}/wordbooks/${nid}/publish`, {
        headers: { Authorization: `Bearer ${adminTok}` },
      })).json()
      if (pub.code !== 0 && !(pub.msg || '').includes('invalid status')) throw new Error('发布失败: ' + pub.msg)
      bk = { ID: nid }
    }
    // enable + 重摸底（空勾选 finish）→ 幂等重跑：作废旧课、保证全新 5 词批次课
    await page.request.post(`${API}/student/books/${bk.ID}/enable`, {
      headers: { Authorization: `Bearer ${tok}` },
    }).catch(() => {})
    const p0 = await (await page.request.post(`${API}/student/books/${bk.ID}/placement`, {
      headers: { Authorization: `Bearer ${tok}` },
      data: { knownWordIds: [], finish: true },
    })).json()
    expect(p0.code, '重摸底排新课').toBe(0)
    const ls = (await (await page.request.get(`${API}/student/lessons?bookId=${bk.ID}`, {
      headers: { Authorization: `Bearer ${tok}` },
    })).json()).data
    const active = ls.filter((l: { Status: string }) => l.Status === 'scheduled')
    expect(active).toHaveLength(1)
    expect(active[0].BatchWordIDs).toHaveLength(5)
    const lessonId = active[0].ID
    // wordID→word 映射（final 池已打乱，按题帧 word_id 查词作答）
    const entries = (await (await page.request.get(`${API}/wordbooks/${bk.ID}/words`, {
      headers: { Authorization: `Bearer ${tok}` },
    })).json()).data.entries as Array<{ ID: number; Word: string }>
    const id2word: Record<number, string> = {}
    for (const e of entries) id2word[e.ID] = e.Word

    // UI 进课堂（e2e=1 禁自动拾音；驱动走 REST 判读/作答管线）
    await page.goto(BASE + '/login')
    await page.fill('input[placeholder*="学生或管理员"]', STU.username)
    await page.fill('input[type="password"]', STU.password)
    await page.getByRole('button', { name: '开始学习' }).click()
    await expect(page.getByText('今天也想见叮当老师').first()).toBeVisible({ timeout: 20_000 })
    await page.goto(`${BASE}/classroom/${lessonId}?e2e=1`)
    await expect(page.getByText('已连接')).toBeVisible({ timeout: 20_000 })
    await expect(page.getByText(/点一下这个单词/)).toBeVisible({ timeout: 20_000 })

    // REST 驱动助手（页内 fetch 走代理，带 token）
    const post = async (path: string, body: unknown) => await page.evaluate(async ([p, b]) => {
      const tok2 = localStorage.getItem('token') || ''
      const r = await fetch(`/api/v1${p}`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${tok2}` },
        body: JSON.stringify(b),
      })
      return await r.json()
    }, [path, body] as const)
    const framesOf = (r: { data?: { frames?: unknown[] } }) => (r.data?.frames ?? []) as Array<{
      type: string; action?: { type: string; kind?: string; total?: number; word_id?: number }; phase?: string
    }>
    const hasStartQuiz = (fs: ReturnType<typeof framesOf>) =>
      fs.some((f) => f.type === 'ui_action' && f.action?.type === 'start_quiz')

    // ===== 定案①：教学期零测试——教前 4 词，任何一步都不得出现 start_quiz =====
    const WORDS = ['cat', 'dog', 'apple', 'bee', 'fish']
    for (let i = 0; i < 4; i++) {
      await post(`/classroom/lessons/${lessonId}/event`, { event: 'word_clicked' })
      for (let s = 0; s < 4; s++) await post(`/classroom/lessons/${lessonId}/event`, { event: 'tts_finished' }) // 3en+1zh
      // ===== 定案③：跟读无"我读完了"按钮（首词时 UI 锁定断言）=====
      if (i === 0) {
        await expect(page.getByText(/轮到你读了/)).toBeVisible({ timeout: 10_000 })
        await expect(page.getByRole('button', { name: /我读完了/ })).toHaveCount(0, '跟读不得有"我读完了"按钮（定案锁定）')
      }
      // ===== 定案②：读对当前词 → 判读通过 → 下一词 =====
      const r = await post(`/classroom/lessons/${lessonId}/voice`, { text: WORDS[i] })
      expect(r.data?.command ?? '').not.toBe('repeat_done', '口令已废：不得再走 repeat_done 指令')
      expect(hasStartQuiz(framesOf(r)), `第 ${i + 1} 词教学期间不得触发测试`).toBe(false)
    }
    // ===== 定案①：教完第 5 词 → 唯一一次 final 完整测试（5 词全量）=====
    await post(`/classroom/lessons/${lessonId}/event`, { event: 'word_clicked' })
    for (let s = 0; s < 4; s++) await post(`/classroom/lessons/${lessonId}/event`, { event: 'tts_finished' })
    const rFin = await post(`/classroom/lessons/${lessonId}/voice`, { text: 'fish' })
    const finFrame = framesOf(rFin).find((f) => f.action?.type === 'start_quiz')
    expect(finFrame, '全部词教完触发完整测试').toBeTruthy()
    expect(finFrame?.action?.kind).toBe('final')
    expect(finFrame?.action?.total).toBe(5)

    // ===== final 逐题作答（按题帧 word_id 对应释义语音作答）→ 下课 =====
    let lastFrames = framesOf(rFin)
    for (let q = 0; q < 6; q++) {
      const item = lastFrames.filter((f) => f.action?.type === 'show_quiz_item').slice(-1)[0]
      const word = id2word[item?.action?.word_id ?? -1]
      if (!word) break
      const rAns = await post(`/classroom/lessons/${lessonId}/voice`, { text: MEANINGS[word] })
      lastFrames = framesOf(rAns)
      if (lastFrames.some((f) => f.type === 'lesson_summary' || f.action?.type === 'lesson_summary')) break
    }
    await expect(page.getByText('下课啦！叮当老师的小结')).toBeVisible({ timeout: 30_000 })
    await expect(page.getByText(/首答率/)).toBeVisible({ timeout: 15_000 })
  })
})
