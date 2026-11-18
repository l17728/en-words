import { test, expect } from './recovery-server'
import { fixture, scheduled, login, media } from './isolated-fixtures'

test('uncertain settlement outcome instructs the student not to retry', async ({ page, request }) => {
  const f = await fixture(request, 'settlement-uncertain')
  const id = await scheduled(request, f)
  let injected = false
  await page.routeWebSocket('**/ws/classroom?*', socket => {
    const upstream = socket.connectToServer()
    let lastSeq = 0
    upstream.onMessage(message => {
      let frame: any
      try { frame = JSON.parse(String(message)) } catch { socket.send(message); return }
      lastSeq = Math.max(lastSeq, frame.seq || 0)
      if (injected && frame.type === 'state_snapshot') frame.settlement_uncertain = true
      socket.send(injected && frame.type === 'state_snapshot' ? JSON.stringify(frame) : message)
      if (!injected && frame.type === 'state_snapshot') {
        injected = true
        socket.send(JSON.stringify({
          type: 'settlement_failed', seq: lastSeq + 0.5, lesson_id: id,
          retryable: false, outcome: 'outcome_uncertain',
        }))
      }
    })
  })
  await media(page)
  await login(page, f.username)
  await page.goto(`/classroom/${id}?e2e=1`)
  await expect(page.getByText('课堂结算结果暂时无法确认。请刷新课程状态后再继续，不要重复提交。')).toBeVisible()
  expect(injected).toBe(true)
  await page.reload()
  await expect(page.getByText('课堂结算结果暂时无法确认。请刷新课程状态后再继续，不要重复提交。')).toBeVisible()
})
