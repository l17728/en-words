import { useEffect, useRef, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { Alert, Button, Card, Space, Typography } from 'antd'
import { api } from '../api/client'
import { PreflightCapture } from '../audio/duplex/capture'
import { capabilities } from '../audio/duplex/capabilities'
import type { Observation } from '../audio/duplex/capture'

type Pilot = { pilot_enabled: boolean; native_duplex: false; automatic_teaching: false; mode: 'preflight'; reasons: string[]; limits: Record<string, number> }
export default function VoicePreflight() {
  const { lessonId } = useParams()
  return <Preflight key={lessonId} lessonId={lessonId || ''} />
}
function Preflight({ lessonId }: { lessonId: string }) {
  const [pilot, setPilot] = useState<Pilot | null>(null)
  const [error, setError] = useState('')
  const [status, setStatus] = useState('未开始')
  const [values, setValues] = useState<ReturnType<typeof capabilities>>([])
  const [observation, setObservation] = useState<Observation>({ frames: 0, batches: 0 })
  const capture = useRef<PreflightCapture | null>(null)
  const lifetime = useRef(0)
  const busy = useRef(false)
  const [active, setActive] = useState(false)
  useEffect(() => {
    const controller = new AbortController(); const epoch = ++lifetime.current
    void api.get<Pilot>(`/classroom/lessons/${lessonId}/duplex-capabilities`, { signal: controller.signal })
      .then(value => { if (lifetime.current === epoch) setPilot(value) })
      .catch(() => { if (lifetime.current === epoch) setError('无法读取预检权限，请返回课堂或刷新重试') })
    // Cleanup must revoke the latest generation and capture started after this effect, not initial null.
    // oxlint-disable-next-line react-hooks/exhaustive-deps
    return () => { lifetime.current++; busy.current = false; capture.current?.stop(); capture.current = null; controller.abort() }
  }, [lessonId])
  function stop() {
    lifetime.current++; busy.current = false; capture.current?.stop(); capture.current = null
    console.info('duplex_preflight', { trace_id: `lesson-${lessonId}`, module: 'media', event: 'capture_stopped', reason: 'user' })
    setActive(false); setStatus('已停止'); setValues([]); setObservation({ frames: 0, batches: 0 })
  }
  async function start() {
    if (busy.current || !pilot?.pilot_enabled) return
    busy.current = true; setActive(true); setStatus('等待麦克风权限'); setError('')
    const epoch = ++lifetime.current; const token = localStorage.getItem('token')
    const current = () => lifetime.current === epoch && token === localStorage.getItem('token')
    const instance = new PreflightCapture(); capture.current = instance
    try {
      await instance.start(value => { if (current()) { setValues(value); console.info('duplex_preflight', { trace_id: `lesson-${lessonId}`, module: 'media', event: 'media_capabilities', capabilities: value }) } }, value => {
        if (!current()) return
        setObservation(previous => ({ ...value, event: value.event || previous.event }))
        if (value.event) console.info('duplex_preflight', { trace_id: `lesson-${lessonId}`, module: 'media', event: `vad_${value.event.type}`, frames: value.event.frames, short: value.event.short })
        if (!value.frames) { setValues([]); setStatus(value.failure ? '音频处理失败，已停止' : '麦克风已断开'); if (value.failure) setError('音频处理失败，请停止后重试或使用文字'); setActive(false); busy.current = false }
      }, current)
      if (current() && busy.current) { setStatus('正在本机预检'); console.info('duplex_preflight', { trace_id: `lesson-${lessonId}`, module: 'media', event: 'capture_started' }) }
      else instance.stop()
    } catch {
      if (current()) { busy.current = false; setActive(false); setStatus('预检未启动'); setError('无法使用麦克风或音频采集能力，请检查权限，或返回课堂使用文字') }
    }
  }
  return <main style={{ maxWidth: 720, margin: '24px auto', padding: 16 }}>
    <Typography.Title level={2}>麦克风能力预检</Typography.Title>
    <Alert type="info" showIcon title="仅本机观测，不上传音频、不识别、不判分" description="全双工与自动教学尚未启用。能量检测不证明真实人声识别或回声消除有效。" />
    {error && <Alert role="alert" type="error" title={error} />}
    {!pilot && !error && <p role="status">正在读取预检权限</p>}
    {pilot && !pilot.pilot_enabled && <Alert type="warning" title="此课堂尚未开放麦克风预检" />}
    <Space style={{ margin: '16px 0' }} wrap>
      <Button type="primary" disabled={!pilot?.pilot_enabled || active} onClick={() => void start()}>开始麦克风预检</Button>
      <Button disabled={!active} onClick={stop}>停止预检</Button>
      <Link to={`/classroom/${lessonId}`}>返回课堂，使用文字继续</Link>
    </Space>
    <Card title="实际采集状态">
      <p role="status">{status}</p>
      {values.map(value => <p key={value.key} data-testid={value.key}>
        {value.key}：请求开启；浏览器支持声明{value.supported ? '支持' : '未知'}；实际{({ enabled: '开启', disabled: '关闭', unknown: '未知' })[value.effective]}
      </p>)}
      <p>已观测20ms帧：{observation.frames}；80ms批次：{observation.batches}</p>
      <p>声音事件：{observation.event ? ({ candidate: '短起音候选', speech: '持续声音候选', endpoint: '声音结束候选', overflow: '达到上限，未提交' })[observation.event.type] : '等待声音'}</p>
    </Card>
  </main>
}
