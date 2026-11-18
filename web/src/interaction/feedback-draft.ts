export type Draft = { id: string; text: string }
export class FeedbackDraft {
  current: Draft | null = null
  busy = false
  uncertain = false
  begin(text: string, id: () => string): Draft | null {
    if (this.busy || !text.trim()) return null
    if (!this.uncertain && (!this.current || this.current.text !== text)) this.current = { id: id(), text }
    if (!this.current) return null
    this.busy = true
    return this.current
  }
  complete(attempt: Draft): boolean {
    if (attempt !== this.current) return false
    this.current = null; this.busy = false; this.uncertain = false; return true
  }
  fail(attempt: Draft, definitive: boolean) {
    if (attempt !== this.current) return
    this.busy = false; this.uncertain = !definitive
  }
  reset() { this.current = null; this.busy = false; this.uncertain = false }
}
