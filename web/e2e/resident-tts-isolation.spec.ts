import { test, expect, type Page, type APIRequestContext } from '@playwright/test'
import { createHash } from 'node:crypto'
import { writeFileSync } from 'node:fs'
import { fixture, login, media, scheduled } from './isolated-fixtures'

const resident = 'http://127.0.0.1:18002/v1/audio/speech'

async function instrument(page: Page) {
  await page.route('**/api/v1/providers', route => route.fulfill({ contentType: 'application/json',
    body: JSON.stringify({ code: 0, msg: 'ok', data: { local: true, asr: false, tts_en: true, tts_zh: true } }) }))
  await media(page, 'hold') // Failure must not be mistaken for successful browser-synth fallback.
  await page.addInitScript(() => {
    const state = { players: [] as Array<{ playing: number; ended: number }>, captions: [] as string[], acks: [] as any[] }
    ;(window as any).__residentBrowser = state
    const NativeAudio = window.Audio
    ;(window as any).Audio = function(src?: string) {
      const player = new NativeAudio(src)
      const row = { playing: 0, ended: 0 }
      state.players.push(row)
      player.addEventListener('playing', () => row.playing++)
      player.addEventListener('ended', () => row.ended++)
      return player
    }
    const NativeSocket = window.WebSocket
    ;(window as any).WebSocket = class extends NativeSocket {
      send(data: string | ArrayBufferLike | Blob | ArrayBufferView) {
        if (typeof data === 'string') {
          try { const frame = JSON.parse(data); if (frame.event === 'tts_finished') state.acks.push(frame) } catch { /* binary */ }
        }
        return super.send(data)
      }
    }
    new MutationObserver(() => {
      const text = document.querySelector('[aria-label="当前朗读字幕"]')?.textContent || ''
      if (state.captions.at(-1) !== text) state.captions.push(text)
    }).observe(document, { childList: true, subtree: true, characterData: true })
  })
}

async function mapSpeech(page: Page, request: APIRequestContext, text: string, hashes: string[]) {
  await page.route('**/api/v1/speech/synthesize', async route => {
    const response = await request.post(resident, { data: { model: 'sherpa-onnx-vits-zh-ll', input: text, voice: 0, speed: 1 } })
    const wav = await response.body()
    if (response.ok()) hashes.push(createHash('sha256').update(wav).digest('hex'))
    try { await route.fulfill({ status: response.status(), contentType: response.ok() ? 'audio/wav' : 'application/json', body: wav }) }
    catch { /* The browser aborted A's fetch after phase invalidation. */ }
  })
}

test('P06 canceled A has no late audio, caption or ACK while B plays isolated resident WAV', async ({ browser, request, baseURL }, info) => {
  const a = await fixture(request, 'resident-a', [{ word: 'cat', meanings: ['猫'] }])
  const b = await fixture(request, 'resident-b', [{ word: 'bird', meanings: ['鸟'] }])
  const [aid, bid] = await Promise.all([scheduled(request, a), scheduled(request, b)])
  const contexts = await Promise.all([browser.newContext({ baseURL }), browser.newContext({ baseURL })])
  const [pa, pb] = await Promise.all(contexts.map(c => c.newPage()))
  const ah: string[] = [], bh: string[] = []
  let publishA: (() => void) | undefined
  let aRequestStarted = false
  try {
    await pa.routeWebSocket('**/ws/classroom?*', socket => {
      const upstream = socket.connectToServer(); let last: any
      upstream.onMessage(message => {
        const frame = JSON.parse(String(message))
        if (frame.context && typeof frame.seq === 'number') last = frame
        socket.send(message)
      })
      socket.onMessage(message => upstream.send(message))
      publishA = () => {
        if (!last) throw new Error('missing A WS context')
        socket.send(JSON.stringify({ type: 'state_snapshot', context: last.context, seq: last.seq + .25,
          phase: 'wait_answer', word: 'cat', quiz_word: 'cat', paused: false }))
      }
    })
    await instrument(pa); await instrument(pb)
    await pa.route('**/api/v1/speech/synthesize', async route => {
      aRequestStarted = true
      const response = await request.post(resident, { data: { model: 'sherpa-onnx-vits-zh-ll', input: 'SLOW_A' } })
      const wav = await response.body(); if (response.ok()) ah.push(createHash('sha256').update(wav).digest('hex'))
      try { await route.fulfill({ status: response.status(), contentType: 'audio/wav', body: wav }) } catch { /* canceled */ }
    })
    await mapSpeech(pb, request, 'B', bh)
    await Promise.all([login(pa, a.username), login(pb, b.username)])
    await Promise.all([pa.goto(`/classroom/${aid}?e2e=1`), pb.goto(`/classroom/${bid}?e2e=1`)])
    await expect.poll(() => aRequestStarted).toBe(true)
    publishA?.()
    await expect.poll(() => ah.length).toBeGreaterThan(0) // Late A response really arrived.
    await expect.poll(() => pb.evaluate(() => (window as any).__residentBrowser.players.some((p: any) => p.ended > 0)), { timeout: 15000 }).toBe(true)
    const ae = await pa.evaluate(() => (window as any).__residentBrowser)
    const be = await pb.evaluate(() => (window as any).__residentBrowser)
    expect(ae.players.every((p: any) => p.playing === 0 && p.ended === 0)).toBe(true)
    expect(ae.captions.filter(Boolean)).toEqual([])
    expect(ae.acks).toEqual([])
    expect(be.players.some((p: any) => p.playing > 0 && p.ended > 0)).toBe(true)
    expect(be.captions.some((s: string) => s.includes('bird'))).toBe(true)
    expect(be.acks.length).toBeGreaterThanOrEqual(1)
    expect(new Set(be.acks.map((ack: any) => ack.event_id)).size).toBe(be.acks.length)
    expect(ah[0]).not.toBe(bh[0])
    const evidence = JSON.stringify({ a: ae, b: be, ah, bh })
    writeFileSync('../.test-artifacts/zh-tts-resident/p06-browser-evidence.json', evidence)
    await info.attach('resident-browser-p06', { body: evidence, contentType: 'application/json' })
  } finally {
    await Promise.all(contexts.map(c => c.close()))
  }
})
