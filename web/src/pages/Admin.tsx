import { useEffect, useRef, useState } from 'react'
import { Alert, Button, Card, Form, Input, List, Modal, Select, Space, Typography, message } from 'antd'
import { useNavigate } from 'react-router-dom'
import { api, type Book, type FeedbackTodo, type Notification, type TeacherAnalysis } from '../api/client'
import { useAuthStore } from '../stores/auth'
import { ExclusiveAction } from '../interaction/exclusive-action'
import { LatestRead } from '../interaction/latest-read'
import LadderSettings from '../components/LadderSettings'

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
  const [analyses, setAnalyses] = useState<TeacherAnalysis[]>([])
  const [analysisBusy, setAnalysisBusy] = useState<number | null>(null)
  const [analysisError, setAnalysisError] = useState('')
  const [wordQuery, setWordQuery] = useState('')
  const [wordMatches, setWordMatches] = useState<Array<{ entry: { ID: number; Word: string; Meanings: string[] }; variants: Array<{ variant: string; language?: string }>; speech_text?: string; cache_ready?: Record<string, boolean> }>>([])
  const [wordSearchBusy, setWordSearchBusy] = useState(false)
  const [speechRefreshBusy, setSpeechRefreshBusy] = useState<string | null>(null)
  type ModelService = { id: string; model: string; capabilities: string[] }
  type ModelProfile = { id: number; name: string; text_service: string; asr_service: string; tts_zh_service: string; tts_en_service: string; active: boolean }
  const [modelServices, setModelServices] = useState<ModelService[]>([])
  const [modelProfiles, setModelProfiles] = useState<ModelProfile[]>([])
  const [profileBusy, setProfileBusy] = useState(false)
  const [profileName, setProfileName] = useState('')
  const [profileDraft, setProfileDraft] = useState({ text_service: 'vl', asr_service: 'asr', tts_zh_service: 'tts', tts_en_service: 'tts-en' })
  const [todoAttempt, setTodoAttempt] = useState(0)
  useEffect(() => {
    let disposed = false
    // oxlint-disable-next-line react/set-state-in-effect -- Refreshing external todos/notifications synchronously clears the previous request error.
    setTodoError('')
    Promise.all([api.get<FeedbackTodo[]>('/admin/todos'), api.get<Notification[]>('/admin/notifications'), api.get<TeacherAnalysis[]>('/admin/teacher-analysis')])
      .then(([items, notices, reports]) => { if (!disposed) { setTodos(items || []); setNotifications(notices || []); setAnalyses(reports || []) } })
      .catch(e => { if (!disposed) setTodoError(e.message) })
    return () => { disposed = true }
  }, [todoAttempt])
  const refresh = () => bookLoader.current.run(signal => api.get<Book[]>('/wordbooks', { signal }))
  useEffect(() => { void refresh() }, [])
  useEffect(() => {
    Promise.all([api.get<ModelService[]>('/admin/model-services'), api.get<ModelProfile[]>('/admin/model-profiles')])
      .then(([services, profiles]) => { setModelServices(services || []); setModelProfiles(profiles || []) })
      .catch(e => message.error((e as Error).message))
  }, [])
  const searchWords = async () => {
    if (!bookId || !wordQuery.trim()) return
    setWordSearchBusy(true)
    try { setWordMatches((await api.get<typeof wordMatches>(`/admin/wordbooks/${bookId}/words/search?q=${encodeURIComponent(wordQuery.trim())}`)) || []) }
    catch (e) { message.error((e as Error).message) } finally { setWordSearchBusy(false) }
  }
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
    <LadderSettings />
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
    <Card title="老师 Agent 跟读诊断" style={{ marginBottom: 16 }}>
      {analysisError && <Alert type="error" title={analysisError} />}
      <List dataSource={analyses} locale={{ emptyText: '暂无待处理诊断' }} renderItem={analysis => <List.Item key={analysis.id}
        actions={analysis.status === 'pending' ? [
          <Button key="apply" type="primary" loading={analysisBusy === analysis.id} onClick={async () => {
            setAnalysisBusy(analysis.id); setAnalysisError('')
            try { await api.post(`/admin/teacher-analysis/${analysis.id}/evaluate`, { action: 'accept_suggestion' }); setAnalyses(rows => rows.map(row => row.id === analysis.id ? { ...row, status: 'approved', implementation_status: 'approved' } : row)); message.success('已提交给老师 Agent 执行') }
            catch (e) { setAnalysisError((e as Error).message) }
            finally { setAnalysisBusy(null) }
          }}>认可并实施</Button>,
          <Button key="reject" disabled={analysisBusy === analysis.id} onClick={async () => {
            setAnalysisBusy(analysis.id); setAnalysisError('')
            try { await api.post(`/admin/teacher-analysis/${analysis.id}/evaluate`, { action: 'reject_suggestion' }); setAnalyses(rows => rows.map(row => row.id === analysis.id ? { ...row, status: 'rejected' } : row)); message.success('已记录为忽略') }
            catch (e) { setAnalysisError((e as Error).message) }
            finally { setAnalysisBusy(null) }
          }}>忽略</Button>,
        ] : []}>
        <List.Item.Meta title={`${analysis.target_text}（${analysis.failure_type}）`} description={<Space direction="vertical"><span>ASR：{analysis.transcript || '未记录'}</span><span>{analysis.diagnosis || '暂无诊断'}{analysis.suggestion ? `；建议：${analysis.suggestion}` : ''}</span></Space>} />
      </List.Item>} />
    </Card>
    <Card title="?????????" style={{ marginBottom: 16 }}>
      <Space wrap>
        <Select aria-label="????" placeholder="????" value={bookId} onChange={setBookId} style={{ width: 220 }} options={books.map(b => ({ value: b.ID, label: b.Name }))} />
        <Input aria-label="????" placeholder="??????? sth" value={wordQuery} onChange={e => setWordQuery(e.target.value)} onPressEnter={() => void searchWords()} />
        <Button loading={wordSearchBusy} disabled={!bookId || !wordQuery.trim()} onClick={() => void searchWords()}>??</Button>
      </Space>
      <List style={{ marginTop: 12 }} dataSource={wordMatches} locale={{ emptyText: '??????' }} renderItem={match => <List.Item actions={['en', 'zh'].map(lang => <Button key={lang} loading={speechRefreshBusy === `${match.entry.ID}:${lang}`} onClick={async () => {
        setSpeechRefreshBusy(`${match.entry.ID}:${lang}`)
        try { await api.post('/admin/speech/refresh', { text: match.entry.Word, lang }); message.success(`${lang === 'en' ? '??' : '??'}???????`) }
        catch (e) { message.error((e as Error).message) } finally { setSpeechRefreshBusy(null) }
      }}>????{lang === 'en' ? '??' : '??'}??</Button>)}><List.Item.Meta title={match.entry.Word} description={<Space direction="vertical"><span>?????{match.speech_text || match.entry.Word}</span><span>?????{match.variants.map(v => v.variant).join('?') || '??'}</span><span>????? {match.cache_ready?.en ? '???' : '???'}??? {match.cache_ready?.zh ? '???' : '???'}</span></Space>} /></List.Item>} />
    </Card>
    <Card title="??????" style={{ marginBottom: 16 }}>
      <Typography.Paragraph type="secondary">??????????????????????????????</Typography.Paragraph>
      <Space wrap style={{ marginBottom: 12 }}>
        <Input aria-label="??????" placeholder="????" value={profileName} onChange={e => setProfileName(e.target.value)} style={{ width: 150 }} />
        {(['text_service', 'asr_service', 'tts_zh_service', 'tts_en_service'] as const).map(role => <Select key={role} aria-label={role} value={profileDraft[role]} onChange={value => setProfileDraft(draft => ({ ...draft, [role]: value }))} style={{ width: 150 }} options={modelServices.map(service => ({ value: service.id, label: `${service.id} (${service.model})` }))} />)}
        <Button type="primary" disabled={!profileName.trim()} loading={profileBusy} onClick={async () => {
          setProfileBusy(true)
          try {
            const saved = await api.post<ModelProfile>('/admin/model-profiles', { name: profileName.trim(), ...profileDraft })
            setModelProfiles(rows => [...rows.filter(row => row.id !== saved.id), saved]); setProfileName(''); message.success('???????')
          } catch (e) { message.error((e as Error).message) } finally { setProfileBusy(false) }
        }}>????</Button>
      </Space>
      <List dataSource={modelProfiles} locale={{ emptyText: '??????' }} renderItem={profile => <List.Item key={profile.id} actions={[<Button key="apply" type={profile.active ? 'primary' : 'default'} loading={profileBusy} onClick={async () => {
        setProfileBusy(true)
        try { await api.post(`/admin/model-profiles/${profile.id}/apply`); setModelProfiles(rows => rows.map(row => ({ ...row, active: row.id === profile.id }))); message.success('???????') }
        catch (e) { message.error((e as Error).message) } finally { setProfileBusy(false) }
      }}>{profile.active ? '???' : '??'}</Button>] }><List.Item.Meta title={`${profile.name}${profile.active ? '????' : ''}`} description={`?? ${profile.text_service} ? ASR ${profile.asr_service} ? ?? ${profile.tts_zh_service} ? ?? ${profile.tts_en_service}`} /></List.Item>} />
      {modelServices.length > 0 && <Typography.Paragraph type="secondary">?????{modelServices.map(service => `${service.id}(${service.model})`).join('?')}</Typography.Paragraph>}
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
