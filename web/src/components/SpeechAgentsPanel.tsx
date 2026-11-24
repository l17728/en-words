import { useEffect, useMemo, useState } from 'react'
import { Alert, Button, Card, Input, List, Modal, Select, Space, Tag, Typography, message } from 'antd'
import { api } from '../api/client'

type ModelService = { id: string; model: string; capabilities: string[] }
type Capability = { service_id: string; model: string; language: 'en' | 'zh'; voice: string }
type SpeechAgent = {
  id: string
  display_name: string
  capabilities: Capability[]
  enabled: boolean
  last_seen_at?: string
  created_at: string
  revoked_at?: string
}
type Enrollment = { enrollment_token: string; expires_in_seconds: number }

export default function SpeechAgentsPanel() {
  const [services, setServices] = useState<ModelService[]>([])
  const [agents, setAgents] = useState<SpeechAgent[]>([])
  const [serviceId, setServiceId] = useState('')
  const [model, setModel] = useState('')
  const [languages, setLanguages] = useState<Array<'en' | 'zh'>>(['en', 'zh'])
  const [voice, setVoice] = useState('default')
  const [refreshing, setRefreshing] = useState(false)
  const [issuing, setIssuing] = useState(false)
  const [busyAgent, setBusyAgent] = useState<string>()
  const [secret, setSecret] = useState<{ title: string; value: string; expires?: number }>()
  const [error, setError] = useState('')
  const ttsServices = useMemo(() => services.filter(s => s.capabilities?.includes('tts')), [services])

  const refresh = async () => {
    setRefreshing(true)
    setError('')
    try {
      const [available, registered] = await Promise.all([
        api.get<ModelService[]>('/admin/model-services'),
        api.get<SpeechAgent[]>('/admin/speech-agents'),
      ])
      setServices(available || [])
      setAgents(registered || [])
    } catch (e) { setError((e as Error).message) }
    finally { setRefreshing(false) }
  }

  useEffect(() => {
    let active = true
    Promise.all([api.get<ModelService[]>('/admin/model-services'), api.get<SpeechAgent[]>('/admin/speech-agents')])
      .then(([available, registered]) => {
        if (!active) return
        setServices(available || [])
        setAgents(registered || [])
      })
      .catch(e => { if (active) setError((e as Error).message) })
    return () => { active = false }
  }, [])

  const createEnrollment = async () => {
    if (!serviceId.trim() || !model.trim() || languages.length === 0 || !voice.trim()) return
    setIssuing(true)
    try {
      const capabilities = languages.map(language => ({ service_id: serviceId.trim(), model: model.trim(), language, voice: voice.trim() }))
      const result = await api.post<Enrollment>('/admin/speech-agents/enrollments', { ttl_seconds: 300, capabilities })
      setSecret({ title: '一次性注册令牌', value: result.enrollment_token, expires: result.expires_in_seconds })
      message.success('注册令牌已签发，请在设备上立即完成登记')
      await refresh()
    } catch (e) { message.error((e as Error).message) }
    finally { setIssuing(false) }
  }

  const rotate = async (agent: SpeechAgent) => {
    setBusyAgent(agent.id)
    try {
      const result = await api.post<{ agent_token: string }>(`/admin/speech-agents/${encodeURIComponent(agent.id)}/rotate`, {})
      setSecret({ title: `设备 ${agent.display_name} 的新令牌`, value: result.agent_token })
      message.success('旧令牌已立即失效')
      await refresh()
    } catch (e) { message.error((e as Error).message) }
    finally { setBusyAgent(undefined) }
  }

  const revoke = async (agent: SpeechAgent) => {
    setBusyAgent(agent.id)
    try {
      await api.post(`/admin/speech-agents/${encodeURIComponent(agent.id)}/revoke`, {})
      message.success('设备已撤销，在途任务已失效')
      await refresh()
    } catch (e) { message.error((e as Error).message) }
    finally { setBusyAgent(undefined) }
  }

  return <>
    <Card title="本机 TTS 驻留设备" style={{ marginBottom: 16 }} extra={<Button onClick={() => void refresh()} loading={refreshing}>刷新</Button>}>
      <Typography.Paragraph type="secondary">
        设备只接收已批准的公开词库朗读文本，通过 HTTPS 主动连接服务器；不会接收学生录音或课堂状态。注册令牌和设备令牌只显示一次。
      </Typography.Paragraph>
      {error && <Alert type="error" title={error} showIcon style={{ marginBottom: 12 }} />}
      <Space wrap style={{ marginBottom: 16 }}>
        <Select aria-label="已有 TTS 服务建议" disabled={issuing} placeholder="可从已有服务填入" allowClear onChange={key => {
          const service = ttsServices.find(s => `${s.id}~${s.model}` === key)
          if (service) { setServiceId(service.id); setModel(service.model) }
        }} style={{ width: 220 }} options={ttsServices.map(s => ({ value: `${s.id}~${s.model}`, label: `${s.id}（${s.model}）` }))} />
        <Input aria-label="TTS 服务 ID" disabled={issuing} placeholder="TTS 服务 ID" value={serviceId} onChange={e => setServiceId(e.target.value)} style={{ width: 160 }} />
        <Input aria-label="TTS 模型 ID" disabled={issuing} placeholder="TTS 模型 ID" value={model} onChange={e => setModel(e.target.value)} style={{ width: 170 }} />
        <Select aria-label="授权语言" disabled={issuing} mode="multiple" value={languages} onChange={setLanguages} style={{ minWidth: 180 }} options={[{ value: 'en', label: '英语' }, { value: 'zh', label: '中文' }]} />
        <Input aria-label="授权声线" disabled={issuing} value={voice} onChange={e => setVoice(e.target.value)} placeholder="声线" style={{ width: 140 }} />
        <Button type="primary" loading={issuing} disabled={issuing || !serviceId.trim() || !model.trim() || !languages.length || !voice.trim()} onClick={() => void createEnrollment()}>签发注册令牌</Button>
      </Space>
      {!ttsServices.length && <Alert type="info" showIcon title="尚未发现 TTS 服务。确认本机模型服务在线，且配置文件中包含 TTS 能力。" style={{ marginBottom: 12 }} />}
      <List loading={refreshing} dataSource={agents} locale={{ emptyText: '尚无驻留设备' }} renderItem={agent => <List.Item key={agent.id}
        actions={agent.enabled ? [
          <Button key="rotate" disabled={!!busyAgent} loading={busyAgent === agent.id} onClick={() => void rotate(agent)}>轮换令牌</Button>,
          <Button key="revoke" danger disabled={!!busyAgent} loading={busyAgent === agent.id} onClick={() => Modal.confirm({ title: `撤销设备 ${agent.display_name}？`, content: '撤销后该设备不能继续领取或提交任务。', okText: '撤销设备', cancelText: '取消', onOk: () => revoke(agent) })}>撤销</Button>,
        ] : []}>
        <List.Item.Meta title={<Space>{agent.display_name}<Tag color={agent.enabled ? 'green' : 'default'}>{agent.enabled ? '已启用' : '已撤销'}</Tag></Space>}
          description={<Space direction="vertical"><span>能力：{agent.capabilities.map(c => `${c.service_id}/${c.model}/${c.language}/${c.voice}`).join('、')}</span><span>最近在线：{agent.last_seen_at ? new Date(agent.last_seen_at).toLocaleString() : '尚未连接'}</span></Space>} />
      </List.Item>} />
    </Card>
    <Modal title={secret?.title} open={!!secret} footer={<Space>
      <Button onClick={() => setSecret(undefined)}>知道了</Button>
      <Button type="primary" onClick={async () => {
        try { await navigator.clipboard.writeText(secret?.value || ''); message.success('已复制令牌') }
        catch { message.error('复制失败，请手动复制令牌') }
      }}>复制令牌</Button>
    </Space>} onCancel={() => setSecret(undefined)} afterClose={() => setSecret(undefined)}>
      {secret && <><Alert type="warning" showIcon title="关闭后将无法再次查看此令牌" description={secret.expires ? `注册令牌 ${secret.expires} 秒后过期。` : '请安全保存；轮换后旧令牌已失效。'} style={{ marginBottom: 12 }} /><Input.TextArea aria-label="一次性令牌" value={secret.value} readOnly autoSize={{ minRows: 2, maxRows: 4 }} /></>}
    </Modal>
  </>
}
