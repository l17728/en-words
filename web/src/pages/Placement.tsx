import { activationKey } from '../interaction/answer-flight'
import { PlacementSave } from '../interaction/placement-save'
import { useEffect, useState, useCallback, useRef } from 'react'
import { Alert, Button, Progress, Typography, message, Modal } from 'antd'
import { useParams, useNavigate } from 'react-router-dom'
import { api } from '../api/client'

interface PWord { id: number; word: string }

// 摸底页（docs/07 §6.1：词芯片点亮/每屏100/断点续做/完成生成计划）
export default function PlacementPage() {
  const { bookId } = useParams()
  const nav = useNavigate()
  const [words, setWords] = useState<PWord[]>([])
  const [known, setKnown] = useState<Set<number>>(new Set())
  const [screen, setScreen] = useState(0)
  const [saving, setSaving] = useState(false) // R5-P0：提交锁（防连点并发覆盖草稿）
  const [loading, setLoading] = useState(true)
  const [loaded, setLoaded] = useState(false)
  const [error, setError] = useState('')
  const [attempt, setAttempt] = useState(0)
  const saveGate = useRef(new PlacementSave())
  const lifetime = useRef(0)
  const PAGE = 100

  useEffect(() => {
    const epoch = ++lifetime.current
    const controller = new AbortController()
    // oxlint-disable-next-line react/set-state-in-effect -- Loading external placement must disable edits before the request settles.
    setLoading(true); setLoaded(false); setSaving(false); saveGate.current = new PlacementSave(); setError(''); setScreen(0)
    api.get<{ words: PWord[]; knownWordIds: number[] }>(`/student/books/${bookId}/placement`, { signal: controller.signal })
      .then(r => { if (epoch === lifetime.current) { setLoaded(true); setWords(r.words || []); setKnown(new Set(r.knownWordIds || [])) } })
      .catch(e => { if (epoch === lifetime.current) setError(e.message) })
      .finally(() => { if (epoch === lifetime.current) setLoading(false) })
    // eslint-disable-next-line react-hooks/exhaustive-deps -- This monotonic generation invalidates the latest requests, not a captured DOM node.
    return () => { lifetime.current++; controller.abort() }
  }, [bookId, attempt])

  const screens = Math.max(1, Math.ceil(words.length / PAGE))
  const current = words.slice(screen * PAGE, screen * PAGE + PAGE)
  const totalKnown = known.size

  const submit = useCallback(async (finish: boolean, after?: () => void) => {
    if (!loaded || loading || saveGate.current.busy) return
    const epoch = lifetime.current
    const token = localStorage.getItem('token')
    setSaving(true); setError('')
    try {
      await saveGate.current.save(Array.from(known), finish,
        payload => api.post(`/student/books/${bookId}/placement`, payload), () => {
          if (epoch !== lifetime.current) return
          if (after) after()
          else if (finish) Modal.success({ title: '摸底完成！', content: '叮当老师已经排好你的第一节课，到时间去课程表开始学习吧。', onOk: () => { if (epoch === lifetime.current && token === localStorage.getItem('token')) nav('/dashboard') } })
          else message.success('进度已保存，随时回来继续')
        })
    } catch (cause) { if (epoch === lifetime.current) setError(cause instanceof Error ? cause.message : '保存失败，请重试') }
    finally { if (epoch === lifetime.current) setSaving(false) }
  }, [bookId, known, loading, loaded, nav])

  const goScreen = (next: number) => {
    if (next !== screen) void submit(false, () => setScreen(next))
  }
  const blocked = loading || saving || !loaded

  return (
    <div style={{ padding: 24, maxWidth: 1100, margin: '0 auto' }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 16 }}>
        <Typography.Title level={4} style={{ margin: 0 }}>
          摸底：把已经认识的点亮
        </Typography.Title>
        <Button type="link" disabled={blocked} onClick={() => void submit(false, () => nav('/books'))}>← 词库广场</Button>
      </div>
      {loading && <Typography.Paragraph role="status">正在读取摸底词条</Typography.Paragraph>}
      {error && <Alert type="error" title={error} action={!loaded ? <Button onClick={() => setAttempt(n => n + 1)}>重新读取</Button> : undefined} />}
      <Progress
        percent={Math.round(((screen + (current.length ? 1 : 0)) / screens) * 100)}
        format={() => `第 ${screen + 1}/${screens} 屏 · 已选 ${totalKnown} 词`}
        style={{ marginBottom: 20 }}
      />
      <div style={{ display: 'flex', flexWrap: 'wrap', gap: 10 }}>
        {current.map((w) => {
          const on = known.has(w.id)
          return (
            <div
              key={w.id}
              onClick={() => {
                if (blocked || saveGate.current.busy) return
                const next = new Set(known)
                if (on) next.delete(w.id)
                else next.add(w.id)
                setKnown(next)
              }}
              style={{
                padding: '12px 20px', // R4-fe#6：≥44px 触控热区
                minHeight: 44,
                borderRadius: 999,
                cursor: 'pointer',
                userSelect: 'none',
                fontSize: 18,
                background: on ? '#14B8A6' : '#FFFFFF',
                color: on ? '#fff' : '#1C2B33',
                border: on ? 'none' : '1px solid #E5EAEC',
                boxShadow: '0 2px 12px rgba(28,43,51,.06)',
                transition: 'all .15s',
              }}
              tabIndex={blocked ? -1 : 0}
              aria-disabled={blocked}
              onKeyDown={e => { if (!blocked && activationKey(e.key)) { e.preventDefault(); e.currentTarget.click() } }}
              role="button"
              aria-pressed={on}
              aria-label={`${w.word}${on ? '，已标记认识' : ''}`}
            >
              {w.word}
            </div>
          )
        })}
      </div>
      <div style={{ marginTop: 28, display: 'flex', justifyContent: 'space-between' }}>
        <Button size="large" disabled={blocked || screen === 0} onClick={() => goScreen(screen - 1)}>
          ← 上一屏
        </Button>
        <Button size="large" loading={saving} disabled={blocked} onClick={() => void submit(false, () => nav('/books'))}>
          保存并返回词库广场
        </Button>
        {screen === screens - 1 ? (
          <Button type="primary" size="large" loading={saving} disabled={blocked || !!error && words.length === 0} onClick={() => void submit(true)}>
            完成摸底，生成学习计划
          </Button>
        ) : (
          <Button type="primary" size="large" disabled={blocked} onClick={() => goScreen(screen + 1)}>
            下一屏 →
          </Button>
        )}
      </div>
    </div>
  )
}
