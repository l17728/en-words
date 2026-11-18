// A write is unsafe to repeat until its own target has been read again.
export class ConfigReconciliation {
  private pending = new Map<string, string>()
  begin(identity: string, before: unknown) { this.pending.set(identity, JSON.stringify(before)) }
  required(identity: string) { return this.pending.has(identity) }
  rejected(identity: string) { this.pending.delete(identity) }
  reconcile(identity: string, actual: unknown): 'changed' | 'unchanged' | null {
    const before = this.pending.get(identity)
    if (before === undefined) return null
    this.pending.delete(identity)
    return before === JSON.stringify(actual) ? 'unchanged' : 'changed'
  }
}
