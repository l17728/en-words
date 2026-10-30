import { test, expect } from './recovery-server'

test('anonymous invalid classroom tickets never upgrade a WebSocket', async ({ page }) => {
  await page.goto('/')
  const opened = await page.evaluate(async () => {
    const attempts = Array.from({ length: 25 }, (_, index) => new Promise<boolean>(resolve => {
      const socket = new WebSocket(`ws://${location.host}/ws/classroom?lessonId=${900000 + index}&ticket=invalid`)
      const timeout = window.setTimeout(() => { socket.close(); resolve(false) }, 3000)
      socket.onopen = () => { clearTimeout(timeout); socket.close(); resolve(true) }
      socket.onerror = () => { clearTimeout(timeout); resolve(false) }
    }))
    return Promise.all(attempts)
  })
  expect(opened).toHaveLength(25)
  expect(opened.filter(Boolean)).toEqual([])
  await expect(page.locator('body')).toBeVisible()
})
