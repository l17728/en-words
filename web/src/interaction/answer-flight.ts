type Context = { epoch: string; turn: string }
export class AnswerFlight {
  private pending: { context: Context; eventId: string } | null = null
  get busy() { return this.pending !== null }
  send(context: Context, eventId: string, dispatch: () => boolean): boolean {
    if (this.pending) return false
    this.pending = { context: { ...context }, eventId }
    try { if (dispatch()) return true } catch { /* Transport failure remains a failed send, never a REST retry. */ }
    this.pending = null; return false
  }
  reject(eventId: string): boolean {
    if (this.pending?.eventId !== eventId) return false
    this.pending = null; return true
  }
  reset() { this.pending = null }
}
export const activationKey = (key: string) => key === 'Enter' || key === ' '
