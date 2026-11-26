import { api } from '../api/client'
import { recordingToWav } from './wav'
import { playAudio } from './playback'
import { speechCache } from './speech-cache'
import { withMediaDeadline } from './media-request'
import { localCaptureConstraints } from './capture-constraints'

export { localCaptureConstraints } from './capture-constraints'

export interface Capabilities { local: boolean; asr: boolean; tts_zh: boolean; tts_en: boolean }
export const capabilities = () => api.get<Capabilities>('/providers')
const pendingSpeech = new Map<string, Promise<Blob>>()

function speechKey(text: string, lang: 'zh' | 'en', lessonId?: string) {
  return `${localStorage.getItem('token') || ''}:${lessonId || 'preview'}:${lang}:${text}`
}

async function fetchSpeechBlob(text: string, lang: 'zh' | 'en', signal: AbortSignal, lessonId?: string): Promise<Blob> {
  const identity = session(signal)
  const res = await fetch('/api/v1/speech/synthesize', { method: 'POST', headers: { ...identity.headers, 'Content-Type': 'application/json' }, body: JSON.stringify({ text, lang, ...(lessonId ? { lesson_id: Number(lessonId) } : {}) }), signal })
  identity.check()
  if (!res.ok) throw new Error('本机语音合成不可用')
  return res.blob()
}

// Start synthesis during the configured inter-segment gap. This keeps the
// perceived gap stable when Chinese TTS has a longer server response time.
export function prefetchLocalSpeech(text: string, lang: 'zh' | 'en', lessonId?: string) {
  const key = speechKey(text, lang, lessonId)
  if (pendingSpeech.has(key)) return
  const controller = new AbortController()
  const timeout = setTimeout(() => controller.abort(), 25_000)
  const promise = fetchSpeechBlob(text, lang, controller.signal, lessonId).finally(() => clearTimeout(timeout))
  pendingSpeech.set(key, promise)
  void promise.catch(() => {
    // A failed prefetch must never poison the next playback attempt.
    if (pendingSpeech.get(key) === promise) pendingSpeech.delete(key)
  }).then(() => {
    // Keep successful media available for the immediately following segment.
    setTimeout(() => { if (pendingSpeech.get(key) === promise) pendingSpeech.delete(key) }, 30_000)
  })
}

function session(signal: AbortSignal) {
  const token = localStorage.getItem('token') || ''
  const check = () => {
    if (signal.aborted || token !== (localStorage.getItem('token') || '')) {
      throw new DOMException('Aborted', 'AbortError')
    }
  }
  check()
  return { check, headers: { Authorization: `Bearer ${token}` } }
}

// A finite capture, with cancellation releasing both recording and microphone tracks.
export async function recordLocal(lessonId: string, language: string, signal: AbortSignal, onStage?: (stage: 'recording' | 'transcribing') => void, maxDurationMs = 4000): Promise<string> {
  const identity = session(signal)
  const stream = await navigator.mediaDevices.getUserMedia({ audio: localCaptureConstraints })
  let audioContext: AudioContext | undefined
  let vadTimer: ReturnType<typeof setInterval> | undefined
  try {
    identity.check()
    const recorder = new MediaRecorder(stream)
    const chunks: Blob[] = []
    const audio = await new Promise<Blob>((resolve, reject) => {
      let watchdogTimer: ReturnType<typeof setTimeout> | undefined
      const cleanup = () => { clearTimeout(timer); if (watchdogTimer) clearTimeout(watchdogTimer); if (vadTimer) clearInterval(vadTimer); signal.removeEventListener('abort', abort); void audioContext?.close().catch(() => undefined) }
      const abort = () => { cleanup(); if (recorder.state !== 'inactive') recorder.stop(); reject(new DOMException('Aborted', 'AbortError')) }
      const timer = setTimeout(() => { if (recorder.state !== 'inactive') recorder.stop() }, maxDurationMs)
      // MediaRecorder can occasionally miss its stop event when several
      // AudioContexts are active. Never leave the classroom in RECORDING.
      watchdogTimer = setTimeout(() => {
        if (recorder.state === 'inactive') return
        try { recorder.stop() } catch { abort(); return }
        setTimeout(() => { if (recorder.state !== 'inactive') abort() }, 750)
      }, maxDurationMs + 500)
      recorder.ondataavailable = e => { if (e.data.size) chunks.push(e.data) }
      recorder.onstart = () => {
        if (recorder.state !== 'recording') return
        try {
          identity.check(); onStage?.('recording')
          // Keep one capture window until speech ends. RMS based VAD avoids
          // submitting a fixed short slice while the student is still speaking.
          const Ctx = window.AudioContext || (window as any).webkitAudioContext
          if (!Ctx) return
          audioContext = new Ctx()
          const source = audioContext.createMediaStreamSource(stream)
          const analyser = audioContext.createAnalyser()
          analyser.fftSize = 2048
          source.connect(analyser)
          const samples = new Uint8Array(analyser.fftSize)
          const startedAt = performance.now()
          let speechStarted = false
          let silenceSince = 0
          vadTimer = setInterval(() => {
            if (recorder.state !== 'recording') return
            analyser.getByteTimeDomainData(samples)
            let sum = 0
            for (const value of samples) { const centered = (value - 128) / 128; sum += centered * centered }
            const rms = Math.sqrt(sum / samples.length)
            const now = performance.now()
            if (rms >= 0.018) {
              speechStarted = true
              silenceSince = 0
            } else if (speechStarted) {
              if (!silenceSince) silenceSince = now
              if (now - silenceSince >= 650) recorder.stop()
            }
            if (now - startedAt >= maxDurationMs) recorder.stop()
          }, 50)
        } catch { abort() }
      }
      recorder.onerror = () => { cleanup(); reject(new Error('麦克风录音失败，请使用文字输入')) }
      recorder.onstop = () => { cleanup(); resolve(new Blob(chunks, { type: recorder.mimeType })) }
      signal.addEventListener('abort', abort, { once: true })
      try { identity.check(); recorder.start() } catch (error) { cleanup(); reject(error) }
    })
    identity.check()
    if (!audio.size) throw new Error('未录到声音，请重试或使用文字输入')
    stream.getTracks().forEach(t => t.stop())
    identity.check()
    onStage?.('transcribing')
    const wav = await recordingToWav(audio, identity.check)
    identity.check()
    const form = new FormData()
    form.append('file', wav, 'recording.wav'); form.append('language', language)
    let lastError: unknown
    for (let attempt = 0; attempt < 2; attempt++) {
      try {
        return await withMediaDeadline(signal, async requestSignal => {
          const res = await fetch(`/api/v1/classroom/lessons/${lessonId}/transcribe`, { method: 'POST', headers: identity.headers, body: form, signal: requestSignal })
          const out = await res.json().catch(() => ({}))
          identity.check()
          if (!res.ok || !out || out.code !== 0) {
            const reason = typeof out?.msg === 'string' && out.msg.trim() ? out.msg.trim() : `HTTP ${res.status}`
            throw new Error(`语音识别失败（${reason}），请重试或使用文字输入`)
          }
          if (typeof out.data?.text !== 'string' || !out.data.text.trim()) throw new Error('未识别到内容，请重试或使用文字输入')
          return out.data.text as string
        }, 30000, { lessonId, operation: 'transcribe' })
      } catch (error) {
        lastError = error
        if (signal.aborted || (error instanceof DOMException && error.name === 'AbortError') || attempt === 1) throw error
        await new Promise<void>((resolve, reject) => {
          const timer = setTimeout(resolve, 400)
          signal.addEventListener('abort', () => { clearTimeout(timer); reject(new DOMException('Aborted', 'AbortError')) }, { once: true })
        })
      }
    }
    throw lastError instanceof Error ? lastError : new Error('语音识别失败，请重试或使用文字输入')
  } finally { if (vadTimer) clearInterval(vadTimer); await audioContext?.close().catch(() => undefined); stream.getTracks().forEach(t => t.stop()) }
}

