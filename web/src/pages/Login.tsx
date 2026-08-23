import { useState } from 'react'
import { Card, Form, Input, Button, Typography, message } from 'antd'
import { SoundOutlined } from '@ant-design/icons'
import { useNavigate } from 'react-router-dom'
import { api } from '../api/client'
import type { LoginResult } from '../api/client'
import { useAuthStore } from '../stores/auth'

// 登录页（docs/07 §2：亲子气质/青绿/清爽；开场插画 splash）
export default function LoginPage() {
  const nav = useNavigate()
  const login = useAuthStore((s) => s.login)
  const [loading, setLoading] = useState(false)

  const onFinish = async (v: { username: string; password: string }) => {
    setLoading(true)
    try {
      const r = await api.post<LoginResult>('/auth/login', v)
      login(r.token, r.role, r.nickname, r.user_id)
      message.success(`欢迎回来，${r.nickname || '同学'}！`)
      nav(r.role === 'admin' ? '/books' : '/dashboard')
    } catch (e) {
      message.error((e as Error).message)
    } finally {
      setLoading(false)
    }
  }

  return (
    <div
      style={{
        minHeight: '100vh',
        background: 'linear-gradient(180deg, #F0FDFA 0%, #F7F9FA 60%)',
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
        padding: 24,
      }}
    >
      <Card style={{ width: 400, textAlign: 'center', boxShadow: '0 2px 12px rgba(28,43,51,.06)' }}>
        <div style={{ margin: '8px 0 4px' }}>
          <img src="/splash-1280x720.png" alt="叮当words" style={{ width: 200, borderRadius: 12 }} />
        </div>
        <Typography.Title level={3} style={{ marginBottom: 4 }}>
          叮当words
        </Typography.Title>
        <Typography.Text type="secondary">
          叮当老师陪你一起背单词 <SoundOutlined />
        </Typography.Text>
        <Form layout="vertical" onFinish={onFinish} style={{ marginTop: 20, textAlign: 'left' }}>
          <Form.Item name="username" label="用户名" rules={[{ required: true, message: '请输入用户名' }]}>
            <Input placeholder="学生或管理员账号" size="large" />
          </Form.Item>
          <Form.Item name="password" label="密码" rules={[{ required: true, message: '请输入密码' }]}>
            <Input.Password placeholder="密码" size="large" />
          </Form.Item>
          <Button type="primary" htmlType="submit" block loading={loading} size="large">
            开始学习
          </Button>
        </Form>
      </Card>
    </div>
  )
}
