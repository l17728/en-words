import { Alert, Button, Space, Typography } from 'antd'
import { ClarificationReply } from './ClarificationReply'
import type { FeedbackRecord } from '../api/client'
import { feedbackStatus } from './useFeedback'

function DifficultySummary({ actual }: { actual: unknown }) {
  if (!actual || typeof actual !== 'object') return null
  const value = actual as { boosted_word?: unknown; evicted_words?: unknown }
  if (typeof value.boosted_word !== 'string') return null
  const evicted = Array.isArray(value.evicted_words) ? value.evicted_words.flatMap(item =>
    item && typeof item.word === 'string' ? [item.word] : []) : []
  return <Typography.Paragraph type="secondary">
    已加入重点复习：{value.boosted_word}。{evicted.length > 0 && `重点池已满，按加入顺序移出：${evicted.join('、')}。`}
  </Typography.Paragraph>
}

export function FeedbackResults({ record, busy, onQuery, onRetry, onClarify }: {
  record: FeedbackRecord; busy?: boolean; onQuery?: () => void; onRetry?: () => void; onClarify?: (id: string, text: string, parent: string) => Promise<FeedbackRecord>
}) {
  return <section aria-label={`反馈结果 ${record.submission_id || record.id}`} style={{ marginTop: 8 }}>
    <Alert type={record.status === 'failed' ? 'error' : record.status === 'partial' ? 'warning' : 'info'}
      title={record.reply || `反馈${feedbackStatus[record.status] || '尚未完成'}`} description={`处理状态：${feedbackStatus[record.status] || '尚未完成'}`} />
    {!!record.results?.length && <ul>{record.results.map(result => <li key={result.index}>
      <Typography.Text>{result.reply || (result.result_code === 'plan_budget_exceeded' ? '一次反馈的事项过多，请拆分后分别提交。' : `该项${feedbackStatus[result.status] || '尚未完成，请查询处理进度。'}`)}</Typography.Text>
      <Typography.Text type="secondary">（{feedbackStatus[result.status] || '尚未完成'}{result.refresh_pending ? '，等待配置刷新' : ''}）</Typography.Text>
      <DifficultySummary actual={result.actual} />
    </li>)}</ul>}
    {record.submission_id && onClarify && record.results?.some(result => result.status === 'clarify') && <ClarificationReply record={record} submit={onClarify} />}
    {record.submission_id && <Space style={{ marginTop: 8 }}>
      {onQuery && <Button disabled={busy} onClick={onQuery}>查询处理进度</Button>}
      {(record.retryable || record.refresh_pending) && onRetry && <Button loading={busy} onClick={onRetry}>
        {record.refresh_pending ? '重试配置刷新' : '重试未完成项'}
      </Button>}
    </Space>}
  </section>
}
