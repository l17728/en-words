import type { Page } from '@playwright/test'

// Legacy REST-driven fixtures obtain context from the server immediately before
// submission. They remain protocol tests, not evidence of real speech playback.
export async function postClassroom(page: Page, path: string, body: Record<string, unknown>) {
  return page.evaluate(async ({ path, body }) => {
    const headers = { 'Content-Type': 'application/json', Authorization: `Bearer ${localStorage.getItem('token') || ''}` }
    const lessonPath = path.replace(/\/(event|voice)$/, '')
    const entered = await (await fetch(`/api/v1${lessonPath}/enter`, { method: 'POST', headers })).json()
    const context = entered.data?.snapshot?.context
    if (entered.code !== 0 || !context) throw new Error('Fixture could not obtain server classroom context')
    const response = await (await fetch(`/api/v1${path}`, {
      method: 'POST', headers,
      body: JSON.stringify({ ...body, context, event_id: crypto.randomUUID() }),
    })).json()
    const frames = Array.isArray(response.data) ? response.data : response.data?.frames || []
    if (response.code !== 0 || frames.some((frame: { type: string }) => frame.type === 'event_rejected')) {
      throw new Error('Legacy fixture raced the active browser media turn; use deterministic media fixtures')
    }
    return response
  }, { path, body })
}
