// The lock covers the entire user operation, including any confirmation dialog.
// Acquire synchronously: React state alone cannot reject two clicks in one render.
export class ExclusiveAction {
  private active = false

  async run(task: () => Promise<void>): Promise<boolean> {
    if (this.active) return false
    this.active = true
    try { await task(); return true } finally { this.active = false }
  }
}
