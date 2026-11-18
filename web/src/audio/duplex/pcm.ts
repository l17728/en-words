export const limits = Object.freeze({ sampleRate: 16000, frameMs: 20, batchMs: 80, preRollMs: 300, tailMs: 200, endpointMs: 600, maxUtteranceMs: 15000, candidateMs: 100 })
// Stateful linear sample-rate conversion: chunk boundaries never reset phase.
export class PCMFrames {
  private previous = 0
  private inputIndex = -1
  private next = 0
  private pending: number[] = []
  private rate: number
  constructor(rate: number) { if (!Number.isFinite(rate) || rate < 8000 || rate > 192000) throw new Error('Unsupported sample rate'); this.rate = rate }
  push(input: Float32Array): Int16Array[] {
    const frames: Int16Array[] = []
    for (const raw of input) {
      const value = Number.isFinite(raw) ? Math.max(-1, Math.min(1, raw)) : 0
      this.inputIndex++
      while (this.next <= this.inputIndex) {
        const fraction = this.inputIndex === 0 ? 1 : this.next - (this.inputIndex - 1)
        const sample = this.previous + (value - this.previous) * fraction
        this.pending.push(Math.round(sample * (sample < 0 ? 32768 : 32767)))
        if (this.pending.length === 320) { frames.push(Int16Array.from(this.pending)); this.pending = [] }
        this.next += this.rate / 16000
      }
      this.previous = value
    }
    return frames
  }
}
export type VadEvent = { type: 'candidate' | 'speech' | 'endpoint' | 'overflow'; frames: number; retainedFrames: number; short?: boolean }
// Energy detector is a preflight instrument, not a validated acoustic speech classifier.
export class RingVad {
  private ring: Int16Array[] = []
  private active = false
  private count = 0
  private voiced = 0
  private silent = 0
  private consecutive = 0
  private announced = false
  push(frame: Int16Array): VadEvent[] {
    if (frame.length !== 320) throw new Error('Expected 20ms PCM frame')
    let energy = 0; for (const x of frame) energy += (x / 32768) ** 2
    const voice = Math.sqrt(energy / frame.length) >= 0.015
    this.ring.push(frame.slice()); if (this.ring.length > 15) this.ring.shift()
    const events: VadEvent[] = []
    if (!this.active && voice) { this.active = true; this.count = 0; this.voiced = 0; this.silent = 0; this.consecutive = 0; this.announced = false; events.push({ type: 'candidate', frames: 0, retainedFrames: this.ring.length }) }
    if (!this.active) return events
    this.count++; this.silent = voice ? 0 : this.silent + 1
    if (voice) this.voiced++
    this.consecutive = voice ? this.consecutive + 1 : 0
    if (this.consecutive >= 5 && !this.announced) { this.announced = true; events.push({ type: 'speech', frames: this.count, retainedFrames: this.ring.length }) }
    if (this.count >= 750) { events.push({ type: 'overflow', frames: this.count, retainedFrames: this.ring.length }); this.reset() }
    else if (this.silent >= 30) {
      // 600ms endpoint evidence; retain only 200ms tail in a future upload.
      events.push({ type: 'endpoint', frames: this.count - 20, retainedFrames: this.ring.length, short: this.voiced < 5 }); this.reset()
    }
    return events
  }
  reset() { this.ring = []; this.active = false; this.count = 0; this.voiced = 0; this.silent = 0; this.consecutive = 0; this.announced = false }
}
