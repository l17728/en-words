import { useEffect, useState } from 'react'
import { Button, Card, Form, Input, Select, Space, Typography, message } from 'antd'
import { useNavigate } from 'react-router-dom'
import { api, type Book } from '../api/client'
import { useAuthStore } from '../stores/auth'

export default function AdminPage() {
  const nav = useNavigate()
  const { logout } = useAuthStore()
  const [books, setBooks] = useState<Book[]>([])
  const [bookId, setBookId] = useState<number>()
  const [raw, setRaw] = useState('')
  const [busy, setBusy] = useState(false)
  const refresh = () => api.get<Book[]>('/wordbooks').then(setBooks).catch(e => message.error(e.message))
  useEffect(() => { void refresh() }, [])
  const run = async (f: () => Promise<void>) => {
    if (busy) return
    setBusy(true)
    try { await f() } catch (e) { message.error((e as Error).message) } finally { setBusy(false) }
  }
  return <main style={{ maxWidth: 900, margin: 'auto', padding: 24 }}>
    <Space><Typography.Title level={3}>叮当words 管理</Typography.Title>
      <Button onClick={() => nav('/books')}>词库广场</Button>
      <Button onClick={() => nav('/settings')}>学习设置</Button>
      <Button onClick={() => { logout(); nav('/login') }}>退出</Button></Space>
    <Card title="创建学生" style={{ marginBottom: 16 }}>
      <Form layout="vertical" onFinish={v => run(async () => {
        await api.post('/admin/users', { ...v, role: 'student' }); message.success('学生已创建')
      })}>
        <Form.Item name="username" label="学生用户名" rules={[{ required: true }]}><Input /></Form.Item>
        <Form.Item name="nickname" label="学生昵称" rules={[{ required: true }]}><Input /></Form.Item>
        <Form.Item name="password" label="初始密码" rules={[{ required: true, min: 6 }]}><Input.Password /></Form.Item>
        <Button type="primary" htmlType="submit" loading={busy}>创建学生</Button>
      </Form>
    </Card>
    <Card title="创建词库" style={{ marginBottom: 16 }}>
      <Form layout="vertical" onFinish={v => run(async () => {
        const book = await api.post<Book>('/wordbooks', v); setBookId(book.ID); await refresh(); message.success('词库已创建')
      })}>
        <Form.Item name="name" label="词库名称" rules={[{ required: true }]}><Input /></Form.Item>
        <Form.Item name="grade" label="年级" initialValue="g7"><Input /></Form.Item>
        <Button htmlType="submit" loading={busy}>创建词库</Button>
      </Form>
    </Card>
    <Card title="导入并发布">
      <Space direction="vertical" style={{ width: '100%' }}>
        <Select aria-label="选择词库" placeholder="选择词库" style={{ width: '100%' }} value={bookId} onChange={setBookId}
          options={books.map(b => ({ value: b.ID, label: `${b.Name}（${b.Status}）` }))} />
        <Input.TextArea aria-label="词条内容" rows={5} value={raw} onChange={e => setRaw(e.target.value)} placeholder={'每行一个词：英文<Tab>中文释义，例如 apple\t苹果'} />
        <Button loading={busy} disabled={!bookId || !raw.trim()} onClick={() => run(async () => {
          const entries = raw.split(/\r?\n/).filter(l => l.trim()).map(line => {
            const [word, ...meaning] = line.trim().split(/\t+/)
            if (!word || !meaning.join('').trim()) throw new Error('每行须以制表符分隔英文和中文释义')
            return { word, meanings: [meaning.join('；')] }
          })
          const result = await api.post<{Inserted: number; Skipped: string[]}>(`/wordbooks/${bookId}/import/commit`, { entries })
          message.success(`已导入 ${result.Inserted} 词，跳过 ${result.Skipped?.length || 0} 词`); await refresh()
        })}>导入词条</Button>
        <Button type="primary" loading={busy} disabled={!bookId} onClick={() => run(async () => {
          await api.post(`/wordbooks/${bookId}/publish`); await refresh(); message.success('词库已发布')
        })}>发布词库</Button>
      </Space>
    </Card>
  </main>
}
