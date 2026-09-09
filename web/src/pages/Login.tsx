import { Button, Form, Input, Typography, Card } from 'antd'
import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { api, setToken } from '../api'

export default function Login() {
  const nav = useNavigate()
  const [err, setErr] = useState('')
  const [pending, setPending] = useState(false)

  return (
    <div style={{ minHeight: '100vh', display: 'grid', placeItems: 'center', background: '#f5f5f5' }}>
      <Card title="管理平台" style={{ width: 360 }}>
        <Form
          layout="vertical"
          onFinish={async (v: { username: string; password: string }) => {
            setErr('')
            setPending(true)
            try {
              const data = (await api('/api/login', {
                method: 'POST',
                body: JSON.stringify(v),
              })) as { token: string }
              setToken(data.token)
              nav('/listeners')
            } catch (e) {
              setErr(e instanceof Error ? e.message : '登录失败')
            } finally {
              setPending(false)
            }
          }}
        >
          <Form.Item name="username" label="用户名" rules={[{ required: true, message: '请输入用户名' }]}>
            <Input autoComplete="username" />
          </Form.Item>
          <Form.Item name="password" label="密码" rules={[{ required: true, message: '请输入密码' }]}>
            <Input.Password autoComplete="current-password" />
          </Form.Item>
          {err ? <Typography.Text type="danger">{err}</Typography.Text> : null}
          <Form.Item>
            <Button type="primary" htmlType="submit" loading={pending} block>
              登录
            </Button>
          </Form.Item>
        </Form>
      </Card>
    </div>
  )
}
