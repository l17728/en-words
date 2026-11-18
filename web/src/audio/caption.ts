// A lease belongs to one actual media attempt, even when its text repeats.
export function shouldCancelPlaybackPhase(phase: unknown, paused = false): boolean {
  return paused || (phase !== 'tts_seg' && phase !== 'reteach_show')
}

export class PlaybackCaption {
  private generation = 0
  private text = ''
  private publish: (text: string) => void

  constructor(publish: (text: string) => void) { this.publish = publish }
  begin(): number { this.reset(); return this.generation }
  show(lease: number, text: string): void {
    if (lease !== this.generation) return
    this.update(text)
  }
  clear(lease: number): void { if (lease === this.generation) this.reset() }
  reset(): void { this.generation++; this.update('') }
  private update(text: string): void {
    if (text === this.text) return
    this.text = text
    this.publish(text)
  }
}
