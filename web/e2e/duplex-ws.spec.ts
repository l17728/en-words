import { test, expect } from './recovery-server'
import { api, fixture, scheduled, login } from './isolated-fixtures'

test.use({ localModel: true, duplexPilot: true, duplexShadow: true })

test('WS duplex shadow requires explicit confirmation before classroom progress', async ({ page, request }) => {
  test.setTimeout(120_000)
  const f = await fixture(request, 'duplex-ws')
  const lessonId = await scheduled(request, f)
  const entered = await api(request, `/classroom/lessons/${lessonId}/enter`, f.student.token, {})
  await login(page, f.username)
  await page.goto('/dashboard')

  const result = await page.evaluate(async ({ lessonId, ticket, context }) => {
    const frames: any[] = []
    let liveContext = context
    const url = `/ws/classroom?lessonId=${lessonId}&ticket=${encodeURIComponent(ticket)}`
    const ws = new WebSocket(url)
    const waitFor = (type: string) => new Promise<any>((resolve, reject) => {
      const timer = setTimeout(() => reject(new Error(`timeout:${type}`)), 30_000)
      const handler = (event: MessageEvent) => {
        const frame = JSON.parse(String(event.data)); frames.push(frame)
        if (frame.context) liveContext = frame.context
        if (frame.snapshot?.context) liveContext = frame.snapshot.context
        if (frame.type === type) { clearTimeout(timer); ws.removeEventListener('message', handler); resolve(frame) }
        if (frame.type === 'voice_stream_error') { clearTimeout(timer); ws.removeEventListener('message', handler); reject(new Error(`stream_error:${frame.reason}:waiting:${type}`)) }
      }
      ws.addEventListener('message', handler)
    })
    await new Promise<void>((resolve, reject) => { ws.onopen = () => resolve(); ws.onerror = () => reject(new Error('ws_error')) })
    const envelope = (id: string) => ({ context, event_id: id })
    ws.send(JSON.stringify({ type: 'voice_stream_start', language: 'en', sample_rate: 16000, channels: 1, mode: 'shadow', ...envelope('duplex-start') }))
    const start = await waitFor('voice_stream_start_ack')
    const pcm = new Uint8Array(640)
    let binary = ''
    for (const byte of pcm) binary += String.fromCharCode(byte)
    ws.send(JSON.stringify({ ...start, type: 'voice_stream_chunk', audio: btoa(binary), chunk_seq: 0, ...envelope('duplex-chunk') }))
    await waitFor('voice_stream_ack')
    ws.send(JSON.stringify({ ...start, type: 'voice_stream_end', ...envelope('duplex-end') }))
    const final = await waitFor('voice_confirmation_required')
    const bytes = new TextEncoder().encode(final.text)
    const digest = await crypto.subtle.digest('SHA-256', bytes)
    const hash = Array.from(new Uint8Array(digest)).map(value => value.toString(16).padStart(2, '0')).join('')
    ws.send(JSON.stringify({ ...start, type: 'voice_confirm', text: final.text, text_hash: hash, input_revision: final.input_revision || start.input_revision + 1, ...envelope('duplex-confirm'), context: liveContext }))
    const confirmed = await waitFor('voice_confirmed')
    ws.close()
    return { confirmed, frames: frames.map(frame => frame.type) }
  }, { lessonId, ticket: entered.ticket, context: entered.snapshot.context })

  expect(result.confirmed.type).toBe('voice_confirmed')
  expect(result.confirmed.commit_receipt).toBeTruthy()
  expect(result.frames).toContain('voice_confirmation_required')
  expect(result.frames).toContain('voice_confirmed')
})
