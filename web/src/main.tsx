import React from 'react'
import ReactDOM from 'react-dom/client'
import { ConfigProvider } from 'antd'
import zhCN from 'antd/locale/zh_CN'
import App from './App'
import './index.css'

// 叮当words 学生端主题（docs/07 §4：青绿主色/44px 控件/16px 基线）
const studentTheme = {
  token: {
    colorPrimary: '#14B8A6',
    colorSuccess: '#22C55E',
    colorError: '#F87171',
    colorWarning: '#F59E0B',
    colorInfo: '#38BDF8',
    colorTextBase: '#1C2B33',
    colorBgLayout: '#F7F9FA',
    borderRadius: 8,
    borderRadiusLG: 12,
    controlHeight: 44,
    fontSize: 16,
    fontFamily: "-apple-system, 'Segoe UI', 'PingFang SC', 'Microsoft YaHei', Roboto, sans-serif",
  },
  components: {
    Button: { borderRadius: 999, controlHeight: 48, fontWeight: 600 },
    Card: { borderRadiusLG: 12 },
    Progress: { defaultColor: '#14B8A6' },
    Modal: { borderRadiusLG: 16 },
  },
}

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <ConfigProvider locale={zhCN} theme={studentTheme as never}>
      <App />
    </ConfigProvider>
  </React.StrictMode>,
)
