import { test, expect, type Page } from '@playwright/test'
import { readFileSync } from 'node:fs'
import { api, fixture, scheduled, login } from './isolated-fixtures'

async function installMedia(page: Page, en: Buffer, zh: Buffer) {
  await page.route('**/independent-en.wav', route => route.fulfill({ contentType: 'audio/wav', body: en }))
  await page.route('**/independent-zh.wav', route => route.fulfill({ contentType: 'audio/wav', body: zh }))
  await page.addInitScript(() => {
    const state = { audio: [] as Array<{ start: number; end: number; errors: number }>,
      captions: [] as Array<{ text: string; at: number }>, captures: [] as string[], acks: [] as any[] }
    ;(window as any).__twoMedia = state
    const NativeSocket = window.WebSocket
    ;(window as any).WebSocket = class extends NativeSocket {
      send(data: string | ArrayBufferLike | Blob | ArrayBufferView) {
        if (typeof data === 'string') {
          try { const frame = JSON.parse(data); if (frame.event === 'tts_finished') state.acks.push(frame) } catch { /* binary/other frame */ }
        }
        return super.send(data)
      }
    }
    new MutationObserver(() => {
      const region = document.querySelector('[aria-label="当前朗读字幕"]')
      const text = region?.lastElementChild?.textContent || ''
      if (state.captions.at(-1)?.text !== text) state.captions.push({ text, at: performance.now() })
    }).observe(document, { subtree: true, childList: true, characterData: true })
    const NativeAudio = window.Audio
    ;(window as any).Audio = function(src?: string) {
      const player = new NativeAudio(src)
      const row = { start: 0, end: 0, errors: 0 }
      state.audio.push(row)
      player.addEventListener('playing', () => { row.start = performance.now() })
      player.addEventListener('ended', () => { row.end = performance.now() })
      player.addEventListener('error', () => { row.errors++ })
      return player
    }
    const sources = new WeakMap<MediaStream, AudioBufferSourceNode>()
    const NativeRecorder = window.MediaRecorder
    ;(window as any).MediaRecorder = class extends NativeRecorder {
      constructor(stream: MediaStream, options?: MediaRecorderOptions) {
        super(stream, options)
        this.addEventListener('start', () => {
          const source = sources.get(stream)
          if (!source) throw new Error('independent source missing')
          source.start()
        })
      }
    }
    navigator.mediaDevices.getUserMedia = async () => {
      const language = document.body.textContent?.includes('轮到你读了') ? 'en' : 'zh'
      state.captures.push(language)
      const context = new AudioContext()
      const buffer = await context.decodeAudioData(await (await fetch(`/independent-${language}.wav`)).arrayBuffer())
      const destination = context.createMediaStreamDestination()
      const source = context.createBufferSource()
      source.buffer = buffer
      source.connect(destination)
      await context.resume()
      sources.set(destination.stream, source)
      for (const track of destination.stream.getTracks()) {
        const stop = track.stop.bind(track)
        track.stop = () => { try { source.stop() } catch { /* already ended */ }; void context.close(); stop() }
      }
      return destination.stream
    }
  })
}

test('two isolated students complete real bilingual playback and ASR lessons concurrently', async ({ browser, request, baseURL }, info) => {
  const enPath = process.env.E2E_EN_AUDIO
  const zhPath = process.env.E2E_ZH_AUDIO
  if (!enPath || !zhPath) throw new Error('E2E_EN_AUDIO and E2E_ZH_AUDIO must name frozen independent WAV fixtures')
  const en = readFileSync(enPath), zh = readFileSync(zhPath)
  const appleEn = readFileSync('../review/audio-fixtures/en/01.wav')
  const appleZh = readFileSync('../review/audio-fixtures/zh/01.wav')
  const first = await fixture(request, 'two-real-a', [{ word: 'teacher', meanings: ['老师'] }])
  const second = await fixture(request, 'two-real-b', [{ word: 'apple', meanings: ['苹果'] }])
  const [firstId, secondId] = await Promise.all([scheduled(request, first), scheduled(request, second)])
  expect(firstId).not.toBe(secondId)
  for (const f of [first, second]) {
    const c = await api(request, '/providers', f.student.token, undefined, 'GET')
    expect(c).toMatchObject({ asr: true, tts_en: true, tts_zh: true })
  }
  const contexts = await Promise.all([browser.newContext({ baseURL }), browser.newContext({ baseURL })])
  const pages = await Promise.all(contexts.map(context => context.newPage()))
  const evidence: any[] = []
  const responses: Array<Array<{ kind: string; status: number }>> = [[], []]
  try {
    pages.forEach((page, index) => page.on('response', response => {
      const kind = response.url().endsWith('/speech/synthesize') ? 'tts'
        : response.url().endsWith('/transcribe') ? 'asr' : ''
      if (kind) responses[index].push({ kind, status: response.status() })
    }))
    await Promise.all([installMedia(pages[0], en, zh), installMedia(pages[1], appleEn, appleZh)])
    const runs = [[first, firstId], [second, secondId]] as const
    await Promise.all(pages.map((page, index) => login(page, runs[index][0].username)))
    await Promise.all(pages.map((page, index) => page.goto(`/classroom/${runs[index][1]}`)))
    await Promise.all(pages.map(page => expect(page.getByText('下课啦！叮当老师的小结')).toBeVisible({ timeout: 180_000 })))
    for (const [index, page] of pages.entries()) {
      const measured = await page.evaluate(() => ({ state: (window as any).__twoMedia,
        playback: (window as any).__ttsDebug || [], journeyDurationMs: Math.round(performance.now()) }))
      evidence.push({ student: runs[index][0].username, lesson: runs[index][1], responses: responses[index],
        ...measured, firstAudioMs: Math.round(measured.state.audio[0]?.start ?? -1) })
    }
    for (const [index, page] of pages.entries()) {
      await expect(page.getByText(/首答率 100%/)).toBeVisible()
      expect(page.url()).toContain(`/classroom/${runs[index][1]}`)
      const { state, playback } = evidence[index]
      expect(state.captures).toEqual(['en', 'zh'])
      expect(state.audio).toHaveLength(4)
      expect(state.audio.every((row: any) => row.start > 0 && row.end > row.start && row.errors === 0)).toBe(true)
      const visible = state.captions.filter((row: any) => row.text)
      const expected = index === 0 ? ['teacher', 'teacher', 'teacher', '老师'] : ['apple', 'apple', 'apple', '苹果']
      expect(visible.map((row: any) => row.text)).toEqual(expected)
      for (const [segment, row] of state.audio.entries()) {
        expect(visible[segment].at).toBeGreaterThanOrEqual(row.start)
        expect(visible[segment].at).toBeLessThan(row.end)
      }
      expect(state.acks).toHaveLength(4)
      expect(new Set(state.acks.map((ack: any) => ack.event_id)).size).toBe(4)
      expect(playback).toHaveLength(4)
      expect(playback.every((row: any) => row.status === 'completed' && row.local && row.startMs !== null)).toBe(true)
      expect(responses[index].filter(row => row.kind === 'asr').map(row => row.status)).toEqual([200, 200])
      expect(responses[index].filter(row => row.kind === 'tts').map(row => row.status)).toEqual([200, 200])
    }
  } finally {
    await info.attach('two-student-real-media', { body: JSON.stringify(evidence), contentType: 'application/json' })
    await Promise.all(contexts.map(context => context.close()))
  }
})
