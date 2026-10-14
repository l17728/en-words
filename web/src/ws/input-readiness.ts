export type InputContext = { epoch: string; turn: string }
export type InputTurn = InputContext & { phase: 'wait_answer' | 'repeating' }
export const sameContext = (a: InputContext | null, b: InputContext | null) =>
  !!a && !!b && !!a.epoch && !!a.turn && a.epoch === b.epoch && a.turn === b.turn

// The transport may already be closed before its onclose callback invalidates
// readiness. Interactive answers must never escape through REST in that window.
export function dispatchClassroom(send: () => boolean, fallback: () => void, requiresConnection: boolean) {
  if (!send() && !requiresConnection) fallback()
}

// A business action, never a snapshot or an open socket, grants input authority.
export class InputReadiness {
  ready: InputTurn | null = null
  private draft: InputContext | null = null

  grant(context: InputContext, phase: InputTurn['phase']): boolean {
    if (!context?.epoch || !context?.turn) return false
    const newDraft = phase === 'wait_answer' && !sameContext(this.draft, context)
    if (phase === 'wait_answer') this.draft = { ...context }
    if (!sameContext(this.ready, context) || this.ready?.phase !== phase) this.ready = { ...context, phase }
    return newDraft
  }

  revoke() { this.ready = null }
  reset() { this.revoke(); this.draft = null }

  accepts(context: InputContext, phase: string, online: boolean, capture = this.ready): boolean {
    return online && !!capture && capture === this.ready && sameContext(capture, context) && capture.phase === phase
  }

  answer(context: InputContext, online: boolean, text: string): boolean {
    return !!text.trim() && this.accepts(context, 'wait_answer', online)
  }
}
