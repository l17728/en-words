const prefix = 'feedback_pending:'

export function pendingFeedback(userId: number): string[] {
  try {
    const value: unknown = JSON.parse(sessionStorage.getItem(prefix + userId) || '[]')
    return Array.isArray(value) ? value.filter(id => typeof id === 'string' && /^[A-Za-z0-9_-]{1,128}$/.test(id)) : []
  } catch { return [] }
}

export function markFeedbackPending(userId: number, id: string, pending: boolean) {
  if (!userId || !id) return
  const ids = new Set(pendingFeedback(userId))
  if (pending) ids.add(id); else ids.delete(id)
  try { sessionStorage.setItem(prefix + userId, JSON.stringify([...ids])) } catch { /* optional recovery hint */ }
}

export function clearFeedbackPending() {
  try {
    for (let i = sessionStorage.length - 1; i >= 0; i--) {
      const key = sessionStorage.key(i)
      if (key?.startsWith(prefix)) sessionStorage.removeItem(key)
    }
  } catch { /* storage unavailable */ }
}
