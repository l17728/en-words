import { useEffect, useState } from 'react'
import { Card, Col, Row, Typography, Button, Tag, message, Modal } from 'antd'
import { useNavigate } from 'react-router-dom'
import { api } from '../api/client'
import type { Book } from '../api/client'
import { currentRole } from '../api/client'

// 词库广场（学生选库；docs/07 §6.2 风格）
export default function BooksPage() {
  const nav = useNavigate()
  const [books, setBooks] = useState<Book[]>([])
  const [enabling, setEnabling] = useState<number | null>(null)
  const isAdmin = currentRole() === 'admin'

  const refresh = () => api.get<Book[]>('/wordbooks').then(setBooks).catch((e) => message.error((e as Error).message))
  useEffect(() => { refresh() }, [])

  const gradeColor: Record<string, string> = {
    g7: 'green', g8: 'cyan', g9: 'blue', g10: 'geekblue',
    g11: 'purple', g12: 'magenta', gaokao: 'orange',
  }
  const gradeName: Record<string, string> = {
    g7: '初一', g8: '初二', g9: '初三', g10: '高一',
    g11: '高二', g12: '高三', gaokao: '高考',
  }

  const enable = (b: Book) => {
    setEnabling(b.ID)
    api
      .post(`/student/books/${b.ID}/enable`)
      .then(() => {
        Modal.confirm({
          title: '词库已加入！',
          content: `先过一遍《${b.Name}》的词，把已经认识的点亮，剩下的交给叮当老师。`,
          okText: '开始摸底',
          cancelText: '稍后再说',
          onOk: () => nav(`/placement/${b.ID}`),
        })
      })
      .catch((e) => message.error((e as Error).message))
      .finally(() => setEnabling(null))
  }

  const deleteBook = (b: Book) => {
    Modal.confirm({
      title: '确认删除词库',
      content: `确定要删除「${b.Name}」吗？删除后所有学生的选课记录和课程数据将一并清除。`,
      okText: '删除',
      okType: 'danger',
      cancelText: '取消',
      onOk: () => api.del(`/wordbooks/${b.ID}`).then(() => {
        message.success('已删除')
        refresh()
      }).catch((e) => message.error((e as Error).message)),
    })
  }

  return (
    <div style={{ padding: 24, maxWidth: 1100, margin: '0 auto' }}>
      <Typography.Title level={3}>
        词库广场 <Button type="link" onClick={() => nav('/dashboard')}>我的课程表 →</Button>
      </Typography.Title>
      <Row gutter={[16, 16]}>
        {books.map((b) => (
          <Col key={b.ID} xs={24} sm={12} md={8}>
            <Card
              hoverable
              actions={[
                <Button type="primary" loading={enabling === b.ID} onClick={() => enable(b)}>
                  选择这个词库
                </Button>,
                ...(isAdmin ? [
                  <Button danger type="link" onClick={() => deleteBook(b)}>删除</Button>,
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
