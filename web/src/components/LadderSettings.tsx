import { useEffect, useRef, useState } from 'react'
import { Alert, Button, Card, Form, Input, Modal, Select, Space, Typography } from 'antd'
import { api, ApiError } from '../api/client'
import { useAuthStore } from '../stores/auth'
import { ExclusiveAction } from '../interaction/exclusive-action'
import { ConfigReconciliation } from '../interaction/config-reconciliation'

type Source = 'student' | 'global' | 'default'
interface Field<T> { effective: T; source: Source; has_override: boolean; fallback?: T; derived_from?: string; goal_mode?: string; goal_mode_source?: Source; revision: { global: number; student: number } }
interface View { student_id: number; ladder: Field<number[]>; goalMode: Field<string> }
interface User { ID: number; Username: string; Role: string }
interface Result { committed: boolean; refresh_pending: boolean }
const facts = (v: View) => ({ ladder: v.ladder.effective, ladderRevision: v.ladder.revision, goal: v.goalMode.effective, goalRevision: v.goalMode.revision })
const names = { student: '学生覆盖', global: '全局显式设置', default: '系统默认' }

export default function LadderSettings() {
  const token = useAuthStore(s => s.token)
  const [scope, setScope] = useState<'global' | 'student'>('global')
  const [student, setStudent] = useState<number>()
  const [users, setUsers] = useState<User[]>([])
  const [data, setData] = useState<View | null>(null)
  const [ladder, setLadder] = useState('')
  const [goal, setGoal] = useState('longterm')
  const [reason, setReason] = useState('')
  const [error, setError] = useState('')
  const [notice, setNotice] = useState('')
  const [busy, setBusy] = useState(false)
  const [deleting, setDeleting] = useState<'ladder' | 'goalMode' | null>(null)
  const [refresh, setRefresh] = useState(0)
  const reconciliation = useRef(new ConfigReconciliation())
  const [reconcileVersion, setReconcileVersion] = useState(0)
  const gate = useRef(new ExclusiveAction())
  const generation = useRef(0)
  const sid = scope === 'global' ? 0 : student
  const identity = `${token}:${scope}:${sid}`
  const identityRef = useRef(identity)
  const loadedIdentity = useRef('')
  identityRef.current = identity
  useEffect(() => { identityRef.current = identity; return () => { identityRef.current = ''; generation.current++ } }, [identity])
  useEffect(() => {
    const ctl = new AbortController()
    gate.current = new ExclusiveAction()
    setUsers([]); setStudent(undefined); setData(null); setDeleting(null); setReason(''); setBusy(false)
    api.get<User[]>('/admin/users', { signal: ctl.signal }).then(rows => { if (!ctl.signal.aborted) setUsers(rows.filter(u => u.Role === 'student')) }).catch(e => { if (!ctl.signal.aborted) setError((e as Error).message) })
    return () => ctl.abort()
  }, [token])
  useEffect(() => {
    const ctl = new AbortController(); const seq = ++generation.current
    const pending = reconciliation.current.required(identity)
    if (!pending) { setData(null); setReason('') }
    setDeleting(null); setError(''); setNotice('')
    if (sid !== undefined) api.get<View>(`/config/effective?studentId=${sid}`, { signal: ctl.signal }).then(v => {
      if (ctl.signal.aborted || seq !== generation.current || identityRef.current !== identity) return
      if (v.student_id !== sid) throw new Error('配置读取目标不一致')
      loadedIdentity.current = identity
      const compared = reconciliation.current.reconcile(identity, facts(v))
      setData(v)
      if (compared) { setNotice(compared === 'changed' ? '已核对实际值和版本：配置已变化，请核对草稿后再操作。' : '已核对实际值和版本：配置未变化，可重新操作。'); setReconcileVersion(n => n + 1) }
      else { setLadder(v.ladder.effective.join(',')); setGoal(v.goalMode.effective) }
    }).catch(e => { if (!ctl.signal.aborted && seq === generation.current) setError((e as Error).message) })
    return () => { ctl.abort(); generation.current++ }
  }, [sid, identity, refresh])
  const write = (key: 'ladder' | 'goalMode', remove = false) => {
    if (reconciliation.current.required(identity)) { setError('请先刷新核对实际配置和版本'); return }
    if (sid === undefined || loadedIdentity.current !== identity || data?.student_id !== sid || (!remove && !reason.trim())) { setError('请选择目标并填写修改原因'); return }
    const captured = identity
    void gate.current.run(async () => {
      if (identityRef.current !== captured || localStorage.getItem('token') !== token) return
      setBusy(true); setError(''); setNotice('')
      let sent = false; let acknowledged = false
      try {
        let value: string | number[] = goal
        if (key === 'ladder' && !remove) {
          const parts = ladder.split(/[,，\s]+/).filter(Boolean)
          const numbers = parts.map(Number)
          if (parts.length < 3 || parts.length > 10 || parts.some(x => !/^\d+$/.test(x)) || numbers.some((x, i) => x < 1 || x > 60 || (i > 0 && x < numbers[i - 1]))) throw new Error('请输入3至10个不递减的整数天数，每项1至60')
          value = numbers
        }
        reconciliation.current.begin(captured, facts(data))
        setReconcileVersion(n => n + 1); sent = true
        const result = remove ? await api.delBody<Result>('/config/override', { studentId: sid, key }) : await api.post<Result>('/config/set', { scope, studentId: sid, key, value, reason })
        if (identityRef.current !== captured || localStorage.getItem('token') !== token) return
        acknowledged = true
        setDeleting(null)
        if (result.refresh_pending) { setNotice('已保存，读取待恢复；请刷新查看，不要重复保存。'); return }
        const v = await api.get<View>(`/config/effective?studentId=${sid}`)
        if (identityRef.current !== captured || localStorage.getItem('token') !== token) return
        if (v.student_id !== sid) throw new Error('配置读取目标不一致')
        reconciliation.current.reconcile(captured, facts(v)); setReconcileVersion(n => n + 1)
        setData(v); setLadder(v.ladder.effective.join(',')); setGoal(v.goalMode.effective); setNotice('已保存')
      } catch (e) {
        if (sent && !acknowledged && e instanceof ApiError && [400, 401, 403].includes(e.code)) reconciliation.current.rejected(captured)
        if (identityRef.current === captured) { setReconcileVersion(n => n + 1); setError(`${(e as Error).message}。请刷新核对当前值，不会自动重复提交。`) }
      } finally { if (identityRef.current === captured) setBusy(false) }
    })
  }
  void reconcileVersion
  const writeBlocked = busy || reconciliation.current.required(identity)
  const ready = loadedIdentity.current === identity && data?.student_id === sid && sid !== undefined
  return <Card title="复习梯度" data-testid="ladder-settings" style={{ marginBottom: 16 }}>
    <Form layout="vertical">
      <Form.Item label="配置范围" htmlFor="ladder-scope"><Select id="ladder-scope" value={scope} disabled={busy} onChange={setScope} options={[{ value: 'global', label: '全局' }, { value: 'student', label: '指定学生' }]} /></Form.Item>
      {scope === 'student' && <Form.Item label="目标学生" htmlFor="ladder-student"><Select id="ladder-student" value={student} disabled={busy} onChange={setStudent} options={users.map(u => ({ value: u.ID, label: `${u.Username} (${u.ID})` }))} /></Form.Item>}
      {error && <Alert type="error" title={error} />}{notice && <Alert type="info" title={notice} />}
      <Button onClick={() => setRefresh(n => n + 1)} disabled={busy}>刷新配置</Button>
      {ready && data && <>
        <Typography.Paragraph data-testid="ladder-effective">有效梯度：{data.ladder.effective.join(', ')}</Typography.Paragraph>
        <Typography.Paragraph data-testid="ladder-source">{names[data.ladder.source]}{data.ladder.derived_from && `，按${names[data.ladder.goal_mode_source || 'default']}的目标模式 ${data.ladder.goal_mode} 推导`}</Typography.Paragraph>
        <Form.Item label="修改原因" htmlFor="ladder-reason"><Input id="ladder-reason" value={reason} onChange={e => setReason(e.target.value)} disabled={busy} /></Form.Item>
        <Form.Item label="梯度天数" htmlFor="ladder-values"><Input id="ladder-values" value={ladder} onChange={e => setLadder(e.target.value)} disabled={busy} /></Form.Item>
        <Space><Button onClick={() => write('ladder')} disabled={writeBlocked}>保存梯度</Button>{data.ladder.has_override && <Button onClick={() => setDeleting('ladder')} disabled={writeBlocked}>恢复梯度继承</Button>}</Space>
        <Form.Item label="目标模式" htmlFor="ladder-goal"><Select id="ladder-goal" value={goal} onChange={setGoal} disabled={busy} options={[{ value: 'longterm', label: '长期学习' }, { value: 'exam_sprint', label: '考前冲刺' }]} /></Form.Item>
        <Typography.Paragraph>模式来源：{names[data.goalMode.source]}。{data.ladder.source !== 'default' && '当前使用显式梯度，切换模式不会替换它。'}</Typography.Paragraph>
        <Space><Button onClick={() => write('goalMode')} disabled={writeBlocked}>保存目标模式</Button>{data.goalMode.has_override && <Button onClick={() => setDeleting('goalMode')} disabled={writeBlocked}>恢复目标模式继承</Button>}</Space>
      </>}
    </Form>
    <Modal title="恢复继承配置" open={!!deleting} okText="确认恢复" confirmLoading={busy} okButtonProps={{ disabled: writeBlocked }} onCancel={() => setDeleting(null)} onOk={() => { if (deleting) write(deleting, true) }}>
      删除本学生的{deleting === 'ladder' ? '梯度' : '目标模式'}覆盖，预览回落值：{deleting && data ? JSON.stringify(data[deleting].fallback) : ''}。提交时以服务器当前继承值为准。
    </Modal>
  </Card>
}
