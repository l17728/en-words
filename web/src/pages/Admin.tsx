import { useEffect, useRef, useState } from 'react'
import { Alert, Button, Card, Form, Input, List, Modal, Select, Space, Typography, message } from 'antd'
import { useNavigate } from 'react-router-dom'
import { api, type Book, type FeedbackTodo, type Notification } from '../api/client'
import { useAuthStore } from '../stores/auth'
import { ExclusiveAction } from '../interaction/exclusive-action'
import { LatestRead } from '../interaction/latest-read'

export default function AdminPage() {
  const nav = useNavigate()
  const { logout } = useAuthStore()
  const [books, setBooks] = useState<Book[]>([])
  const [bookId, setBookId] = useState<number>()
  const [raw, setRaw] = useState('')
  const [busy, setBusy] = useState<'student' | 'book' | 'import' | 'publish' | null>(null)
  const gate = useRef(new ExclusiveAction())
  const bookLoader = useRef(new LatestRead<Book[]>(state => {
    if (state.status === 'ready') setBooks(state.value || [])
    if (state.status === 'error') message.error(state.error)
  }))
  const lifetime = useRef(new AbortController())
  useEffect(() => {
    const controller = new AbortController()
    lifetime.current = controller
    const reader = bookLoader.current
    return () => { controller.abort(); reader.dispose() }
  }, [])
  const [todos, setTodos] = useState<FeedbackTodo[]>([])
  const [notifications, setNotifications] = useState<Notification[]>([])
  const [todoError, setTodoError] = useState('')
  const [selectedTodo, setSelectedTodo] = useState<FeedbackTodo | null>(null)
  const [todoAttempt, setTodoAttempt] = useState(0)
  useEffect(() => {
    let disposed = false
    // oxlint-disable-next-line react/set-state-in-effect -- Refreshing external todos/notifications synchronously clears the previous request error.
    setTodoError('')
    Promise.all([api.get<FeedbackTodo[]>('/admin/todos'), api.get<Notification[]>('/admin/notifications')])
      .then(([items, notices]) => { if (!disposed) { setTodos(items || []); setNotifications(notices || []) } })
      .catch(e => { if (!disposed) setTodoError(e.message) })
    return () => { disposed = true }
  }, [todoAttempt])
  const refresh = () => bookLoader.current.run(signal => api.get<Book[]>('/wordbooks', { signal }))
  useEffect(() => { void refresh() }, [])
  const run = (action: NonNullable<typeof busy>, f: (current: () => boolean) => Promise<void>) => {
    const controller = lifetime.current
    const token = localStorage.getItem('token')
    const current = () => !controller.signal.aborted && token === localStorage.getItem('token')
    void gate.current.run(async () => {
      if (!current()) return
      setBusy(action)
      try { await f(current) } catch (e) { if (current()) message.error((e as Error).message) }
      finally { if (current()) setBusy(null) }
    })
  }
  return <main style={{ maxWidth: 900, margin: 'auto', padding: 24 }}>
    <Space><Typography.Title level={3}>叮当words 管理</Typography.Title>
      <Button onClick={() => nav('/books')}>词库广场</Button>
      <Button onClick={() => nav('/settings')}>学习设置</Button>
      <Button onClick={() => { logout(); nav('/login') }}>退出</Button></Space>
    <Card title="学生反馈待办" style={{ marginBottom: 16 }} extra={<Button onClick={() => setTodoAttempt(n => n + 1)}>刷新待办</Button>}>
      {todoError && <Alert type="error" title={todoError} />}
      <List dataSource={todos} locale={{ emptyText: '暂无反馈待办' }} renderItem={todo => <List.Item key={todo.id}
        actions={[<Button key="detail" onClick={() => setSelectedTodo(todo)}>查看反馈详情</Button>]}>
        <List.Item.Meta title={`学生 ${todo.student_id} · ${todo.legacy ? '历史待办' : '待管理员查看'}`} description={todo.text} />
      </List.Item>} />
    </Card>
    <Card title="管理员通知" style={{ marginBottom: 16 }}>
      <List dataSource={notifications} locale={{ emptyText: '暂无通知' }} renderItem={notice => <List.Item key={notice.id}>
        <List.Item.Meta title={notice.title} description={notice.body} />
      </List.Item>} />
    </Card>
    <Modal title="反馈待办详情" open={!!selectedTodo} footer={null} onCancel={() => setSelectedTodo(null)}>
      {selectedTodo && <><p>学生：{selectedTodo.student_id}</p><p>{selectedTodo.text}</p>
        <p>{selectedTodo.reply || '已转交管理员，尚未处理。'}</p></>}
    </Modal>
    <Card title="创建学生" style={{ marginBottom: 16 }}>
      <Form layout="vertical" disabled={!!busy} onFinish={v => run('student', async current => {
        await api.post('/admin/users', { ...v, role: 'student' })
        if (current()) message.success('学生已创建')
      })}>
        <Form.Item name="username" label="学生用户名" rules={[{ required: true }]}><Input /></Form.Item>
        <Form.Item name="nickname" label="学生昵称" rules={[{ required: true }]}><Input /></Form.Item>
        <Form.Item name="password" label="初始密码" rules={[{ required: true, min: 6 }]}><Input.Password /></Form.Item>
        <Button type="primary" htmlType="submit" loading={busy === 'student'}>创建学生</Button>
      </Form>
    </Card>
    <Card title="创建词库" style={{ marginBottom: 16 }}>
      <Form layout="vertical" disabled={!!busy} onFinish={v => run('book', async current => {
        const book = await api.post<Book>('/wordbooks', v)
        if (!current()) return
        setBookId(book.ID); await refresh()
        if (current()) message.success('词库已创建')
      })}>
        <Form.Item name="name" label="词库名称" rules={[{ required: true }]}><Input /></Form.Item>
        <Form.Item name="grade" label="年级" initialValue="g7"><Input /></Form.Item>
        <Button htmlType="submit" loading={busy === 'book'}>创建词库</Button>
      </Form>
    </Card>
    <Card title="导入并发布">
      <Space direction="vertical" style={{ width: '100%' }}>
        <Select disabled={!!busy} aria-label="选择词库" placeholder="选择词库" style={{ width: '100%' }} value={bookId} onChange={setBookId}
          options={books.map(b => ({ value: b.ID, label: `${b.Name}（${b.Status}）` }))} />
        <Input.TextArea disabled={!!busy} aria-label="词条内容" rows={5} value={raw} onChange={e => setRaw(e.target.value)} placeholder={'每行一个词：英文<Tab>中文释义，例如 apple\t苹果'} />
        <Button loading={busy === 'import'} disabled={!!busy || !bookId || !raw.trim()} onClick={() => run('import', async current => {
          const entries = raw.split(/\r?\n/).filter(l => l.trim()).map(line => {
            const [word, ...meaning] = line.trim().split(/\t+/)
            if (!word || !meaning.join('').trim()) throw new Error('每行须以制表符分隔英文和中文释义')
            return { word, meanings: [meaning.join('；')] }
          })
          const result = await api.post<{Inserted: number; Skipped: string[]}>(`/wordbooks/${bookId}/import/commit`, { entries })
          if (!current()) return
          message.success(`已导入 ${result.Inserted} 词，跳过 ${result.Skipped?.length || 0} 词`); await refresh()
        })}>导入词条</Button>
        <Button type="primary" loading={busy === 'publish'} disabled={!!busy || !bookId} onClick={() => run('publish', async current => {
          await api.post(`/wordbooks/${bookId}/publish`)
          if (!current()) return
          await refresh()
          if (current()) message.success('词库已发布')
        })}>发布词库</Button>
      </Space>
    </Card>
  </main>
}
