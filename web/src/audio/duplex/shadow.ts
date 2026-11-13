import { limits } from './pcm.ts'

export type ShadowState = 'idle' | 'capturing' | 'ended' | 'canceled' | 'overflowed'

export class ShadowUtterance {
  private state: ShadowState = 'idle'
  private frames: Int16Array[] = []
  private samples = 0
  private startedAt = 0

  start(now = performance.now()) {
    if (this.state !== 'idle') throw new Error('shadow_utterance_not_idle')
    this.state = 'capturing'; this.startedAt = now
  }

  append(frame: Int16Array, now = performance.now()) {
    if (this.state !== 'capturing') throw new Error('shadow_utterance_not_capturing')
    if (frame.length !== 320 || frame.length % 2 !== 0) throw new Error('shadow_invalid_frame')
    if (now - this.startedAt >= limits.maxUtteranceMs || this.samples + frame.length > limits.sampleRate * limits.maxUtteranceMs / 1000) {
      this.state = 'overflowed'; throw new Error('shadow_stream_overflow')
    }
    this.frames.push(frame.slice()); this.samples += frame.length
  }

  end() {
    if (this.state !== 'capturing') throw new Error('shadow_utterance_not_capturing')
    this.state = 'ended'; return encodeWav(this.frames, limits.sampleRate)
  }

  cancel() { if (this.state === 'capturing' || this.state === 'ended') this.state = 'canceled'; this.frames = []; this.samples = 0 }
  get status() { return this.state }
  get sampleCount() { return this.samples }
}

export function encodeWav(frames: Int16Array[], sampleRate: number): Blob {
  const samples = frames.reduce((total, frame) => total + frame.length, 0)
  const bytes = new ArrayBuffer(44 + samples * 2); const view = new DataView(bytes)
  const ascii = (offset: number, value: string) => [...value].forEach((char, index) => view.setUint8(offset + index, char.charCodeAt(0)))
  ascii(0, 'RIFF'); view.setUint32(4, 36 + samples * 2, true); ascii(8, 'WAVE'); ascii(12, 'fmt ')
  view.setUint32(16, 16, true); view.setUint16(20, 1, true); view.setUint16(22, 1, true)
  view.setUint32(24, sampleRate, true); view.setUint32(28, sampleRate * 2, true); view.setUint16(32, 2, true); view.setUint16(34, 16, true)
  ascii(36, 'data'); view.setUint32(40, samples * 2, true)
  let offset = 44; for (const frame of frames) for (const sample of frame) { view.setInt16(offset, sample, true); offset += 2 }
  return new Blob([bytes], { type: 'audio/wav' })
}
