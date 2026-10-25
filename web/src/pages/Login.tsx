import { Button, Form, Input, Select, Typography } from 'antd'
import { useEffect, useState } from 'react'
import { useLocation, useNavigate } from 'react-router-dom'
import { api, setToken } from '../api'
import { THEME_OPTIONS } from '../theme/palettes'
import { useTheme } from '../theme/ThemeRoot'
import styles from './Login.module.css'

export default function Login() {
  const nav = useNavigate()
  const loc = useLocation()
  const { id, palette, setTheme } = useTheme()
  const [err, setErr] = useState('')
  const [pending, setPending] = useState(false)

  useEffect(() => {
    document.title = `登录 · ${palette.label}`
  }, [palette.label])

  return (
    <div className={styles.page}>
      <section className={styles.brand} aria-hidden>
        <div className={styles.logo}>C2</div>
        <h1 className={styles.product}>管理平台</h1>
        <p className={styles.tag}>授权环境控制台</p>
      </section>
      <section className={styles.panel}>
        <Select
          className={styles.theme}
          aria-label="配色"
          value={id}
          options={THEME_OPTIONS}
          onChange={(v) => setTheme(v)}
        />
        <div className={styles.card}>
          <h2 className={styles.title}>登录</h2>
          <p className={styles.sub}>使用配置文件中的操作员账户</p>
          <Form
            layout="vertical"
            initialValues={{ username: localStorage.getItem('c2.username') || '' }}
            onFinish={async (v: { username: string; password: string }) => {
              setErr('')
              setPending(true)
              try {
                const data = (await api('/api/login', {
                  method: 'POST',
                  body: JSON.stringify(v),
                })) as { token: string }
                setToken(data.token)
                localStorage.setItem('c2.username', v.username)
                nav({ pathname: '/listeners', search: loc.search })
              } catch (e) {
                setErr(e instanceof Error ? e.message : '登录失败')
              } finally {
                setPending(false)
              }
            }}
          >
            <Form.Item name="username" label="用户名" rules={[{ required: true, message: '请输入用户名' }]}>
              <Input autoComplete="username" size="large" />
            </Form.Item>
            <Form.Item name="password" label="密码" rules={[{ required: true, message: '请输入密码' }]}>
              <Input.Password autoComplete="current-password" size="large" />
            </Form.Item>
            {err ? <Typography.Text type="danger">{err}</Typography.Text> : null}
            <Form.Item>
              <Button type="primary" htmlType="submit" loading={pending} block size="large">
                登录
              </Button>
            </Form.Item>
          </Form>
        </div>
      </section>
    </div>
  )
}
