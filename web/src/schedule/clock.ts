export function isLessonOpen(openAt: string, now: number): boolean {
  const opening = Date.parse(openAt)
  return Number.isFinite(opening) && Number.isFinite(now) && opening <= now
}

type ClockHost = {
  now: () => number
  setInterval: (callback: () => void, milliseconds: number) => number
  clearInterval: (id: number) => void
  focusTarget: EventTarget
  visibilityTarget: EventTarget
  isVisible: () => boolean
}

// Display refresh only. The server independently authorizes entering a lesson.
export function watchScheduleClock(onTick: (now: number) => void, host: ClockHost = {
  now: Date.now,
  setInterval: (callback, milliseconds) => window.setInterval(callback, milliseconds),
  clearInterval: id => window.clearInterval(id),
  focusTarget: window,
  visibilityTarget: document,
  isVisible: () => !document.hidden,
}): () => void {
  let disposed = false
  const tick = () => { if (!disposed) onTick(host.now()) }
  const visible = () => { if (host.isVisible()) tick() }
  const timer = host.setInterval(tick, 1000)
  host.focusTarget.addEventListener('focus', tick)
  host.visibilityTarget.addEventListener('visibilitychange', visible)
  return () => {
    disposed = true
    host.clearInterval(timer)
    host.focusTarget.removeEventListener('focus', tick)
    host.visibilityTarget.removeEventListener('visibilitychange', visible)
  }
}
