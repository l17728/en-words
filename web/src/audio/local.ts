import { api } from '../api/client'

export interface Capabilities { local: boolean; asr: boolean; tts_zh: boolean; tts_en: boolean }
export const capabilities = () => api.get<Capabilities>('/providers')

function headers() { return { Authorization: `Bearer ${localStorage.getItem('token') || ''}` } }

// A finite capture, with cancellation releasing both recording and microphone tracks.
export async function recordLocal(lessonId: string, language: string, signal: AbortSignal): Promise<string> {
  const stream = await navigator.mediaDevices.getUserMedia({ audio: { echoCancellation: true, noiseSuppression: true } })
  if (signal.aborted) { stream.getTracks().forEach(t => t.stop()); throw new DOMException('Aborted', 'AbortError') }
  try {
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
      recorder.start()
    })
    if (signal.aborted) throw new DOMException('Aborted', 'AbortError')
    const form = new FormData()
    form.append('file', audio, 'recording.webm'); form.append('language', language)
    const res = await fetch(`/api/v1/classroom/lessons/${lessonId}/transcribe`, { method: 'POST', headers: headers(), body: form, signal })
    const out = await res.json()
    if (!res.ok || out.code !== 0) throw new Error(out.msg || '语音识别失败，请使用文字输入')
    return out.data.text
  } finally { stream.getTracks().forEach(t => t.stop()) }
}

export async function playLocalChinese(text: string, signal: AbortSignal): Promise<void> {
  const res = await fetch('/api/v1/speech/synthesize', { method: 'POST', headers: { ...headers(), 'Content-Type': 'application/json' }, body: JSON.stringify({ text, lang: 'zh' }), signal })
  if (!res.ok) throw new Error('本机语音合成不可用')
  const url = URL.createObjectURL(await res.blob())
  const audio = new Audio(url)
  try {
    await new Promise<void>((resolve, reject) => {
      const cleanup = () => { clearTimeout(timer); signal.removeEventListener('abort', abort) }
      const abort = () => { cleanup(); audio.pause(); reject(new DOMException('Aborted', 'AbortError')) }
      const timer = setTimeout(() => { cleanup(); audio.pause(); reject(new Error('语音播放超时')) }, 20000)
      audio.onended = () => { cleanup(); resolve() }
      audio.onerror = () => { cleanup(); reject(new Error('语音播放失败')) }
      signal.addEventListener('abort', abort, { once: true })
      if (signal.aborted) { abort(); return }
      audio.play().catch(e => { cleanup(); reject(e) })
    })
  } finally { audio.pause(); URL.revokeObjectURL(url) }
}
