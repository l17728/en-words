import { randomUUID } from 'node:crypto'
import type { Page } from '@playwright/test'

// Legacy REST-driven fixtures obtain context from the server immediately before
// submission. They remain protocol tests, not evidence of real speech playback.
export async function postClassroom(page: Page, path: string, body: Record<string, unknown>, options: { allowRejected?: boolean } = {}) {
  return page.evaluate(async ({ path, body, eventId, allowRejected }) => {
    const headers = { 'Content-Type': 'application/json', Authorization: `Bearer ${localStorage.getItem('token') || ''}` }
    const lessonPath = path.replace(/\/(event|voice)$/, '')
    const entered = await (await fetch(`/api/v1${lessonPath}/enter`, { method: 'POST', headers })).json()
    const context = entered.data?.snapshot?.context
    if (entered.code !== 0 || !context) throw new Error('Fixture could not obtain server classroom context')
    const response = await (await fetch(`/api/v1${path}`, {
      method: 'POST', headers,
      body: JSON.stringify({ ...body, context, event_id: eventId }),
    })).json()
    const frames = Array.isArray(response.data) ? response.data : response.data?.frames || []
    if (response.code !== 0) throw new Error(`Classroom fixture request failed: code=${response.code}`)
    const rejection = frames.find((frame: { type: string }) => frame.type === 'event_rejected')
    if (rejection && !allowRejected) throw new Error(`Classroom event rejected: reason=${rejection.reason || rejection.error_code || 'unknown'}`)
    return response
  }, { path, body, eventId: randomUUID(), allowRejected: options.allowRejected === true })
}
