import { clientId } from '../interaction/client-id'
import { useEffect, useRef, useState } from 'react'
import { Alert, Button, Input, Modal, Typography } from 'antd'
import { ApiError, type FeedbackRecord } from '../api/client'

export function ClarificationReply({ record, submit }: {
  record: FeedbackRecord
  submit: (id: string, text: string, parent: string) => Promise<FeedbackRecord>
}) {
  const [open, setOpen] = useState(false)
  const [text, setText] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [uncertain, setUncertain] = useState(false)
  const draft = useRef<{ id: string; text: string } | null>(null)
  const sending = useRef(false)
  const alive = useRef(true)
  useEffect(() => { alive.current = true; return () => { alive.current = false } }, [])
  const send = async () => {
    if (sending.current || !text.trim() || !record.submission_id) return
    const normalized = text.replace(/\r\n/g, '\n').trim()
    if (!draft.current || draft.current.text !== normalized) draft.current = { id: clientId(), text: normalized }
    sending.current = true; setBusy(true); setError('')
    try {
      await submit(draft.current.id, normalized, record.submission_id)
      if (alive.current) { setOpen(false); setText(''); setUncertain(false); draft.current = null }
    } catch (e) {
      if (!alive.current) return
      setUncertain(!(e instanceof ApiError && e.code >= 400 && e.code < 500))
      setError(e instanceof ApiError && e.error_code === 'clarification_already_answered'
        ? '这条反馈已补充说明，请刷新反馈历史查看结果。'
        : e instanceof ApiError && e.error_code === 'clarification_parent_unavailable'
          ? '原反馈暂不可回复，请刷新反馈历史。' : '暂未确认是否送达，请保留正文并再次发送以查询原提交。')
    } finally { if (alive.current) { sending.current = false; setBusy(false) } }
  }
  return <>
    {record.clarification_answered ? <Typography.Text type="secondary">已补充说明</Typography.Text> : <Button onClick={() => setOpen(true)}>补充说明</Button>}
    <Modal title="补充说明" open={open} okText="发送补充说明" cancelText="取消" confirmLoading={busy}
      okButtonProps={{ disabled: !text.trim() || record.clarification_answered }} onOk={() => void send()} onCancel={() => { if (!busy) setOpen(false) }}>
      <Typography.Paragraph>回复原反馈：{record.text}</Typography.Paragraph>
      {error && <Alert type="warning" title={error} />}
      <Input.TextArea aria-label="反馈补充说明" rows={3} disabled={busy || uncertain} value={text} onChange={e => setText(e.target.value)} />
    </Modal>
  </>
}
