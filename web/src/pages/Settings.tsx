import { useEffect, useRef, useState } from 'react'
import { Alert, Button, Card, Form, InputNumber, Space, Spin, Typography, message } from 'antd'
import { useNavigate } from 'react-router-dom'
import { api } from '../api/client'
import { useAuthStore } from '../stores/auth'

interface PlaybackSettings {
  ttsEnRepeat: number
  ttsZhRepeat: number
  ttsIntervalMs: number
  ttsZhIntervalMs: number
  ttsSwitchIntervalMs: number
}
interface SettingsResult {
  settings: PlaybackSettings
  scope: 'global' | 'student'
  refresh_pending: boolean
}
const gapFields = [
  ['ttsIntervalMs', '英文重复间隔'],
  ['ttsZhIntervalMs', '中文重复间隔'],
  ['ttsSwitchIntervalMs', '中英文切换间隔'],
] as const

export default function SettingsPage() {
  const role = useAuthStore(state => state.role)
  const nav = useNavigate()
  const [form] = Form.useForm<PlaybackSettings>()
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  const [refreshPending, setRefreshPending] = useState(false)
  const [attempt, setAttempt] = useState(0)
  const admin = role === 'admin'
  const path = admin ? '/admin/playback-settings' : '/student/playback-settings'
  const requestEpoch = useRef(0)
  useEffect(() => {
    requestEpoch.current++
    let disposed = false
    setLoading(true)
    setSaving(false)
    setRefreshPending(false)
    setError('')
    api.get<SettingsResult>(path).then(result => {
      if (disposed) return
      form.setFieldsValue({ ...result.settings,
        ttsIntervalMs: result.settings.ttsIntervalMs / 1000,
        ttsZhIntervalMs: result.settings.ttsZhIntervalMs / 1000,
        ttsSwitchIntervalMs: result.settings.ttsSwitchIntervalMs / 1000,
      })
      setRefreshPending(result.refresh_pending)
    }).catch((cause: unknown) => {
      if (!disposed) setError(cause instanceof Error ? cause.message : '无法读取朗读设置')
    }).finally(() => { if (!disposed) setLoading(false) })
    return () => { disposed = true; requestEpoch.current++ }
  }, [path, attempt, form])

  const save = async (values: PlaybackSettings) => {
    if (saving) return
    setSaving(true)
    setError('')
    const epoch = requestEpoch.current
    try {
      const result = await api.post<SettingsResult>(path, {
        ...values,
        ttsIntervalMs: Math.round(values.ttsIntervalMs * 1000),
        ttsZhIntervalMs: Math.round(values.ttsZhIntervalMs * 1000),
        ttsSwitchIntervalMs: Math.round(values.ttsSwitchIntervalMs * 1000),
      })
      if (epoch !== requestEpoch.current) return
      setRefreshPending(result.refresh_pending)
      if (!result.refresh_pending) message.success('朗读设置已保存，从下一课生效')
    } catch (cause) {
      if (epoch !== requestEpoch.current) return
      setError(cause instanceof Error ? cause.message : '朗读设置未保存')
    } finally { if (epoch === requestEpoch.current) setSaving(false) }
  }

  return <main style={{ padding: 24, maxWidth: 760, margin: '0 auto' }}>
    <Space style={{ display: 'flex', justifyContent: 'space-between', marginBottom: 16 }}>
      <Typography.Title level={3}>学习设置</Typography.Title>
      <Button onClick={() => nav(admin ? '/admin' : '/dashboard')}>{admin ? '返回管理页面' : '返回课程表'}</Button>
    </Space>
    <Typography.Paragraph>{admin ? '设置全体学生的默认朗读节奏，已有个人设置的学生保持其个人设置。' : '设置你自己的朗读节奏，不影响其他学生。'}修改从下一课生效，进行中的课堂保持原设置。</Typography.Paragraph>
    {refreshPending && <Alert style={{ marginBottom: 16 }} type="warning" title="设置已保存，等待运行配置恢复"
      description="数据已保存，但服务暂未完成刷新。请勿重复保存，待服务恢复后开始下一课。" />}
    {error && <Alert style={{ marginBottom: 16 }} type="error" title={error}
      action={<Button onClick={() => setAttempt(value => value + 1)} disabled={saving}>重新读取</Button>} />}
    <Spin spinning={loading}>
      <Card title="朗读次数与停顿">
        <Form form={form} layout="vertical" onFinish={save} disabled={loading || saving}>
          <Form.Item name="ttsEnRepeat" label="英文朗读遍数" rules={[{ required: true, type: 'number', min: 1, max: 5 }]}>
            <InputNumber min={1} max={5} precision={0} addonAfter="遍" />
          </Form.Item>
          <Form.Item name="ttsZhRepeat" label="中文朗读遍数" rules={[{ required: true, type: 'number', min: 1, max: 5 }]}>
            <InputNumber min={1} max={5} precision={0} addonAfter="遍" />
          </Form.Item>
          {gapFields.map(([key, label]) => <Form.Item key={key} name={key} label={label} rules={[{ required: true, type: 'number', min: 0, max: 10 }]}>
            <InputNumber min={0} max={10} step={0.1} precision={1} addonAfter="秒" />
          </Form.Item>)}
          <Typography.Paragraph type="secondary">间隔从上一段播放结束计算。英文和中文重复间隔用于同一语言的连续朗读，语言切换只使用切换间隔；合成或网络耗时可能增加等待。</Typography.Paragraph>
          <Button htmlType="submit" type="primary" loading={saving}>保存设置</Button>
        </Form>
      </Card>
    </Spin>
  </main>
}
