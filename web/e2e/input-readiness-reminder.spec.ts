import { test, expect } from './recovery-server'
import { fixture, scheduled, login, media } from './isolated-fixtures'
import { postClassroom } from './classroom-protocol'

test('answer reminder refreshes turn authority without erasing a typed draft', async ({ page, request }) => {
  const f = await fixture(request, 'answer-reminder-draft', [{ word: 'cat', meanings: ['猫'] }])
  const id = await scheduled(request, f)
  const sent: any[] = []
  let quizContext: any
  let reminder: any
  await page.routeWebSocket('**/ws/classroom?*', socket => {
    const upstream = socket.connectToServer()
    socket.onMessage(message => { try { sent.push(JSON.parse(String(message))) } catch {} upstream.send(message) })
    upstream.onMessage(message => {
      let frame: any
      try { frame = JSON.parse(String(message)) } catch { socket.send(message); return }
      if (frame.action?.type === 'show_quiz_item') quizContext = frame.context
      if (frame.action?.type === 'nudge_answer') {
        reminder = frame
      }
      socket.send(message)
    })
  })

  await media(page)
  await login(page, f.username)
  await page.goto(`/classroom/${id}?e2e=1`)
  await expect(page.getByLabel('跟读文字输入框')).toBeVisible()
  await postClassroom(page, `/classroom/lessons/${id}/voice`, { text: 'cat' })
  await expect(page.getByLabel('答案输入框')).toBeVisible()
  const input = page.getByLabel('答案输入框')
  await input.fill('猫')
  await expect.poll(() => reminder?.action?.type, { timeout: 35_000 }).toBe('nudge_answer')
  expect(reminder?.action?.type).toBe('nudge_answer')
  expect(reminder.context.turn).not.toBe(quizContext.turn)
  await expect(input).toHaveValue('猫')
  await expect(input).toBeEnabled()
  await input.press('Enter')
  await expect(page.getByLabel('回答正确')).toBeVisible()
  expect(sent.filter(frame => frame.event === 'answer_submitted').map(frame => frame.text)).toEqual(['猫'])
})