export async function playLocalSpeech(text: string, lang: 'zh' | 'en', signal: AbortSignal, lessonId?: string, onPlaying?: () => void): Promise<void> {
  const identity = session(signal)
  const started = performance.now()
  const hit = await speechCache.play(JSON.stringify([identity.headers.Authorization, lessonId ?? 'preview', lang]), text, signal,
    () => withMediaDeadline(signal, async requestSignal => {
      const key = speechKey(text, lang, lessonId)
      const prefetched = pendingSpeech.get(key)
      const blob = prefetched ? await prefetched : await fetchSpeechBlob(text, lang, requestSignal, lessonId)
      identity.check()
      return blob
    }, 30000, { lessonId, operation: 'synthesize' }), async blob => {
      const url = URL.createObjectURL(blob)
      let audio: HTMLAudioElement | undefined
      let audioContext: AudioContext | undefined
      let source: MediaElementAudioSourceNode | undefined
      let gain: GainNode | undefined
      let compressor: DynamicsCompressorNode | undefined
      try {
        const player = new Audio(url)
        audio = player
        // The local Chinese voice is mastered quieter than the English voice.
        // Use a small playback gain so cached media does not need rewriting.
        player.volume = 1
        try {
          const AudioContextCtor = (window as typeof window & { webkitAudioContext?: typeof AudioContext }).AudioContext || (window as typeof window & { webkitAudioContext?: typeof AudioContext }).webkitAudioContext
          if (AudioContextCtor) {
            audioContext = new AudioContextCtor()
            source = audioContext.createMediaElementSource(player)
            gain = audioContext.createGain()
            gain.gain.value = lang === 'zh' ? 6.2 : 1
            compressor = audioContext.createDynamicsCompressor()
            compressor.threshold.value = -3
            compressor.knee.value = 6
            compressor.ratio.value = 8
            compressor.attack.value = 0.003
            compressor.release.value = 0.12
            source.connect(gain).connect(compressor).connect(audioContext.destination)
            await audioContext.resume()
          }
        } catch {
          // Fall back to the element volume when Web Audio is unavailable.
        }
        await playAudio(player, signal, identity.check, onPlaying)
      } finally {
        audio?.pause(); source?.disconnect(); gain?.disconnect(); compressor?.disconnect(); await audioContext?.close().catch(() => undefined); URL.revokeObjectURL(url)
      }
    }, identity.check)
  console.info('[speech_cache]', { module: 'speech', event: 'playback_completed', trace_id: `lesson-${lessonId ?? 'preview'}`,
    lesson_id: lessonId, cache: hit ? 'hit' : 'miss', duration_ms: Math.round(performance.now() - started) })
}
