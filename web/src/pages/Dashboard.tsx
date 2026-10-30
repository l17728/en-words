import { clientId } from '../interaction/client-id'
import { ExclusiveAction } from '../interaction/exclusive-action'
import { LatestRead, type ReadState } from '../interaction/latest-read'
import { FeedbackDraft } from '../interaction/feedback-draft'
import { lazy, Suspense, useEffect, useRef, useState } from 'react'
import { Alert, Card, Typography, Button, Input, Modal, message } from 'antd'
import { BellOutlined, BookOutlined, SoundOutlined, DeleteOutlined } from '@ant-design/icons'
import { useNavigate } from 'react-router-dom'
import { api, ApiError } from '../api/client'
import type { Book, Lesson, Notification } from '../api/client'
import { useAuthStore } from '../stores/auth'
import { useFeedback, feedbackStatus } from '../feedback/useFeedback'
import { isLessonOpen, watchScheduleClock } from '../schedule/clock'

const FeedbackResults = lazy(() => import('../feedback/FeedbackResults').then((module) => ({ default: module.FeedbackResults })))

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
  const draft = useRef(new FeedbackDraft())
  const [fbUncertain, setFbUncertain] = useState(false)
  const [fbError, setFbError] = useState('')
  const pageGeneration = useRef(0)
  useEffect(() => {
    // oxlint-disable-next-line react/set-state-in-effect -- Identity changes must synchronously clear private feedback drafts.
    pageGeneration.current++; draft.current.reset(); setFbText(''); setFbBusy(false); setFbUncertain(false); setFbError('')
    // eslint-disable-next-line react-hooks/exhaustive-deps -- This is a monotonic request generation, not a DOM ref; invalidate the latest requests.
    return () => { pageGeneration.current++ }
  }, [userId])
  const [report, setReport] = useState<{ total_words: number; correct_count: number; first_try_rate: number } | null>(null)

  const [bookRead, setBookRead] = useState<ReadState<Book[]>>({ status: 'loading' })
  const [noticeRead, setNoticeRead] = useState<ReadState<Notification[]>>({ status: 'loading' })
  const [lessonRead, setLessonRead] = useState<ReadState<Lesson[]>>({ status: 'loading' })
  const bookLoader = useRef(new LatestRead<Book[]>(state => { setBookRead(state); if (state.status === 'ready') setBooks(state.value || []) }))
  const noticeLoader = useRef(new LatestRead<Notification[]>(state => { setNoticeRead(state); if (state.status === 'ready') setNotifs(state.value || []) }))
  const lessonLoader = useRef(new LatestRead<Lesson[]>(state => { setLessonRead(state); if (state.status === 'ready') setLessons(state.value || []) }))
  const [reportId, setReportId] = useState<number | null>(null)
  const [reportRead, setReportRead] = useState<ReadState<typeof report>>({ status: 'loading' })
  const reportLoader = useRef(new LatestRead<typeof report>(state => { setReportRead(state); if (state.status === 'ready') setReport(state.value || null) }))
  const noticeAction = useRef(new ExclusiveAction())
  const [noticeBusy, setNoticeBusy] = useState(false)
  const [noticeError, setNoticeError] = useState('')
  const refresh = () => {
    void bookLoader.current.run(signal => api.get<Book[]>('/student/books', { signal }))
    void noticeLoader.current.run(signal => api.get<Notification[]>('/student/notifications', { signal }))
  }
  useEffect(() => {
    const books = bookLoader.current; const notices = noticeLoader.current; const reports = reportLoader.current
    refresh()
    return () => { books.dispose(); notices.dispose(); reports.dispose() }
  }, [])
  const [lessonAttempt, setLessonAttempt] = useState(0)
  const feedbackRevision = feedback.records.map(record => `${record.submission_id || record.id}:${record.version || 0}:${record.status}`).join('|')
  useEffect(() => {
    if (!selectedBook) return
    const loader = lessonLoader.current
    void loader.run(signal => api.get<Lesson[]>(`/student/lessons?bookId=${selectedBook.ID}`, { signal }))
    return () => loader.dispose()
  }, [selectedBook, feedbackRevision, userId, lessonAttempt])
  const pick = (b: Book) => {
    setLessonRead({ status: 'loading' })
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
          if (generation !== pageGeneration.current || token !== localStorage.getItem('token')) return
          message.success('已移除')
          if (selectedBook?.ID === b.ID) setSelectedBook(null)
          refresh()
        }).catch((e) => { if (generation === pageGeneration.current) message.error((e as Error).message) })
      },
    })
  }

  const showReport = (id: number) => {
    setReportId(id); setReport(null)
    void reportLoader.current.run(async signal => (await api.get<{ report: typeof report }>(`/student/lessons/${id}/report`, { signal })).report)
  }
  const markRead = () => {
    const generation = pageGeneration.current
    void noticeAction.current.run(async () => {
      setNoticeBusy(true); setNoticeError('')
      try { await api.post('/student/notifications/read'); if (generation === pageGeneration.current) refresh() }
      catch (cause) { if (generation === pageGeneration.current) setNoticeError(cause instanceof Error ? cause.message : '通知标记失败，请重试') }
      finally { if (generation === pageGeneration.current) setNoticeBusy(false) }
    })
  }

  const nextLesson = lessons.find((l) => l.Status === 'scheduled')
  const [now, setNow] = useState(Date.now)
  useEffect(() => watchScheduleClock(setNow), [])
  const openNow = (l: Lesson) => isLessonOpen(l.OpenAt, now)
  const fmt = (s: string) => new Date(s).toLocaleString('zh-CN', { month: 'numeric', day: 'numeric', hour: '2-digit', minute: '2-digit' })

  const sendFeedback = () => {
    const text = fbText.replace(/\r\n/g, '\n').trim()
    const attempt = draft.current.begin(text, () => clientId())
    if (!attempt) return
    const generation = pageGeneration.current
    setFbBusy(true); setFbError('')
    feedback.submit(attempt.id, attempt.text)
      .then(record => {
        if (generation !== pageGeneration.current || !draft.current.complete(attempt)) return
        message.info(feedbackStatus[record.status] || record.status)
        setFbOpen(false); setFbText(''); setFbUncertain(false)
        refresh()
      })
      .catch(e => {
        if (generation !== pageGeneration.current) return
        const definitive = e instanceof ApiError && e.code >= 400 && e.code < 500 && e.error_code !== 'session_changed'
        draft.current.fail(attempt, definitive)
        setFbUncertain(!definitive)
        setFbError(definitive ? e.message : '送达状态待确认，已保留原稿；请查询原提交，勿另建重复反馈。')
      })
      .finally(() => { if (generation === pageGeneration.current) setFbBusy(false) })
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
            loading={noticeBusy} disabled={noticeBusy} onClick={markRead}>
            通知（未读 {notifs.filter((n) => !n.read).length}）
          </Button>
          <Button style={{ marginLeft: 8 }} onClick={() => { logout(); nav('/login') }}>退出</Button>
        </div>
      </div>

      {noticeError && <Alert type="error" title="通知标记失败" description={noticeError} action={<Button onClick={markRead} disabled={noticeBusy}>重试标记通知</Button>} />}
      <Card title="我的词库（点击查看课程）" style={{ marginTop: 16 }}>
        {bookRead.status === 'loading' ? <Typography.Text role="status">正在读取词库</Typography.Text> : bookRead.status === 'error' ?
          <Alert type="error" title="词库读取失败" description={bookRead.error} action={<Button onClick={refresh}>重新读取词库</Button>} /> : books.length === 0 ? (
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
                  aria-label={`移除 ${b.Name}`}
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
          {lessonRead.status === 'loading' ? <Typography.Text role="status">正在读取课程</Typography.Text> : lessonRead.status === 'error' ?
            <Alert type="error" title="课程读取失败" description={lessonRead.error} action={<Button onClick={() => setLessonAttempt(n => n + 1)}>重新读取课程</Button>} /> : lessons.length === 0 ? (
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
          <label style={{ display: 'block', marginTop: 12 }}>
            计划进度 {lessons.length ? Math.round((lessons.filter((l) => l.Status === 'finished').length / lessons.length) * 100) : 0}%
            <progress
              aria-label="计划进度"
              value={lessons.length ? lessons.filter((l) => l.Status === 'finished').length : 0}
              max={lessons.length || 1}
              style={{ display: 'block', width: '100%', height: 12, marginTop: 4, accentColor: '#14B8A6' }}
            />
          </label>
        </Card>
      )}

      <Card title="反馈给叮当老师" style={{ marginTop: 16 }}>
        <Button type="primary" ghost onClick={() => setFbOpen(true)}>
          我想说点啥（词太多/要请假/某个词太难…）
        </Button>
        {feedback.error && <Alert type="error" title={feedback.error} />}
        {feedback.pending.filter(id => !feedback.records.some(record => record.submission_id === id)).map(id => <Alert key={id} type="warning" title="反馈送达状态待确认"
          action={<Button disabled={feedback.busy.includes(id)} onClick={() => void feedback.recover(id)}>查询处理进度</Button>} />)}
        <Suspense fallback={<Typography.Text role="status">正在加载反馈结果…</Typography.Text>}>
          {feedback.records.slice().reverse().map(f => <FeedbackResults key={f.submission_id || f.id} record={f}
            busy={feedback.busy.includes(f.submission_id || '')}
            onClarify={(id, text, parent) => feedback.submit(id, text, 'page', parent)} onQuery={() => void feedback.recover(f.submission_id!)} onRetry={() => void feedback.recover(f.submission_id!, true)} />)}
        </Suspense>
      </Card>

      {lessons.filter(l => l.Status === 'finished').map(l => <Button key={l.ID} style={{ marginTop: 12 }} onClick={() => showReport(l.ID)}>查看第 {l.ID} 课课报</Button>)}
      <Modal title={`学习课报 · 第 ${reportId ?? ''} 课`} open={reportId !== null} footer={null}
        onCancel={() => { reportLoader.current.dispose(); setReportId(null); setReport(null) }}>
        {reportRead.status === 'loading' ? <p role="status">正在读取课报</p> : reportRead.status === 'error' ?
          <Alert type="error" title="课报读取失败" description={reportRead.error} action={<Button onClick={() => reportId !== null && showReport(reportId)}>重新读取课报</Button>} /> :
          report && <p>本课 {report.total_words} 词，答对 {report.correct_count} 词，首次正确率 {Math.round(report.first_try_rate * 100)}%</p>}
      </Modal>

      {noticeRead.status === 'loading' && <Typography.Paragraph role="status">正在读取通知</Typography.Paragraph>}
      {noticeRead.status === 'error' && <Alert type="error" title="通知读取失败" description={noticeRead.error} action={<Button onClick={refresh}>重新读取通知</Button>} />}
      {noticeRead.status === 'ready' && notifs.length > 0 && (
        <Card title="通知" style={{ marginTop: 16 }}>
          <ul style={{ listStyle: 'none', margin: 0, padding: 0, textAlign: 'left' }}>
            {notifs.slice(-8).reverse().map((notice) => (
              <li key={notice.id} style={{ padding: '10px 0', borderBottom: '1px solid var(--border)' }}>
                <strong>{notice.title}</strong>
                <Typography.Paragraph type="secondary" style={{ margin: '4px 0 0' }}>{notice.body}</Typography.Paragraph>
              </li>
            ))}
          </ul>
        </Card>
      )}

      <Modal
        title="告诉叮当老师"
        open={fbOpen}
        okText={fbUncertain ? '查询原提交' : '发送'}
        cancelText="取消"
        onOk={sendFeedback}
        confirmLoading={fbBusy}
        okButtonProps={{ disabled: !fbText.trim() }}
        cancelButtonProps={{ disabled: fbBusy }}
        closable={!fbBusy}
        maskClosable={!fbBusy}
        keyboard={!fbBusy}
        onCancel={() => { if (!fbBusy) setFbOpen(false) }}
      >
        {fbError && <Alert type="warning" title={fbError} />}
        <Input.TextArea
          disabled={fbBusy || fbUncertain}
          rows={3}
          value={fbText}
          onChange={e => setFbText(e.target.value)}
          placeholder="例如：每次词太多了 / 这周三有事 / abandon 我老忘"
        />
      </Modal>
    </div>
  )
}
