import { useEffect, useRef, useState } from 'react'
import { Button, Descriptions, Input, Space, Typography, App } from 'antd'
import { useParams } from 'react-router-dom'
import { api, getToken } from '../api'
import type { SessionRow } from './Sessions'

type WsEvent = {
  kind: string
  session_id?: string
  task_id?: string
  data?: string
  error?: string
}

export default function SessionDetail() {
  const { id } = useParams()
  const { message } = App.useApp()
  const [info, setInfo] = useState<SessionRow | null>(null)
  const [cmd, setCmd] = useState('')
  const [log, setLog] = useState('')
  const box = useRef<HTMLPreElement>(null)

  const append = (s: string) => {
    setLog((prev) => prev + s)
  }

  useEffect(() => {
    if (!id) return
    void api(`/api/sessions/${id}`)
      .then((d) => setInfo(d as SessionRow))
      .catch((e) => message.error(e instanceof Error ? e.message : '加载失败'))
    const tok = getToken()
    if (!tok) return
    const proto = location.protocol === 'https:' ? 'wss' : 'ws'
    const ws = new WebSocket(`${proto}://${location.host}/api/ws?token=${encodeURIComponent(tok)}`)
    ws.onmessage = (ev) => {
      try {
        const e = JSON.parse(ev.data) as WsEvent
        if (e.session_id && e.session_id !== id) return
        if (e.kind === 'task.output' && e.data) append(e.data)
        if (e.kind === 'task.end') append('\n[结束]\n')
        if (e.kind === 'task.error') append(`\n[错误] ${e.error || ''}\n`)
      } catch {
        /* ignore */
      }
    }
    return () => ws.close()
  }, [id])

  useEffect(() => {
    if (box.current) box.current.scrollTop = box.current.scrollHeight
  }, [log])

  return (
    <div>
      <Typography.Title level={4}>会话 {id}</Typography.Title>
      {info ? (
        <Descriptions bordered size="small" column={2} style={{ marginBottom: 16 }}>
          <Descriptions.Item label="主机">{info.hostname}</Descriptions.Item>
          <Descriptions.Item label="用户">{info.username}</Descriptions.Item>
          <Descriptions.Item label="地址">{info.remote_addr}</Descriptions.Item>
          <Descriptions.Item label="状态">{info.status}</Descriptions.Item>
        </Descriptions>
      ) : null}
      <Space.Compact style={{ width: '100%', marginBottom: 12 }}>
        <Input
          value={cmd}
          placeholder="whoami"
          onChange={(e) => setCmd(e.target.value)}
          onPressEnter={async () => {
            if (!id || !cmd.trim()) return
            try {
              await api(`/api/sessions/${id}/exec`, {
                method: 'POST',
                body: JSON.stringify({ cmd }),
              })
              append(`\n$ ${cmd}\n`)
              setCmd('')
            } catch (e) {
              message.error(e instanceof Error ? e.message : '下发失败')
            }
          }}
        />
        <Button
          type="primary"
          onClick={async () => {
            if (!id || !cmd.trim()) return
            try {
              await api(`/api/sessions/${id}/exec`, {
                method: 'POST',
                body: JSON.stringify({ cmd }),
              })
              append(`\n$ ${cmd}\n`)
              setCmd('')
            } catch (e) {
              message.error(e instanceof Error ? e.message : '下发失败')
            }
          }}
        >
          执行
        </Button>
      </Space.Compact>
      <pre
        ref={box}
        style={{
          background: '#111',
          color: '#d9d9d9',
          padding: 12,
          minHeight: 280,
          maxHeight: 480,
          overflow: 'auto',
          borderRadius: 6,
        }}
      >
        {log || '等待输出…'}
      </pre>
    </div>
  )
}
