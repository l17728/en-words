import { useEffect, useRef, useState } from 'react'
import { Alert, Card, Col, Row, Typography, Button, Tag, message, Modal, Spin } from 'antd'
import { useNavigate } from 'react-router-dom'
import { api } from '../api/client'
import type { Book } from '../api/client'
import { currentRole } from '../api/client'
import { ExclusiveAction } from '../interaction/exclusive-action'

// 词库广场（学生选库；docs/07 §6.2 风格）
export default function BooksPage() {
  const nav = useNavigate()
  const [books, setBooks] = useState<Book[]>([])
  const [enabling, setEnabling] = useState<number | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [attempt, setAttempt] = useState(0)
  const [uncertain, setUncertain] = useState<Book | null>(null)
  const [deleteUncertain, setDeleteUncertain] = useState<Book | null>(null)
  const gate = useRef(new ExclusiveAction())
  const lifetime = useRef(0)
  const ownDialog = useRef<(() => void) | null>(null)
  const isAdmin = currentRole() === 'admin'

  useEffect(() => {
    const generation = ++lifetime.current
    // oxlint-disable-next-line react-hooks/exhaustive-deps -- Invalidate the live operation and destroy the latest owned dialog, not the mount-time null value.
    return () => { if (lifetime.current === generation) lifetime.current++; ownDialog.current?.() }
  }, [])
  useEffect(() => {
    const controller = new AbortController()
    // oxlint-disable-next-line react/set-state-in-effect -- Loading is an explicit external request state.
    setLoading(true)
    setError('')
    void api.get<Book[]>('/wordbooks', { signal: controller.signal })
      .then(items => { if (!controller.signal.aborted) setBooks(items || []) })
      .catch(e => { if (!controller.signal.aborted) setError(e.message) })
      .finally(() => { if (!controller.signal.aborted) setLoading(false) })
    return () => controller.abort()
  }, [attempt])
  const refresh = () => setAttempt(n => n + 1)

  const gradeColor: Record<string, string> = {
    g7: 'green', g8: 'cyan', g9: 'blue', g10: 'geekblue',
    g11: 'purple', g12: 'magenta', gaokao: 'orange',
  }
  const gradeName: Record<string, string> = {
    g7: '初一', g8: '初二', g9: '初三', g10: '高一',
    g11: '高二', g12: '高三', gaokao: '高考',
  }

  const enable = (b: Book, confirmOnly = false) => {
    if (deleteUncertain || (uncertain && !confirmOnly)) return
    const generation = lifetime.current
    const token = localStorage.getItem('token')
    const current = () => lifetime.current === generation && localStorage.getItem('token') === token
    void gate.current.run(async () => {
      setEnabling(b.ID)
      try {
        if (confirmOnly) {
          const selected = await api.get<Book[]>('/student/books')
          if (!current()) return
          if (!(selected || []).some(item => item.ID === b.ID)) {
            setUncertain(null)
            message.info('暂未查到已选记录，请到课程表核对后再操作')
            return
          }
        } else {
          try { await api.post(`/student/books/${b.ID}/enable`) } catch (e) {
            if (current()) { setUncertain(b); message.error((e as Error).message) }
            return
          }
        }
        if (!current()) return
        setUncertain(null)
        await new Promise<void>(resolve => {
          let closed = false
          const done = () => { if (closed) return; closed = true; ownDialog.current = null; resolve() }
          const dialog = Modal.confirm({
          title: '词库已加入！',
          content: `先过一遍《${b.Name}》的词，把已经认识的点亮，剩下的交给叮当老师。`,
          okText: '开始摸底',
          cancelText: '稍后再说',
          onOk: () => { if (current()) nav(`/placement/${b.ID}`) },
          afterClose: done,
        })
          ownDialog.current = () => { dialog.destroy(); done() }
        })
      } catch (e) { if (current()) message.error((e as Error).message) }
      finally { if (current()) setEnabling(null) }
    })
  }

  const deleteBook = (b: Book, confirmOnly = false) => {
    if (uncertain || (deleteUncertain && !confirmOnly)) return
    const generation = lifetime.current
    const token = localStorage.getItem('token')
    const current = () => lifetime.current === generation && localStorage.getItem('token') === token
    void gate.current.run(async () => {
      setEnabling(b.ID)
      try {
        if (confirmOnly) {
          const items = await api.get<Book[]>('/wordbooks')
          if (!current()) return
          setBooks(items || [])
          setDeleteUncertain(null)
          message.info((items || []).some(item => item.ID === b.ID) ? '词库仍存在，请核对后再操作' : '已确认词库不存在')
          return
        }
        await new Promise<void>(resolve => {
          let closed = false
          const done = () => { if (closed) return; closed = true; ownDialog.current = null; resolve() }
          const dialog = Modal.confirm({
            title: '确认删除词库',
            content: `确定要删除「${b.Name}」吗？删除后所有学生的选课记录和课程数据将一并清除。`,
            okText: '删除', okType: 'danger', cancelText: '取消',
            onOk: async () => {
              if (!current()) return
              dialog.update({ cancelButtonProps: { disabled: true }, keyboard: false })
              try {
                await api.del(`/wordbooks/${b.ID}`)
                if (current()) { message.success('已删除'); refresh() }
              } catch (e) {
                if (current()) { setDeleteUncertain(b); message.error((e as Error).message) }
              }
            },
            afterClose: done,
          })
          ownDialog.current = () => { dialog.destroy(); done() }
        })
      } catch (e) { if (current()) message.error((e as Error).message) }
      finally { if (current()) setEnabling(null) }
    })
  }

  return (
    <div style={{ padding: 24, maxWidth: 1100, margin: '0 auto' }}>
      <Typography.Title level={3}>
        词库广场 <Button type="link" onClick={() => nav('/dashboard')}>我的课程表 →</Button>
      </Typography.Title>
      {loading && <div role="status"><Spin size="small" /> 正在加载词库…</div>}
      {error && <Alert type="error" title={error} action={<Button onClick={refresh}>重试加载词库</Button>} />}
      {uncertain && <Alert type="warning" title={`《${uncertain.Name}》的选库结果待确认`} description="请先查询已选记录，避免重复操作。"
        action={<Button loading={enabling !== null} onClick={() => enable(uncertain, true)}>查询选库结果</Button>} />}
      {deleteUncertain && <Alert type="warning" title={`《${deleteUncertain.Name}》的删除结果待确认`} description="请先查询词库状态，避免重复操作。"
        action={<Button loading={enabling !== null} onClick={() => deleteBook(deleteUncertain, true)}>查询删除结果</Button>} />}
      {!loading && !error && books.length === 0 && <p>暂无可选词库</p>}
      <Row gutter={[16, 16]}>
        {books.map((b) => (
          <Col key={b.ID} xs={24} sm={12} md={8}>
            <Card
              hoverable
              actions={[
                <Button key="select" type="primary" loading={enabling === b.ID} disabled={loading || !!error || enabling !== null || !!uncertain || !!deleteUncertain} onClick={() => enable(b)}>
                  选择这个词库
                </Button>,
                ...(isAdmin ? [
                  <Button key="delete" danger type="link" disabled={loading || !!error || enabling !== null || !!uncertain || !!deleteUncertain} onClick={() => deleteBook(b)}>删除</Button>,
                ] : []),
              ]}
            >
              <Card.Meta
                title={
                  <span>
                    {b.Name} <Tag color={gradeColor[b.Grade]}>{gradeName[b.Grade] || b.Grade}</Tag>
                  </span>
                }
                description={`${b.WordCount} 个词 · ${b.Description || '人教版'}`}
              />
            </Card>
          </Col>
        ))}
      </Row>
    </div>
  )
}
