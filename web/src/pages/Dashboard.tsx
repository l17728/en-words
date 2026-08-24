import { useEffect, useState } from 'react'
import { Card, Typography, Button, List, Badge, Progress, Input, Modal, message } from 'antd'
import { BellOutlined, BookOutlined, SoundOutlined } from '@ant-design/icons'
import { useNavigate } from 'react-router-dom'
import { api } from '../api/client'
import type { Book, Lesson, Notification } from '../api/client'
import { useAuthStore } from '../stores/auth'

// 课程表首页（docs/07 §6.2：下一课卡片/计划一览/通知/反馈入口）
export default function DashboardPage() {
  const nav = useNavigate()
  const { nickname, logout } = useAuthStore()
  const [books, setBooks] = useState<Book[]>([])
  const [lessons, setLessons] = useState<Lesson[]>([])
  const [notifs, setNotifs] = useState<Notification[]>([])
  const [fbOpen, setFbOpen] = useState(false)
  const [fbText, setFbText] = useState('')

  const refresh = () => {
    api.get<Book[]>('/wordbooks').then((bs) => setBooks(bs || [])).catch(() => {})
    api.get<Notification[]>('/student/notifications').then((ns) => setNotifs(ns || [])).catch(() => {})
  }
  useEffect(refresh, [])

  const pick = (b: Book) => {
    api.get<Lesson[]>(`/student/lessons?bookId=${b.ID}`)
      .then(setLessons)
      .catch((e) => message.error((e as Error).message))
  }

  const nextLesson = lessons.find((l) => l.Status === 'scheduled')
  const openNow = (l: Lesson) => new Date(l.OpenAt).getTime() <= Date.now()
  const fmt = (s: string) => new Date(s).toLocaleString('zh-CN', { month: 'numeric', day: 'numeric', hour: '2-digit', minute: '2-digit' })

  const sendFeedback = () => {
    api
      .post('/feedbacks', { text: fbText, channel: 'page' })
      .then(() => {
        message.success('叮当老师已收到你的反馈！')
        setFbOpen(false)
        setFbText('')
      })
      .catch((e) => message.error((e as Error).message))
  }

  return (
    <div style={{ padding: 24, maxWidth: 1100, margin: '0 auto' }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
        <Typography.Title level={3}>
          {nickname || '同学'}，今天也想见叮当老师吗 <SoundOutlined />
        </Typography.Title>
        <div>
          <Button icon={<BookOutlined />} onClick={() => nav('/books')}>词库广场</Button>
          <Button style={{ marginLeft: 8 }} icon={<BellOutlined />}
            onClick={() => api.post('/student/notifications/read').then(refresh)}>
            通知 {notifs.filter((n) => !n.Read).length ? <Badge count={notifs.filter((n) => !n.Read).length} /> : null}
          </Button>
          <Button style={{ marginLeft: 8 }} onClick={() => { logout(); nav('/login') }}>退出</Button>
        </div>
      </div>

      <Card title="我的词库（点击查看课程）" style={{ marginTop: 16 }}>
        {books.length === 0 ? (
          <Typography.Text type="secondary">还没有词库，去 <a onClick={() => nav('/books')}>词库广场</a> 选一个吧</Typography.Text>
        ) : (
          <div style={{ display: 'flex', gap: 12, flexWrap: 'wrap' }}>
            {books.map((b) => (
              <Card.Grid
                key={b.ID}
                style={{ width: '31%', cursor: 'pointer', padding: 16 }}
                hoverable
                onClick={() => pick(b)}
              >
                <b>{b.Name}</b>
                <div style={{ color: '#5C6B73', marginTop: 4 }}>{b.WordCount} 个词</div>
              </Card.Grid>
            ))}
          </div>
        )}
      </Card>

      {lessons.length > 0 && (
        <Card title="下一节课" style={{ marginTop: 16 }}>
          {nextLesson ? (
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
            percent={Math.round((lessons.filter((l) => l.Status === 'finished').length / lessons.length) * 100)}
            style={{ marginTop: 12 }}
            format={(p) => `计划进度 ${p}%`}
          />
        </Card>
      )}

      <Card title="反馈给叮当老师" style={{ marginTop: 16 }}>
        <Button type="primary" ghost onClick={() => setFbOpen(true)}>
          我想说点啥（词太多/要请假/某个词太难…）
        </Button>
      </Card>

      {notifs.length > 0 && (
        <Card title="通知" style={{ marginTop: 16 }}>
          <List
            size="small"
            dataSource={notifs.slice(-8).reverse()}
            renderItem={(n) => (
              <List.Item>
                <List.Item.Meta title={n.Title} description={n.Body} />
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
        onCancel={() => setFbOpen(false)}
      >
        <Input.TextArea
          rows={3}
          value={fbText}
          onChange={(e) => setFbText(e.target.value)}
          placeholder="例如：每次词太多了 / 这周三有事 / abandon 我老忘"
        />
      </Modal>
    </div>
  )
}
