import { useEffect, useRef, useState } from 'react'
import { Alert, Button, Card, Space, Typography } from 'antd'
import { useNavigate, useParams } from 'react-router-dom'
import { recordLocal } from '../audio/local'

const PHRASE = 'This is a cat.'

function matchesPhrase(text: string) {
  return /\bcat\b/i.test(text.replace(/[^a-zA-Z\s]/g, ' '))
}

export default function MicCheckPage() {
  const { lessonId } = useParams()
  const nav = useNavigate()
  const [status, setStatus] = useState<'idle' | 'recording' | 'transcribing' | 'passed' | 'failed'>('idle')
  const [transcript, setTranscript] = useState('')
  const [error, setError] = useState('')
  const controller = useRef<AbortController | null>(null)

  useEffect(() => () => controller.current?.abort(), [])

  async function check() {
    if (!lessonId || status === 'recording' || status === 'transcribing') return
    controller.current?.abort()
    const current = new AbortController()
    controller.current = current
    setStatus('recording'); setTranscript(''); setError('')
    console.info('[mic_check_started]', { module: 'media', event: 'mic_check_started', lesson_id: lessonId })
    try {
      const text = await recordLocal(lessonId, 'en', current.signal, stage => setStatus(stage === 'recording' ? 'recording' : 'transcribing'))
      if (!matchesPhrase(text)) throw new Error(`未听清目标词“cat”，识别结果：${text}`)
      setTranscript(text); setStatus('passed')
      console.info('[mic_check_passed]', { module: 'media', event: 'mic_check_passed', lesson_id: lessonId, transcript_length: text.length })
    } catch (cause) {
      if (current.signal.aborted) return
      const message = cause instanceof Error ? cause.message : '麦克风测试失败，请重试'
      setError(message); setStatus('failed')
      console.warn('[mic_check_failed]', { module: 'media', event: 'mic_check_failed', lesson_id: lessonId, reason: message })
    }
  }

  const busy = status === 'recording' || status === 'transcribing'
  return (
    <main style={{ maxWidth: 620, margin: '48px auto', padding: 24 }}>
      <Card>
        <Typography.Title level={2}>课前麦克风测试</Typography.Title>
        <Typography.Paragraph>
          请点击开始测试，然后清楚地跟读：<strong>{PHRASE}</strong>
        </Typography.Paragraph>
        <Typography.Paragraph type="secondary">测试只验证麦克风和语音识别，不会开始课程，也不会改变学习进度。</Typography.Paragraph>
        {error && <Alert role="alert" type="error" title={error} style={{ marginBottom: 16 }} />}
        {status === 'passed' && <Alert type="success" title="麦克风测试通过" description={`识别结果：${transcript}`} style={{ marginBottom: 16 }} />}
        {(status === 'recording' || status === 'transcribing') && <Typography.Paragraph role="status">{status === 'recording' ? '正在录音，请跟读句子…' : '正在识别，请稍候…'}</Typography.Paragraph>}
        <Space wrap>
          <Button type="primary" loading={busy} disabled={busy} onClick={() => void check()}>
            {status === 'failed' ? '重新测试' : '开始测试'}
          </Button>
          <Button type={status === 'passed' ? 'primary' : 'default'} disabled={status !== 'passed'} onClick={() => nav(`/classroom/${lessonId}`)}>进入课堂</Button>
          <Button onClick={() => nav('/dashboard')}>返回课程表</Button>
        </Space>
      </Card>
    </main>
  )
}
