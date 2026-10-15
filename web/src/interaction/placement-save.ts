export class PlacementSave {
  busy = false
  async save(ids: number[], finish: boolean, persist: (payload: { knownWordIds: number[]; finish: boolean }) => Promise<unknown>, committed: () => void): Promise<boolean> {
    if (this.busy) return false
    this.busy = true
    const payload = { knownWordIds: [...ids], finish }
    try { await persist(payload); committed(); return true } finally { this.busy = false }
  }
}
