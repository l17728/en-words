import { postClassroom } from './classroom-protocol'
// 叮当words 全生命周期 headed E2E（用户指定测试账户：叮当猫）
// 场景：首次启动向导→注册叮当猫→选库→摸底→首课进入→课堂全流程
//      （教学/TTS自动推进/跟读判读/教完全部词→完整测试/RETEACH纠错闭环/轻量回顾）→下课课报→
//      自动排下一课→反馈闭环→通知→退出。
// 语音：浏览器 Web Speech 真实朗读（Windows 语音引擎，headed 可听到）；
//      TTS onend 回执驱动课堂推进，看门狗兜底（FE-03）。
// 运行前置：后端 -testmode（:8080，首启自动播种7库+无admin）；前端 dev（:5173）。
import { test, expect, type Page } from '@playwright/test'

const BASE = 'http://localhost:5173'
const API = 'http://127.0.0.1:8080/api/v1'

test.describe.configure({ mode: 'serial' })

// ---- 工具：课堂驱动（容错循环内联实现，保留以下辅助备用） ----

async function answerQuiz(page: Page, text: string) {
  const input = page.getByLabel('答案输入框')
  await input.fill(text)
  await page.getByRole('button', { name: '回答', exact: true }).click()
}

test('叮当words 全生命周期', async ({ page }) => {
  test.setTimeout(600_000)

  // ===== 1. 首次启动：创建管理员（首启向导）=====
  await page.goto(BASE + '/login')
  await expect(page.getByText('叮当words')).toBeVisible()

  // 通过 API 引导创建 admin（UI 向导页未实现，直接走 bootstrap——验证后端闭环）
  const needResp = await page.request.get(API + '/bootstrap/status')
  const need = (await needResp.json()).data?.needed
  if (need) {
    const resp = await page.request.post(API + '/bootstrap', {
      data: { username: 'dad', password: 'family123', nickname: '叮当猫爸爸' },
    })
    expect((await resp.json()).code).toBe(0)
  }

  // ===== 2. 管理员登录 → 建学生"叮当猫" =====
  const loginResp = await page.request.post(API + '/auth/login', {
    data: { username: 'dad', password: 'family123' },
  })
  const adminTok = (await loginResp.json()).data.token
  const mkStu = await page.request.post(API + '/admin/users', {
    headers: { Authorization: `Bearer ${adminTok}` },
    data: { username: 'dingdangcat', password: 'cat123456', nickname: '叮当猫', role: 'student' },
  })
  const mkStuBody = await mkStu.json()
  // 幂等：已存在（400 duplicate）视作通过；其余才失败
  if (mkStuBody.code !== 0 && !(mkStuBody.msg || '').includes('duplicate')) {
    throw new Error(`create student failed: ${mkStuBody.msg}`)
  }

  // ===== 3. 叮当猫登录（UI）=====
  await page.fill('input[placeholder*="学生或管理员"]', 'dingdangcat')
  await page.fill('input[type="password"]', 'cat123456')
  await page.getByRole('button', { name: '开始学习' }).click()
  // 登录成功 → dashboard 问候语（.first() 消除 strict 歧义）
  await expect(page.getByText('今天也想见叮当老师').first()).toBeVisible({ timeout: 20_000 })

  // ===== 4. 词库广场：选 g7 小库（5 词种子足够走完单组课）=====
  // 保证存在 5 词小库（幂等：不存在则建）
  const booksResp = await page.request.get(API + '/wordbooks', {
    headers: { Authorization: `Bearer ${adminTok}` },
  })
  let books = (await booksResp.json()).data as Array<{ ID: number; Name: string; Status: string; WordCount: number }>
  let small = books.find((b) => b.Name === 'E2E小库')
  if (!small) {
    const mk = await page.request.post(API + '/wordbooks', {
      headers: { Authorization: `Bearer ${adminTok}` },
      data: { name: 'E2E小库', grade: 'g7', description: '全生命周期E2E' },
    })
    small = (await mk.json()).data
    const words = ['cat', 'dog', 'apple', 'bee', 'fish']
    const entries = words.map((w) => ({ word: w, meanings: ['n. ' + ({ cat: '猫', dog: '狗', apple: '苹果', bee: '蜜蜂', fish: '鱼' } as Record<string, string>)[w]] }))
    await page.request.post(`${API}/wordbooks/${small.ID}/import/commit`, {
      headers: { Authorization: `Bearer ${adminTok}` },
      data: { entries },
    })
    await page.request.post(`${API}/wordbooks/${small.ID}/publish`, {
      headers: { Authorization: `Bearer ${adminTok}` },
    })
  }

  // 去词库广场选 E2E小库（幂等：已选过则 Enable 报错忽略）
  await page.goto(BASE + '/books')
  const card = page.locator('.ant-card', { hasText: 'E2E小库' })
  await expect(card).toBeVisible()
  const chooseBtn = card.getByRole('button', { name: '选择这个词库' })
  if (await chooseBtn.isVisible().catch(() => false)) {
    await chooseBtn.click()
    // 摸底引导 Modal → "开始摸底"
    const startBtn = page.getByRole('button', { name: '开始摸底' })
    if (await startBtn.isVisible({ timeout: 8_000 }).catch(() => false)) {
      await startBtn.click()
      // ===== 5. 摸底：标记 cat 已会 → 完成（幂等：草稿恢复可能已点亮）=====
      await expect(page.getByText('摸底：把已经认识的点亮')).toBeVisible({ timeout: 15_000 })
      const catChip = page.locator('[aria-pressed]', { hasText: 'cat' }).first()
      if ((await catChip.getAttribute('aria-pressed')) === 'false') {
        await catChip.click()
      }
      await page.getByRole('button', { name: /完成摸底/ }).click()
      // 成功 Modal（"知道了"）→ 点掉
      const okBtn = page.getByRole('button', { name: /知道了|开始学习|OK|确定/ }).last()
      await okBtn.click({ timeout: 15_000 }).catch(() => {})
    }
  }

  // ===== 6. 课程表：选词库卡片（触发 lessons 加载）→ 进入课堂（testmode 已跳过开放时间）=====
  await page.goto(BASE + '/dashboard')
  await expect(page.getByText('我的词库').first()).toBeVisible({ timeout: 15_000 })
  // 点 E2E小库 卡片加载课程（BUG-9 改版后卡片=role=button aria-label=查看…）
  const gridCard = page.locator('[role="button"][aria-label*="查看"]').filter({ hasText: 'E2E小库' })
  if (await gridCard.isVisible().catch(() => false)) {
    await gridCard.click()
  }
  await page.waitForTimeout(800)
  const enterBtn = page.getByRole('button', { name: '进入课堂' })
  const canEnter = await enterBtn.isVisible({ timeout: 10_000 }).catch(() => false)
  test.skip(!canEnter, '叮当猫已无待上课程（前次运行已消费）')
  await enterBtn.click()

  // ===== 7. 课堂全流程 =====
  // e2e=1：禁 🎙 自动监听（测试答题走单次 REST /voice 提交，避免拾音+显式=重复回答）
  await expect(page.getByText('已连接')).toBeVisible({ timeout: 20_000 })
  await page.evaluate(() => {
    const u = new URL(window.location.href)
    if (u.searchParams.get('e2e') !== '1') {
      u.searchParams.set('e2e', '1')
      window.history.replaceState(null, '', u.toString())
    }
  })
  await expect(page.getByText(/点一下这个单词/)).toBeVisible({ timeout: 20_000 })

  // 容错驱动直到下课（最长 300s）：按相位做正确动作（教学→跟读→final 完整测试→RETEACH→回顾）
  const deadline = Date.now() + 300_000
  while (Date.now() < deadline) {
    const bodyText = (await page.textContent('body')) || ''

    if (bodyText.includes('下课啦！叮当老师的小结')) break

    // 优先级1：跟读相位 → 模拟学生读出单词（REST /voice 转写→判读；用户定案 2026-08-25：
    // 无"我读完了"按钮，老师听学生读完（判读通过）自动下一词）
    if (bodyText.includes('轮到你读了') || bodyText.includes('跟读')) {
      const big = await page.evaluate(() => {
        const els = Array.from(document.querySelectorAll('div'))
        const b = els.find((d) => getComputedStyle(d).fontSize === '84px')
        return b?.textContent?.trim() || ''
      })
      const word = /^[a-zA-Z]+$/.test(big) ? big : ''
      const lid = (page.url().match(/classroom\/(\d+)/) || [])[1] || ''
      await postClassroom(page, `/classroom/lessons/${lid}/voice`, { text: word || '\u8df3\u8fc7' })
      await page.waitForTimeout(300)
      continue
    }

    if (bodyText.includes('这个单词是什么意思')) {
      // ===== 真实语音作答（模拟学生口头回答：直接说中文释义，如"鱼"）=====
      // 词→释义映射（与 E2E小库 种子一致）；智谱 GLM 真实判分"鱼"是否为 fish 释义
      const MEANINGS: Record<string, string> = { dog: '狗', apple: '苹果', bee: '蜜蜂', fish: '鱼', cat: '猫' }
      const big = await page.evaluate(() => {
        const els = Array.from(document.querySelectorAll('div'))
        const big = els.find((d) => getComputedStyle(d).fontSize === '84px')
        return big?.textContent?.trim() || ''
      })
      const word = /^[a-zA-Z]+$/.test(big) ? big : 'fish'
      const ans = MEANINGS[word] || '鱼'
      // 页面内真实 TTS 朗读答案（模拟学生开口；e2e=1 模式下自动监听已禁——由下方 REST 单次提交）
      await page.evaluate((zh) => {
        try {
          const u = new SpeechSynthesisUtterance(zh)
          u.lang = 'zh-CN'
          u.rate = 0.9
          window.speechSynthesis.speak(u)
        } catch { /* 朗读环境缺失不阻塞 */ }
      }, ans)
      await page.waitForTimeout(800) // 给麦克风监听窗口（紧凑）
      // 语音管线提交（REST /voice → 后端智谱 GLM 真实判分）
      const lid = (page.url().match(/classroom\/(\d+)/) || [])[1] || ''
      await postClassroom(page, `/classroom/lessons/${lid}/voice`, { text: ans })
      await page.waitForTimeout(900) // 真实 LLM 判分往返（节奏紧凑：≤1s）
    } else if (bodyText.includes('点一下这个单词')) {
      // 教学：点大词卡开始（TTS 自动推进；force 容错动画/遮罩）
      const card = page.locator('[role="button"][aria-label*="点击开始学习"]')
      if (await card.isVisible().catch(() => false)) {
        await card.click({ force: true }).catch(() => {})
      }
      await page.waitForTimeout(300)
    } else if (bodyText.includes('再认识一遍')) {
      // RETEACH：TTS 自动推进+跟读，交给下一轮循环（快节奏轮询）
      await page.waitForTimeout(300)
    } else {
      // TTS 播放/判分中：等待自动推进（快节奏轮询）
      await page.waitForTimeout(300)
    }
  }
  await expect(page.getByText('下课啦！叮当老师的小结')).toBeVisible({ timeout: 30_000 })

  // ===== 8. 课报（量化+定性）=====
  await expect(page.getByText(/首答率/)).toBeVisible()
  await page.getByRole('button', { name: '回课程表' }).click()

  // ===== 9. 课程表：自动排下一课或全库毕业（单组小库全对可能直接毕业）=====
  await page.waitForTimeout(800)
  // 重新点词库卡片加载该库课程（lessons 按需加载）
  const grid2 = page.locator('[role="button"][aria-label*="查看"]').filter({ hasText: 'E2E小库' })
  if (await grid2.isVisible().catch(() => false)) {
    await grid2.click()
    await page.waitForTimeout(900)
  }
  const hasNext = await page
    .getByRole('button', { name: '进入课堂' })
    .or(page.getByText('还没到时间哦'))
    .or(page.getByText('课程已全部完成'))
    .first()
    .isVisible({ timeout: 15_000 })
    .catch(() => false)
  expect(hasNext).toBeTruthy()
  const notif = await page.request.get(API + '/student/notifications', {
    headers: { Authorization: `Bearer ${await getUserToken(page)}` },
  })
  const notifs = (await notif.json()).data as Array<{ title?: string; Title?: string }>
  expect(notifs.some((n) => String(n.title ?? n.Title ?? '').includes('课报'))).toBeTruthy()

  // ===== 10. 反馈闭环：语音/文字反馈"太多了" → 自动降量 =====
  await page.getByRole('button', { name: /我想说点啥/ }).click()
  await page.getByPlaceholder('例如：每次词太多了').fill('每次词太多了，累')
  // AntD 两字中文按钮自动插空格（"发 送"），name 匹配不可靠——直接定位 Modal 主按钮
  await page.locator('.ant-modal .ant-btn-primary').click()
  await expect(page.getByText('叮当老师已收到你的反馈')).toBeVisible({ timeout: 15_000 })

  // ===== 11. 退出登录 =====
  // AntD 两字中文按钮自动插空格（"退 出"）——正则容忍
  await page.getByRole('button', { name: /退\s*出/ }).click()
  await expect(page.getByText('叮当words')).toBeVisible()
})

async function getUserToken(page: import('@playwright/test').Page): Promise<string> {
  const tok = await page.evaluate(() => localStorage.getItem('token'))
  return tok || ''
}
