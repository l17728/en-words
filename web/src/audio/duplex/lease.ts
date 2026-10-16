// Revoke authority before cleanup: synchronous media callbacks cannot revive output.
export class MediaLease {
  private generation = 0
  private cleanup: (() => void) | undefined
  begin(cleanup: () => void) { this.cancel(); this.cleanup = cleanup; return this.generation }
  current(lease: number) { return lease === this.generation && this.cleanup !== undefined }
  cancel() { const cleanup = this.cleanup; this.cleanup = undefined; this.generation++; cleanup?.() }
}
