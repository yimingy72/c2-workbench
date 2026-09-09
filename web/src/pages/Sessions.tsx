import { useEffect, useState } from 'react'
import { Table, Tag, Typography, App } from 'antd'
import { useNavigate } from 'react-router-dom'
import { api, getToken } from '../api'

export type SessionRow = {
  id: string
  listener_id: string
  remote_addr: string
  hostname: string
  username: string
  os: string
  arch: string
  pid: number
  ip: string
  last_seen: number
  status: string
}

export default function Sessions() {
  const { message } = App.useApp()
  const nav = useNavigate()
  const [rows, setRows] = useState<SessionRow[]>([])

  const load = async () => {
    try {
      const data = (await api('/api/sessions')) as SessionRow[]
      setRows(Array.isArray(data) ? data : [])
    } catch (e) {
      message.error(e instanceof Error ? e.message : '加载失败')
    }
  }

  useEffect(() => {
    void load()
    const t = setInterval(() => void load(), 4000)
    const tok = getToken()
    let ws: WebSocket | null = null
    if (tok) {
      const proto = location.protocol === 'https:' ? 'wss' : 'ws'
      ws = new WebSocket(`${proto}://${location.host}/api/ws?token=${encodeURIComponent(tok)}`)
      ws.onmessage = () => {
        void load()
      }
    }
    return () => {
      clearInterval(t)
      ws?.close()
    }
  }, [])

  const color = (s: string) => (s === 'online' ? 'green' : s === 'stale' ? 'orange' : 'default')

  return (
    <div>
      <Typography.Title level={4}>主机管理</Typography.Title>
      <Table
        rowKey="id"
        dataSource={rows}
        pagination={false}
        onRow={(r) => ({ onClick: () => nav(`/sessions/${r.id}`) })}
        columns={[
          { title: 'ID', dataIndex: 'id' },
          { title: '主机名', dataIndex: 'hostname' },
          { title: '用户', dataIndex: 'username' },
          { title: '地址', dataIndex: 'remote_addr' },
          { title: '内网 IP', dataIndex: 'ip' },
          { title: 'OS', dataIndex: 'os' },
          {
            title: '状态',
            dataIndex: 'status',
            render: (s: string) => <Tag color={color(s)}>{s}</Tag>,
          },
        ]}
      />
    </div>
  )
}
