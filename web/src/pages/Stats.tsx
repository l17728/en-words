import { Typography } from 'antd'

// 学习统计页（骨架：真实数据接入后填充）
export default function StatsPage() {
  return (
    <div style={{ padding: 24, maxWidth: 1100, margin: '0 auto' }}>
      <Typography.Title level={3}>学习统计</Typography.Title>
      <Typography.Text type="secondary">
        已学/已毕业/剩余词数、正确率趋势、连续学习天数——数据接入后展示。
      </Typography.Text>
    </div>
  )
}
