export type ContinuationIdentity = { session: number; lesson: string; epoch: string; turn: string; playback: number }
export type WaitReason = 'business_progress' | 'transport_error' | 'disconnect' | 'session_invalidated' | 'paused' | 'classroom_error' | 'lesson_end' | 'replaced'
export type WaitObservation = { event: 'waiting_started' | 'waiting_cleared'; module: 'classroom'; lesson_id: string; generation: number; session_generation: number; playback_generation: number; baseline_seq: number; reason?: WaitReason; accepted_seq?: number }
export class PlaybackContinuationWait {
  private generation = 0
  private current: { identity: ContinuationIdentity; seq: number; generation: number } | null = null
  private observe: (event: WaitObservation) => void
  constructor(observe: (event: WaitObservation) => void = () => {}) { this.observe = observe }
  private emit(event: WaitObservation['event'], reason?: WaitReason, acceptedSeq?: number) {
    const current = this.current
    if (!current) return
    const summary: WaitObservation = { event, module: 'classroom', lesson_id: current.identity.lesson,
      generation: current.generation, session_generation: current.identity.session,
      playback_generation: current.identity.playback, baseline_seq: current.seq }
    if (reason) summary.reason = reason
    if (acceptedSeq !== undefined) summary.accepted_seq = acceptedSeq
    try { this.observe(summary) } catch { /* Observation cannot alter playback or transport. */ }
  }
  begin(identity: ContinuationIdentity, seq: number): number {
    this.clear(undefined, 'replaced')
    const generation = ++this.generation
    this.current = identity.epoch && identity.turn && identity.lesson && Number.isFinite(seq) && seq >= 0
      ? { identity: { ...identity }, seq, generation } : null
    if (this.current) this.emit('waiting_started')
    return generation
  }
  clear(generation = this.generation, reason: WaitReason = 'session_invalidated', acceptedSeq?: number): boolean {
    if (generation !== this.generation) return false
    const changed = this.current !== null
    if (changed) this.emit('waiting_cleared', reason, acceptedSeq)
    this.current = null; this.generation++
    return changed
  }
  advance(frame: Record<string, any>): boolean {
    const current = this.current
    if (!current || frame.context?.epoch !== current.identity.epoch || !frame.context?.turn ||
      !Number.isFinite(frame.seq) || frame.seq <= current.seq ||
      (frame.lesson_id !== undefined && String(frame.lesson_id) !== current.identity.lesson)) return false
    const action = frame.type === 'ui_action' && ['play_tts', 'start_repeat', 'show_word', 'show_quiz_item', 'lesson_summary'].includes(frame.action?.type)
    const phase = frame.type === 'phase_changed' && ['repeating', 'wait_answer', 'show_word', 'lesson_end'].includes(frame.phase)
    return (action || phase) ? this.clear(current.generation, 'business_progress', frame.seq) : false
  }
  get ticket(): number | null { return this.current?.generation ?? null }
  get waiting(): boolean { return this.current !== null }
}
