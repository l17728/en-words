import { useEffect, useState, useCallback } from 'react'
import { Button, Progress, Typography, message, Modal } from 'antd'
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
  const PAGE = 100

  useEffect(() => {
    api.get<{ words: PWord[]; knownWordIds: number[] }>(`/student/books/${bookId}/placement`)
      .then((r) => {
        setWords(r.words || [])
        // 草稿恢复：二次进入不清空已选（FR-C4 用户定案：可续选可取消）
        setKnown(new Set(r.knownWordIds || []))
      })
      .catch((e) => message.error((e as Error).message))
  }, [bookId])

  const screens = Math.max(1, Math.ceil(words.length / PAGE))
  const current = words.slice(screen * PAGE, screen * PAGE + PAGE)
  const totalKnown = known.size

  const submit = useCallback(
    (finish: boolean) => {
      api
        .post(`/student/books/${bookId}/placement`, {
          knownWordIds: Array.from(known),
          finish,
        })
        .then(() => {
          if (finish) {
            Modal.success({
              title: '摸底完成！',
              content: '叮当老师已经排好你的第一节课，到时间去课程表开始学习吧。',
              onOk: () => nav('/dashboard'),
            })
          } else {
            message.success('进度已保存，随时回来继续')
          }
        })
        .catch((e) => message.error((e as Error).message))
    },
    [bookId, known, nav],
  )

  // 自动保存：换屏时提交中间进度（FR-C4）
  const goScreen = (next: number) => {
    if (next !== screen) {
      submit(false)
      setScreen(next)
    }
  }

  return (
    <div style={{ padding: 24, maxWidth: 1100, margin: '0 auto' }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 16 }}>
        <Typography.Title level={4} style={{ margin: 0 }}>
          摸底：把已经认识的点亮
        </Typography.Title>
        <Button type="link" onClick={() => nav('/books')}>← 词库广场</Button>
      </div>
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
        <Button size="large" disabled={screen === 0} onClick={() => goScreen(screen - 1)}>
          ← 上一屏
        </Button>
        <Button size="large" onClick={() => { submit(false); nav('/books') }}>
          返回词库广场（进度已保存）
        </Button>
        {screen === screens - 1 ? (
          <Button type="primary" size="large" onClick={() => submit(true)}>
            完成摸底，生成学习计划
          </Button>
        ) : (
          <Button type="primary" size="large" onClick={() => goScreen(screen + 1)}>
            下一屏 →
          </Button>
        )}
      </div>
    </div>
  )
}
