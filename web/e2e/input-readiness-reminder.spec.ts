import { test, expect } from './recovery-server'
import { fixture, scheduled, login, media } from './isolated-fixtures'
import { postClassroom } from './classroom-protocol'

test('answer reminder refreshes turn authority without erasing a typed draft', async ({ page, request }) => {
  const f = await fixture(request, 'answer-reminder-draft', [{ word: 'cat', meanings: ['猫'] }])
  const id = await scheduled(request, f)
  let injectReminder: (() => void) | undefined
  await page.routeWebSocket('**/ws/classroom?*', socket => {
    const upstream = socket.connectToServer()
    let lastSeq = 0
    upstream.onMessage(message => {
      let frame: any
      try { frame = JSON.parse(String(message)) } catch { socket.send(message); return }
      lastSeq = Math.max(lastSeq, frame.seq || 0)
      socket.send(message)
      if (injectReminder || frame.action?.type !== 'show_quiz_item' || !frame.context) return
      injectReminder = () => socket.send(JSON.stringify({
        type: 'ui_action', seq: lastSeq + 1,
        context: { ...frame.context, turn: `${frame.context.turn}:reminder` },
        action: { type: 'nudge_answer', text: 'Take your time.' },
      }))
    })
  })

  await media(page)
  await login(page, f.username)
  await page.goto(`/classroom/${id}?e2e=1`)
  await expect(page.getByLabel('跟读文字输入框')).toBeVisible()
  await postClassroom(page, `/classroom/lessons/${id}/voice`, { text: 'cat' })
  await expect(page.getByLabel('答案输入框')).toBeVisible()
  await expect.poll(() => Boolean(injectReminder)).toBe(true)
  const input = page.getByLabel('答案输入框')
  await input.fill('my answer draft')
  injectReminder!()
  await expect(page.getByText('Take your time.')).toBeVisible()
  await expect(input).toHaveValue('my answer draft')
  await expect(input).toBeEnabled()
})
