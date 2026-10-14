// Resolve only after ended; playing is an observation, never a success ACK.
export function playAudio(player: HTMLAudioElement, signal: AbortSignal,
  check: () => void, onPlaying?: () => void): Promise<void> {
  return new Promise<void>((resolve, reject) => {
    let settled = false
    const cleanup = () => {
      clearTimeout(timer)
      signal.removeEventListener('abort', abort)
      player.onplaying = null; player.onended = null; player.onerror = null
    }
    const finish = (ok: boolean, error?: unknown) => {
      if (settled) return
      settled = true
      cleanup()
      if (!ok) { player.pause(); reject(error ?? new Error("Audio playback failed")) } else resolve()
    }
    const abort = () => finish(false, new DOMException('Aborted', 'AbortError'))
    const timer = setTimeout(() => finish(false, new Error('语音播放超时')), 20000)
    player.onplaying = () => {
      if (settled) return
      try { check(); onPlaying?.() } catch (error) { finish(false, error) }
    }
    player.onended = () => {
      if (settled) return
      try { check(); finish(true) } catch (error) { finish(false, error) }
    }
    player.onerror = () => finish(false, new Error('语音播放失败'))
    signal.addEventListener('abort', abort, { once: true })
    try {
      if (signal.aborted) { abort(); return }
      check()
      player.play().catch(error => finish(false, error))
    } catch (error) { finish(false, error) }
  })
}
