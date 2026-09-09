import { Layout, Menu, Typography, Button } from 'antd'
import { Outlet, useLocation, useNavigate } from 'react-router-dom'
import { clearToken } from '../api'

const items = [
  { key: '/listeners', label: '监听管理' },
  { key: '/sessions', label: '主机管理' },
  { key: '/payload', label: '上线说明' },
]

export default function AppLayout() {
  const loc = useLocation()
  const nav = useNavigate()
  const selected = items.find((i) => loc.pathname.startsWith(i.key))?.key || '/listeners'

  return (
    <Layout style={{ minHeight: '100vh' }}>
      <Layout.Sider theme="dark" width={200}>
        <Typography.Title level={5} style={{ color: '#fff', padding: '16px 20px', margin: 0 }}>
          管理平台
        </Typography.Title>
        <Menu
          theme="dark"
          mode="inline"
          selectedKeys={[selected]}
          items={items}
          onClick={(e) => nav(e.key)}
        />
      </Layout.Sider>
      <Layout>
        <Layout.Header style={{ background: '#fff', display: 'flex', justifyContent: 'flex-end', alignItems: 'center' }}>
          <Button
            onClick={() => {
              clearToken()
              nav('/login')
            }}
          >
            退出
          </Button>
        </Layout.Header>
        <Layout.Content style={{ margin: 24 }}>
          <Outlet />
        </Layout.Content>
      </Layout>
    </Layout>
  )
}
