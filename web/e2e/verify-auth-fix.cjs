// 验证 unauthorized BUG 修复：模拟真实用户流（登录→立即选库）+ 登出重登另一账号
const { chromium } = require('playwright-core')

;(async () => {
  const b = await chromium.launch({ headless: true })
  const ctx = await b.newContext()
  const p = await ctx.newPage()
  const errors = []
  p.on('pageerror', (e) => errors.push('PAGEERROR: ' + e.message))

  // 全新会话（无任何 localStorage 残留——原 BUG 触发条件）
  await p.goto('http://localhost:5173/login')
  await p.fill('input[placeholder*="学生或管理员"]', 'dingdangcat')
  await p.fill('input[type="password"]', 'cat123456')
  await p.getByRole('button', { name: '开始学习' }).click()
  await p.waitForTimeout(1500)
  console.log('登录后 URL:', p.url())

  // 原 BUG 场景：选词库（enable 调用）
  await p.goto('http://localhost:5173/books')
  await p.waitForTimeout(1200)
  const card = p.locator('.ant-card', { hasText: '人教版初一' }).first()
  if (await card.isVisible().catch(() => false)) {
    await card.getByRole('button', { name: '选择这个词库' }).click()
    await p.waitForTimeout(1000)
    const body = (await p.textContent('body')) || ''
    console.log('选库结果:', body.includes('unauthorized') ? '❌ BUG 仍存在' : '✅ 无 unauthorized')
    // 关掉可能弹的确认框
    const ok = p.getByRole('button', { name: /开始摸底|稍后再说/ }).last()
    if (await ok.isVisible().catch(() => false)) await ok.click().catch(() => {})
  } else {
    console.log('词库卡片未出现，页面:', ((await p.textContent('body')) || '').slice(0, 200))
  }

  // 场景2：登出→管理员登录（同类问题：登出残留旧 token）
  await p.goto('http://localhost:5173/dashboard')
  await p.waitForTimeout(800)
  const logoutBtn = p.getByRole('button', { name: '退出' })
  if (await logoutBtn.isVisible().catch(() => false)) {
    await logoutBtn.click()
    await p.waitForTimeout(800)
    await p.fill('input[placeholder*="学生或管理员"]', 'dad')
    await p.fill('input[type="password"]', 'family123')
    await p.getByRole('button', { name: '开始学习' }).click()
    await p.waitForTimeout(1500)
    const body2 = (await p.textContent('body')) || ''
    console.log('登出重登管理员:', body2.includes('unauthorized') ? '❌ 残留 token BUG' : '✅ 会话切换正常')
    console.log('重登后 URL:', p.url())
  }
  console.log('PAGEERRORS:', errors)
  await b.close()
})()
