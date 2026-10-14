import { useCallback, useEffect, useRef, useState } from 'react'
import { api, ApiError, type FeedbackRecord } from '../api/client'
import { markFeedbackPending, pendingFeedback } from './pending'
import { useAuthStore } from '../stores/auth'

export const feedbackStatus: Record<string, string> = {
  processing: '正在处理', applied: '已应用', applied_refresh_pending: '已保存，等待配置刷新',
  partial: '部分完成', failed: '未完成', need_confirm: '需要确认', clarify: '需要补充说明', escalated: '已转交管理员',
}

export function useFeedback(userId: number, loadHistory = false) {
  const sessionToken = useAuthStore(state => state.token)
  const [records, setRecords] = useState<FeedbackRecord[]>([])
  const [pending, setPending] = useState<string[]>([])
  const [busy, setBusy] = useState<string[]>([])
  const [error, setError] = useState('')
  const generation = useRef(0)
  const recordsRef = useRef<FeedbackRecord[]>([])
  const pollStarted = useRef(new Map<string, number>())
  const busyRef = useRef(new Set<string>())
  const accept = useCallback((record: FeedbackRecord) => {
    if (localStorage.getItem('token') !== sessionToken) return
    if (record.student_id && record.student_id !== userId) return
    const id = record.submission_id
    const current = recordsRef.current
    const index = current.findIndex(r => id ? r.submission_id === id : r.id === record.id)
    if (index >= 0 && (current[index].version ?? 0) > (record.version ?? 0)) return
    if (id) {
      markFeedbackPending(userId, id, record.status === 'processing')
      setPending(pendingFeedback(userId))
    }
    const next = [...current]
    if (index < 0) next.push(record); else next[index] = record
    recordsRef.current = next
    setRecords(next)
  }, [userId, sessionToken])

  const query = useCallback(async (id: string) => {
    const epoch = generation.current
    if (localStorage.getItem('token') !== sessionToken) throw new ApiError('会话已改变', 409, 'session_changed')
    const record = await api.get<FeedbackRecord>(`/feedbacks/submissions/${encodeURIComponent(id)}`)
    if (epoch !== generation.current || localStorage.getItem('token') !== sessionToken) throw new ApiError('会话已改变', 409, 'session_changed')
    accept(record); setError('')
    return record
  }, [accept, sessionToken])

  useEffect(() => {
    const epoch = ++generation.current
    // oxlint-disable-next-line react/set-state-in-effect -- A new identity/session must synchronously clear private history and pending UI.
    recordsRef.current = []; setRecords([]); setBusy([]); busyRef.current.clear(); setError(''); pollStarted.current.clear()
    const ids = loadHistory ? pendingFeedback(userId) : []
    setPending(ids)
    if (loadHistory && userId) {
      api.get<FeedbackRecord[]>('/feedbacks/mine').then(rows => {
        if (epoch === generation.current) for (const row of rows || []) accept(row)
      }).catch(e => { if (epoch === generation.current) setError(e.message) })
      for (const id of ids) void query(id).catch(e => {
        if (epoch === generation.current) setError(e instanceof ApiError && e.code === 404 ? '反馈未送达，请重新填写并确认发送。' : '暂时无法查询反馈，请稍后查询处理进度。')
      })
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps -- Invalidate the latest asynchronous request generation, not a captured DOM node.
    return () => { generation.current++ }
  }, [userId, loadHistory, accept, query])

  useEffect(() => {
    const timers: ReturnType<typeof setTimeout>[] = []
    let disposed = false
    const epoch = generation.current
    for (const record of records) {
      const id = record.submission_id
      if (!id || record.status !== 'processing') continue
      if (!pollStarted.current.has(id)) pollStarted.current.set(id, Date.now())
      if (Date.now() - pollStarted.current.get(id)! >= 60_000) {
        setError('自动查询已暂停，请手动查询处理进度。')
        continue
      }
      const schedule = () => {
        if (disposed || epoch !== generation.current) return
        if (Date.now() - pollStarted.current.get(id)! >= 60_000) {
          setError('自动查询已暂停，请手动查询处理进度。')
          return
        }
        timers.push(setTimeout(async () => {
          try { await query(id) } catch {
            if (!disposed && epoch === generation.current) {
              setError('暂时无法查询反馈，正在自动重试；也可手动查询处理进度。')
              schedule()
            }
          }
        }, Math.max(1000, Math.min(10_000, record.retry_after_ms || 2000))))
      }
      schedule()
    }
    return () => { disposed = true; timers.forEach(clearTimeout) }
  }, [records, query])

  const submit = async (id: string, text: string, channel: 'page' | 'voice' = 'page', parentSubmissionId?: string) => {
    const epoch = generation.current
    const assertCurrent = () => {
      if (epoch !== generation.current || localStorage.getItem('token') !== sessionToken) throw new ApiError('会话已改变', 409, 'session_changed')
    }
    const refreshParent = async () => {
      assertCurrent()
      if (parentSubmissionId) { try { await query(parentSubmissionId) } catch { assertCurrent() } }
      assertCurrent()
    }
    // An uncertain previous delivery is queried before any retransmission.
    if (pendingFeedback(userId).includes(id)) {
      try {
        const previous = await query(id)
        await refreshParent()
        return previous
      } catch (e) { if (!(e instanceof ApiError && e.code === 404)) throw e }
    }
    if (epoch !== generation.current || localStorage.getItem('token') !== sessionToken) throw new Error('当前会话已改变，请重新提交反馈。')
    markFeedbackPending(userId, id, true); setPending(pendingFeedback(userId))
    try {
      const result = await api.post<FeedbackRecord>('/feedbacks', { submission_id: id, text, channel, ...(parentSubmissionId ? { parent_submission_id: parentSubmissionId } : {}) })
      assertCurrent(); accept(result)
      await refreshParent()
      return result
    } catch (e) {
      if (epoch !== generation.current || localStorage.getItem('token') !== sessionToken) throw e
      if (e instanceof ApiError && e.code === 400) {
        if (epoch === generation.current) {
          markFeedbackPending(userId, id, false); setPending(pendingFeedback(userId))
        }
        throw e
      }
      if (e instanceof ApiError && ['clarification_already_answered', 'clarification_parent_unavailable'].includes(e.error_code || '')) {
        markFeedbackPending(userId, id, false); setPending(pendingFeedback(userId))
        await refreshParent()
        throw e
      }
      if (e instanceof ApiError && e.code === 409) {
        try { await query(id) } catch { /* Preserve the original conflict. */ }
        throw e
      }
      try {
        const recovered = await query(id)
        await refreshParent()
        return recovered
      } catch { throw e }
    }
  }

  const recover = async (id: string, retry = false) => {
    if (localStorage.getItem('token') !== sessionToken) return
    if (busyRef.current.has(id)) return
    busyRef.current.add(id)
    const epoch = generation.current
    setBusy(ids => [...ids, id]); setError('')
    try {
      const record = retry ? await api.post<FeedbackRecord>(`/feedbacks/submissions/${encodeURIComponent(id)}/retry`) : await query(id)
      if (epoch === generation.current) accept(record)
    } catch (e) { if (epoch === generation.current) setError((e as Error).message) }
    finally { if (epoch === generation.current) { busyRef.current.delete(id); setBusy(ids => ids.filter(value => value !== id)) } }
  }
  return { records, pending, busy, error, accept, query, submit, recover }
}
