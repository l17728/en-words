import { api } from '../api/client'
import { recordingToWav } from './wav'
import { playAudio } from './playback'

export interface Capabilities { local: boolean; asr: boolean; tts_zh: boolean; tts_en: boolean }
export const capabilities = () => api.get<Capabilities>('/providers')

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
export async function recordLocal(lessonId: string, language: string, signal: AbortSignal): Promise<string> {
  const identity = session(signal)
  const stream = await navigator.mediaDevices.getUserMedia({ audio: { echoCancellation: true, noiseSuppression: true } })
  try {
    identity.check()
    const recorder = new MediaRecorder(stream)
    const chunks: Blob[] = []
    const audio = await new Promise<Blob>((resolve, reject) => {
      const cleanup = () => { clearTimeout(timer); signal.removeEventListener('abort', abort) }
      const abort = () => { cleanup(); if (recorder.state !== 'inactive') recorder.stop(); reject(new DOMException('Aborted', 'AbortError')) }
      const timer = setTimeout(() => { if (recorder.state !== 'inactive') recorder.stop() }, 4000)
      recorder.ondataavailable = e => { if (e.data.size) chunks.push(e.data) }
      recorder.onerror = () => { cleanup(); reject(new Error('麦克风录音失败，请使用文字输入')) }
      recorder.onstop = () => { cleanup(); resolve(new Blob(chunks, { type: recorder.mimeType })) }
      signal.addEventListener('abort', abort, { once: true })
      try { identity.check(); recorder.start() } catch (error) { cleanup(); reject(error) }
    })
    identity.check()
    if (!audio.size) throw new Error('未录到声音，请重试或使用文字输入')
    stream.getTracks().forEach(t => t.stop())
    const wav = await recordingToWav(audio, identity.check)
    identity.check()
    const form = new FormData()
    form.append('file', wav, 'recording.wav'); form.append('language', language)
    const res = await fetch(`/api/v1/classroom/lessons/${lessonId}/transcribe`, { method: 'POST', headers: identity.headers, body: form, signal })
    const out = await res.json()
    identity.check()
    if (!res.ok || out.code !== 0) throw new Error(out.msg || '语音识别失败，请使用文字输入')
    if (typeof out.data?.text !== 'string' || !out.data.text.trim()) throw new Error('未识别到内容，请重试或使用文字输入')
    return out.data.text
  } finally { stream.getTracks().forEach(t => t.stop()) }
}

export async function playLocalChinese(text: string, signal: AbortSignal, lessonId?: string, onPlaying?: () => void): Promise<void> {
  const identity = session(signal)
  const res = await fetch('/api/v1/speech/synthesize', { method: 'POST', headers: { ...identity.headers, 'Content-Type': 'application/json' }, body: JSON.stringify({ text, lang: 'zh', ...(lessonId ? { lesson_id: Number(lessonId) } : {}) }), signal })
  identity.check()
  if (!res.ok) throw new Error('本机语音合成不可用')
  const blob = await res.blob()
  identity.check()
  const url = URL.createObjectURL(blob)
  let audio: HTMLAudioElement | undefined
  try {
    const player = new Audio(url)
    audio = player
    await playAudio(player, signal, identity.check, onPlaying)
  } finally { audio?.pause(); URL.revokeObjectURL(url) }
}
