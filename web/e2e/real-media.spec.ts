import { test, expect } from '@playwright/test'
import { readFileSync } from 'node:fs'
import { api, fixture, scheduled, login } from './isolated-fixtures'

// No model or media event mocks: independent WAV sources are decoded into real
// MediaStreams, encoded by MediaRecorder, and recognized by the user's console.
// This measures automated audio lifecycle/recognition, not human listening.
test('real local Chinese audio playback and real encoded English/Chinese recognition complete a lesson', async ({ page, request }, info) => {
  if (!process.env.E2E_EN_AUDIO || !process.env.E2E_ZH_AUDIO) throw new Error('Independent English cat and Chinese 猫 WAV fixtures are required')
  const en = readFileSync(process.env.E2E_EN_AUDIO); const zh = readFileSync(process.env.E2E_ZH_AUDIO)
  const f = await fixture(request, 'real-media', [{ word: 'cat', meanings: ['猫'] }]); const id = await scheduled(request, f)
  await page.route('**/independent-en.wav', route => route.fulfill({ contentType: 'audio/wav', body: en }))
  await page.route('**/independent-zh.wav', route => route.fulfill({ contentType: 'audio/wav', body: zh }))
  await page.addInitScript(() => {
    const state = { audio: [] as any[], captures: [] as string[], recordings: [] as any[], starts: [] as any[] }
    const sources = new WeakMap<MediaStream, { source: AudioBufferSourceNode; durationMs: number }>()
    ;(window as any).__realMedia = state
    const NativeRecorder = window.MediaRecorder
    ;(window as any).MediaRecorder = class extends NativeRecorder {
      constructor(stream: MediaStream, options?: MediaRecorderOptions) {
        super(stream, options)
        this.addEventListener('start', () => {
          const entry = sources.get(stream)
          if (!entry) throw new Error('Independent source is missing for this recorded stream')
          const recorderStartMs = performance.now()
          entry.source.start()
          state.starts.push({ recorderStartMs, sourceStartMs: performance.now(), durationMs: entry.durationMs })
        })
        this.addEventListener('dataavailable', async event => {
          const bytes = new Uint8Array(await event.data.arrayBuffer())
          state.recordings.push({ mime: event.data.type, bytes: bytes.length, magic: Array.from(bytes.slice(0, 4)) })
        })
      }
    }
    const NativeAudio = window.Audio
    ;(window as any).Audio = function(src?: string) {
      const player = new NativeAudio(src)
      const entry = { playing: 0, ended: 0, errors: 0 }
      state.audio.push(entry)
      player.addEventListener('playing', () => { entry.playing++ })
      player.addEventListener('ended', () => { entry.ended++ })
      player.addEventListener('error', () => { entry.errors++ })
      return player
    }
    navigator.mediaDevices.getUserMedia = async () => {
      const language = document.body.textContent?.includes('轮到你读了') ? 'en' : 'zh'
      state.captures.push(language)
      const context = new AudioContext()
      const buffer = await context.decodeAudioData(await (await fetch(`/independent-${language}.wav`)).arrayBuffer())
      const destination = context.createMediaStreamDestination()
      const source = context.createBufferSource(); source.buffer = buffer; source.connect(destination)
      await context.resume()
      sources.set(destination.stream, { source, durationMs: buffer.duration * 1000 })
      for (const track of destination.stream.getTracks()) {
        const stop = track.stop.bind(track)
        track.stop = () => { try { source.stop() } catch {} void context.close(); stop() }
      }
      return destination.stream
    }
  })
  const responses: any[] = []; const uploads: any[] = []
  page.on('request', request => {
    if (!request.url().endsWith('/transcribe')) return
    const body = request.postDataBuffer()!
    const offset = body.indexOf(Buffer.from('RIFF'))
    uploads.push({ filename: body.includes(Buffer.from('filename="recording.wav"')), riff: offset >= 0,
      ...(offset >= 0 && offset + 44 <= body.length ? { format: body.readUInt16LE(offset + 20), channels: body.readUInt16LE(offset + 22), sampleRate: body.readUInt32LE(offset + 24), bits: body.readUInt16LE(offset + 34) } : {}) })
  })
  page.on('response', async response => {
    if (response.url().endsWith('/transcribe')) responses.push({ kind: 'asr', status: response.status(), body: await response.json() })
    if (response.url().endsWith('/speech/synthesize')) responses.push({ kind: 'tts', status: response.status(), mime: response.headers()['content-type'] })
  })
  await login(page, f.username)
  const capabilities = await api(request, '/providers', f.student.token, undefined, 'GET')
  expect(capabilities.local).toBe(true); expect(capabilities.asr).toBe(true); expect(capabilities.tts_zh).toBe(true)
  await page.goto(`/classroom/${id}`)
  try {
    await expect(page.getByText('下课啦！叮当老师的小结')).toBeVisible({ timeout: 150_000 })
    await expect(page.getByText(/首答率 100%/)).toBeVisible()
    const state = await page.evaluate(() => (window as any).__realMedia)
    expect(state.captures).toEqual(['en', 'zh'])
    expect(state.recordings).toHaveLength(2)
    expect(state.starts).toHaveLength(2)
    expect(state.starts.every((start: any) => start.sourceStartMs >= start.recorderStartMs && start.durationMs > 0 && start.durationMs <= 4000)).toBe(true)
    expect(state.recordings.every((record: any) => record.mime.includes('webm') && record.bytes > 0 && record.magic.join(',') === '26,69,223,163')).toBe(true)
    expect(uploads).toEqual([0, 1].map(() => ({ filename: true, riff: true, format: 1, channels: 1, sampleRate: 16000, bits: 16 })))
    expect(state.audio.some((row: any) => row.playing > 0 && row.ended > 0 && row.errors === 0)).toBe(true)
    expect(responses.filter(r => r.kind === 'tts' && r.status === 200 && r.mime?.includes('audio/'))).toHaveLength(1)
    expect(responses.filter(r => r.kind === 'asr')).toHaveLength(2)
    expect(responses.filter(r => r.kind === 'asr').every(r => r.status === 200 && r.body.code === 0)).toBe(true)
    const english = await page.evaluate(() => ((window as any).__ttsDebug || []).filter((s: any) => s.lang === 'en-US'))
    expect(english).toHaveLength(3)
    expect(english.every((s: any) => s.status === 'completed' && s.startMs !== null && s.local)).toBe(true)
  } finally {
    await info.attach('real-media-evidence', { body: JSON.stringify({ responses, uploads, browser: await page.evaluate(() => (window as any).__realMedia), playback: await page.evaluate(() => (window as any).__ttsDebug || []) }), contentType: 'application/json' })
  }
})
