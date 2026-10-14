import { useEffect, useRef, useState } from 'react'
import { Alert, Card, Typography, Button, List, Badge, Progress, Input, Modal, message } from 'antd'
import { BellOutlined, BookOutlined, SoundOutlined, DeleteOutlined } from '@ant-design/icons'
import { useNavigate } from 'react-router-dom'
import { api } from '../api/client'
import type { Book, Lesson, Notification } from '../api/client'
import { useAuthStore } from '../stores/auth'
import { useFeedback, feedbackStatus } from '../feedback/useFeedback'
import { FeedbackResults } from '../feedback/FeedbackResults'
import { isLessonOpen, watchScheduleClock } from '../schedule/clock'

// 课程表首页（docs/07 §6.2：下一课卡片/计划一览/通知/反馈入口）
export default function DashboardPage() {
  const nav = useNavigate()
  const { nickname, logout, userId } = useAuthStore()
  const [books, setBooks] = useState<Book[]>([])
  const [lessons, setLessons] = useState<Lesson[]>([])
  const [selectedBook, setSelectedBook] = useState<Book | null>(null)
  const [notifs, setNotifs] = useState<Notification[]>([])
  const [fbOpen, setFbOpen] = useState(false)
  const [fbText, setFbText] = useState('')
  const [fbBusy, setFbBusy] = useState(false)
  const feedback = useFeedback(userId, true)
  const draft = useRef<{ id: string; text: string } | null>(null)
  const feedbackSending = useRef(false)
  const pageGeneration = useRef(0)
  useEffect(() => {
    // oxlint-disable-next-line react/set-state-in-effect -- Identity changes must synchronously clear private feedback drafts.
    pageGeneration.current++; draft.current = null; setFbText(''); setFbBusy(false); feedbackSending.current = false
    // eslint-disable-next-line react-hooks/exhaustive-deps -- This is a monotonic request generation, not a DOM ref; invalidate the latest requests.
    return () => { pageGeneration.current++ }
  }, [userId])
  const [report, setReport] = useState<{ total_words: number; correct_count: number; first_try_rate: number } | null>(null)

  const refresh = () => {
    api.get<Book[]>('/student/books').then((bs) => setBooks(bs || [])).catch(() => {})
    api.get<Notification[]>('/student/notifications').then((ns) => setNotifs(ns || [])).catch(() => {})
  }
  useEffect(refresh, [])

  const [picking, setPicking] = useState(false) // R5：防连点+清旧课表防混合
  const feedbackRevision = feedback.records.map(record => `${record.submission_id || record.id}:${record.version || 0}:${record.status}`).join('|')
  useEffect(() => {
    if (!selectedBook) return
    let disposed = false
    const generation = pageGeneration.current
    api.get<Lesson[]>(`/student/lessons?bookId=${selectedBook.ID}`)
      .then(rows => { if (!disposed && generation === pageGeneration.current) setLessons(rows || []) })
      .catch(e => { if (!disposed && generation === pageGeneration.current) message.error(`课程表刷新失败：${e.message}`) })
      .finally(() => { if (!disposed && generation === pageGeneration.current) setPicking(false) })
    return () => { disposed = true }
  }, [selectedBook, feedbackRevision, userId])
  const pick = (b: Book) => {
    if (picking) return
    setPicking(true)
    setSelectedBook({ ...b })
    setLessons([]) // 清空旧数据（防上一词库残留与新课混合）
  }

  const removeBook = (b: Book) => {
    const generation = pageGeneration.current
    const token = localStorage.getItem('token')
    Modal.confirm({
      title: '移除词库',
      content: `确定要从「我的词库」中移除「${b.Name}」吗？课程进度将被清除。`,
      okText: '移除',
      okType: 'danger',
      cancelText: '取消',
      onOk: () => {
        if (generation !== pageGeneration.current || token !== localStorage.getItem('token')) return
        return api.del(`/student/books/${b.ID}`).then(() => {
          message.success('已移除')
          if (selectedBook?.ID === b.ID) setSelectedBook(null)
          refresh()
        }).catch((e) => { if (generation === pageGeneration.current) message.error((e as Error).message) })
      },
    })
  }

  const nextLesson = lessons.find((l) => l.Status === 'scheduled')
  const [now, setNow] = useState(Date.now)
  useEffect(() => watchScheduleClock(setNow), [])
  const openNow = (l: Lesson) => isLessonOpen(l.OpenAt, now)
  const fmt = (s: string) => new Date(s).toLocaleString('zh-CN', { month: 'numeric', day: 'numeric', hour: '2-digit', minute: '2-digit' })

  const sendFeedback = () => {
    if (feedbackSending.current || !fbText.trim()) return
    const text = fbText.replace(/\r\n/g, '\n').trim()
    if (!draft.current || draft.current.text !== text) draft.current = { id: crypto.randomUUID(), text }
    const attempt = draft.current
    const generation = pageGeneration.current
    feedbackSending.current = true
    setFbBusy(true)
    feedback.submit(attempt.id, attempt.text)
      .then(record => {
        if (generation !== pageGeneration.current) return
        message.info(feedbackStatus[record.status] || record.status)
        setFbOpen(false)
        setFbText('')
        draft.current = null
        refresh()
      })
      .catch((e) => { if (generation === pageGeneration.current) message.error((e as Error).message) })
      .finally(() => { if (generation === pageGeneration.current) { feedbackSending.current = false; setFbBusy(false) } })
  }

  return (
    <div style={{ padding: 24, maxWidth: 1100, margin: '0 auto' }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
        <Typography.Title level={3}>
          {nickname || '同学'}，今天也想见叮当老师吗 <SoundOutlined />
        </Typography.Title>
        <div>
          <Button icon={<BookOutlined />} onClick={() => nav('/books')}>词库广场</Button>
		  <Button style={{ marginLeft: 8 }} onClick={() => nav('/stats')}>学习统计</Button>
          <Button style={{ marginLeft: 8 }} onClick={() => nav('/settings')}>学习设置</Button>
          <Button style={{ marginLeft: 8 }} icon={<BellOutlined />}
            onClick={() => api.post('/student/notifications/read').then(refresh)}>
            通知 {notifs.filter((n) => !n.read).length ? <Badge count={notifs.filter((n) => !n.read).length} /> : null}
          </Button>
          <Button style={{ marginLeft: 8 }} onClick={() => { logout(); nav('/login') }}>退出</Button>
        </div>
      </div>

      <Card title="我的词库（点击查看课程）" style={{ marginTop: 16 }}>
        {books.length === 0 ? (
          <Typography.Text type="secondary">还没有词库，去 <a onClick={() => nav('/books')}>词库广场</a> 选一个吧</Typography.Text>
        ) : (
          <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(220px, 1fr))', gap: 12 }}>
            {books.map((b) => (
              <div
                key={b.ID}
                role="button"
                tabIndex={0}
                aria-label={`查看 ${b.Name} 的课程`}
                onClick={() => pick(b)}
                onKeyDown={(e) => e.key === 'Enter' && pick(b)}
                style={{
                  padding: 16,
                  background: '#fff',
                  border: '1px solid #E5EAEC',
                  borderRadius: 10,
                  cursor: 'pointer',
                  minHeight: 44,
                  transition: 'all .15s',
                  position: 'relative',
                }}
                onMouseEnter={(e) => (e.currentTarget.style.borderColor = '#14B8A6')}
                onMouseLeave={(e) => (e.currentTarget.style.borderColor = '#E5EAEC')}
              >
                <b>{b.Name}</b>
                <div style={{ color: '#5C6B73', marginTop: 4 }}>{b.WordCount} 个词</div>
                <Button
                  type="text"
                  danger
                  size="small"
                  icon={<DeleteOutlined />}
                  onClick={(e) => { e.stopPropagation(); removeBook(b) }}
                  style={{ position: 'absolute', top: 8, right: 8 }}
                />
              </div>
            ))}
          </div>
        )}
      </Card>

      {selectedBook && (
        <Card
          title={`下一节课 · ${selectedBook.Name}`}
          style={{ marginTop: 16 }}
          extra={selectedBook ? (
            <Button size="small" type="link" onClick={() => nav(`/placement/${selectedBook.ID}`)}>重新摸底</Button>
          ) : null}
        >
          {lessons.length === 0 ? (
            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
              <Typography.Text type="secondary">
                这个词库还没有课程——先完成摸底，叮当老师才能为你排课表
              </Typography.Text>
              <Button type="primary" onClick={() => nav(`/placement/${selectedBook.ID}`)}>去摸底</Button>
            </div>
          ) : nextLesson ? (
            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
              <div>
                <Typography.Title level={4} style={{ margin: 0 }}>{fmt(nextLesson.OpenAt)}</Typography.Title>
                <Typography.Text type="secondary">
                  {nextLesson.BatchWordIDs?.length || 0} 个词（复习+新词）
                </Typography.Text>
              </div>
              <Button
                type="primary"
                size="large"
                disabled={!openNow(nextLesson)}
                onClick={() => nav(`/classroom/${nextLesson.ID}`)}
              >
                {openNow(nextLesson) ? '进入课堂' : '还没到时间哦'}
              </Button>
            </div>
          ) : (
            <Typography.Text type="secondary">本词库课程已全部完成 🎉</Typography.Text>
          )}
          <Progress
            percent={lessons.length ? Math.round((lessons.filter((l) => l.Status === 'finished').length / lessons.length) * 100) : 0}
            style={{ marginTop: 12 }}
            format={(p) => `计划进度 ${p}%`}
          />
        </Card>
      )}

      <Card title="反馈给叮当老师" style={{ marginTop: 16 }}>
        <Button type="primary" ghost onClick={() => setFbOpen(true)}>
          我想说点啥（词太多/要请假/某个词太难…）
        </Button>
        {feedback.error && <Alert type="error" title={feedback.error} />}
        {feedback.pending.filter(id => !feedback.records.some(record => record.submission_id === id)).map(id => <Alert key={id} type="warning" title="反馈送达状态待确认"
          action={<Button disabled={feedback.busy.includes(id)} onClick={() => void feedback.recover(id)}>查询处理进度</Button>} />)}
        {feedback.records.slice().reverse().map(f => <FeedbackResults key={f.submission_id || f.id} record={f}
          busy={feedback.busy.includes(f.submission_id || '')}
          onClarify={(id, text, parent) => feedback.submit(id, text, 'page', parent)} onQuery={() => void feedback.recover(f.submission_id!)} onRetry={() => void feedback.recover(f.submission_id!, true)} />)}
      </Card>

      {lessons.filter(l => l.Status === 'finished').map(l => <Button key={l.ID} style={{ marginTop: 12 }} onClick={() => {
        api.get<{report: typeof report}>(`/student/lessons/${l.ID}/report`).then(r => setReport(r.report)).catch(e => message.error(e.message))
      }}>查看第 {l.ID} 课课报</Button>)}
      <Modal title="学习课报" open={!!report} footer={null} onCancel={() => setReport(null)}>
        {report && <p>本课 {report.total_words} 词，答对 {report.correct_count} 词，首次正确率 {Math.round(report.first_try_rate * 100)}%</p>}
      </Modal>

      {notifs.length > 0 && (
        <Card title="通知" style={{ marginTop: 16 }}>
          <List
            size="small"
            dataSource={notifs.slice(-8).reverse()}
            renderItem={(n) => (
              <List.Item>
                <List.Item.Meta title={n.title} description={n.body} />
              </List.Item>
            )}
          />
        </Card>
      )}

      <Modal
        title="告诉叮当老师"
        open={fbOpen}
        okText="发送"
        cancelText="取消"
        onOk={sendFeedback}
        confirmLoading={fbBusy}
        okButtonProps={{ disabled: !fbText.trim() }}
        onCancel={() => setFbOpen(false)}
      >
        <Input.TextArea
          rows={3}
          value={fbText}
          onChange={(e) => { setFbText(e.target.value); if (draft.current?.text !== e.target.value.replace(/\r\n/g, '\n').trim()) draft.current = null }}
          placeholder="例如：每次词太多了 / 这周三有事 / abandon 我老忘"
        />
      </Modal>
    </div>
  )
}
