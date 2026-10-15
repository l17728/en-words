export type ReadState<T> = { status: 'loading' | 'ready' | 'error'; value?: T; error?: string }
export class LatestRead<T> {
  private generation = 0
  private controller: AbortController | null = null
  private notify: (state: ReadState<T>) => void
  constructor(notify: (state: ReadState<T>) => void) { this.notify = notify }
  async run(load: (signal: AbortSignal) => Promise<T>): Promise<void> {
    const generation = ++this.generation
    this.controller?.abort()
    const controller = new AbortController(); this.controller = controller
    this.notify({ status: 'loading' })
    try { const value = await load(controller.signal); if (generation === this.generation && !controller.signal.aborted) this.notify({ status: 'ready', value }) }
    catch (error) { if (generation === this.generation && !controller.signal.aborted) this.notify({ status: 'error', error: error instanceof Error ? error.message : '读取失败，请重试' }) }
  }
  dispose() { this.generation++; this.controller?.abort(); this.controller = null }
}
