// The deadline includes response-body reading, not microphone capture or playback.
export async function withMediaDeadline<T>(signal: AbortSignal,
  task: (signal: AbortSignal) => Promise<T>, timeoutMs = 30000,
  context?: { lessonId?: string; operation: 'transcribe' | 'synthesize' }): Promise<T> {
  const started = performance.now()
  const controller = new AbortController()
  let rejectStop: (error: DOMException) => void = () => {}
  const stop = new Promise<never>((_, reject) => { rejectStop = reject })
  const cancel = () => {
    rejectStop(new DOMException('Aborted', 'AbortError'))
    controller.abort()
  }
  const limit = Number.isFinite(timeoutMs) && timeoutMs > 0 ? timeoutMs : 30000
  const timer = setTimeout(() => {
    rejectStop(new DOMException('语音服务响应超时，请重试或使用文字输入', 'TimeoutError'))
    controller.abort()
  }, limit)
  signal.addEventListener('abort', cancel, { once: true })
  if (signal.aborted) cancel()
  try {
    return await Promise.race([stop, (async () => {
      if (controller.signal.aborted) return await stop
      const result = await task(controller.signal)
      if (controller.signal.aborted) return await stop
      return result
    })()])
  } catch (error) {
    if (error instanceof DOMException && error.name === 'TimeoutError' && context) {
      const lesson = context.lessonId && /^\d+$/.test(context.lessonId) ? context.lessonId : 'preview'
      console.warn('[media_request_timeout]', { module: 'speech', event: 'media_request_timeout',
        trace_id: `lesson-${lesson}`, operation: context.operation, timeout_ms: limit,
        duration_ms: Math.round(performance.now() - started) })
    }
    throw error
  } finally {
    clearTimeout(timer)
    signal.removeEventListener('abort', cancel)
  }
}
