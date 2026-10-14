import { useEffect, useState } from 'react'
import { Alert, Button, Card, Empty, Space, Spin, Statistic, Table, Typography } from 'antd'
import { useNavigate } from 'react-router-dom'
import { api } from '../api/client'

interface LessonStats {
  lesson_id: number
  book_id: number
  finished_at: string
  total_words: number
  first_try_rate: number
}
interface StudentStats {
  learned: number
  graduated: number
  remaining: number
  placement_known: number
  finished_lessons: number
  streak_days: number
  timezone: string
  recent_lessons: LessonStats[]
}

export default function StatsPage() {
  const nav = useNavigate()
  const [stats, setStats] = useState<StudentStats | null>(null)
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(true)
  const [attempt, setAttempt] = useState(0)
  useEffect(() => {
    let disposed = false
    // oxlint-disable-next-line react/set-state-in-effect -- An external statistics retry must immediately reset loading/error before its guarded response.
    setLoading(true)
    setError('')
    api.get<StudentStats>('/student/stats').then(value => {
      if (!disposed) setStats(value)
    }).catch((cause: unknown) => {
      if (!disposed) setError(cause instanceof Error ? cause.message : '学习统计暂时不可用')
    }).finally(() => {
      if (!disposed) setLoading(false)
    })
    return () => { disposed = true }
  }, [attempt])

  return (
    <div style={{ padding: 24, maxWidth: 1100, margin: '0 auto' }}>
      <Space style={{ display: 'flex', justifyContent: 'space-between', marginBottom: 16 }}>
        <Typography.Title level={3}>学习统计</Typography.Title>
        <Button onClick={() => nav('/dashboard')}>返回课程表</Button>
      </Space>
      {loading ? <Spin tip="正在读取学习统计"><div style={{ minHeight: 100 }} /></Spin> : error ? (
        <Alert type="error" title="学习统计加载失败" description={error}
          action={<Button onClick={() => setAttempt(value => value + 1)}>重试</Button>} />
      ) : stats ? <>
        <Typography.Paragraph type="secondary">
          统计当前已选词库的学习记录，同一词在不同词库分别计数。已学包含学习中和已毕业；摸底已会单独计算。连续学习按北京时间计天，今天尚未学习时保留截至昨天的连续记录。
        </Typography.Paragraph>
        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(150px, 1fr))', gap: 12 }}>
          {[
            ['已学词数', stats.learned], ['已毕业词数', stats.graduated], ['剩余新词', stats.remaining],
            ['摸底已会', stats.placement_known], ['已完成课程', stats.finished_lessons], ['连续学习天数', stats.streak_days],
          ].map(([label, value]) => <Card key={label} aria-label={`${label}：${value}`}><Statistic title={label} value={value} /></Card>)}
        </div>
        <Typography.Title level={4}>最近课程首答率</Typography.Title>
        <Typography.Paragraph type="secondary">首答率表示第一次回答正确的比例，不包含重教后的改正。显示最近 20 节已完成课程。</Typography.Paragraph>
        {stats.recent_lessons.length === 0 ? <Empty description="还没有已完成的课程，完成第一课后即可查看记录。" /> : <Table<LessonStats>
          rowKey="lesson_id" dataSource={stats.recent_lessons} pagination={false} scroll={{ x: 500 }}
          columns={[
            { title: '课程', dataIndex: 'lesson_id', render: (id: number) => `第 ${id} 课` },
            { title: '完成时间（北京时间）', dataIndex: 'finished_at', render: (date: string) => new Date(date).toLocaleString('zh-CN', { timeZone: 'Asia/Shanghai' }) },
            { title: '本课词数', dataIndex: 'total_words' },
            { title: '首答率', dataIndex: 'first_try_rate', render: (rate: number) => `${Math.round(rate * 100)}%` },
          ]} />}
      </> : null}
    </div>
  )
}
