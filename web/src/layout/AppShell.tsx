import { ApiOutlined, DesktopOutlined, DownloadOutlined, LogoutOutlined } from '@ant-design/icons'
import { Button, Select } from 'antd'
import { useEffect, useState } from 'react'
import { Outlet, useLocation, useNavigate } from 'react-router-dom'
import { api, clearToken } from '../api'
import { THEME_OPTIONS } from '../theme/palettes'
import { useTheme } from '../theme/ThemeRoot'
import styles from './AppShell.module.css'

const NAV = [
  { key: '/listeners', label: '监听管理', icon: <ApiOutlined /> },
  { key: '/sessions', label: '主机管理', icon: <DesktopOutlined /> },
  { key: '/payload', label: '载荷生成', icon: <DownloadOutlined /> },
]

const TITLES: Record<string, string> = {
  '/listeners': '监听管理',
  '/sessions': '主机管理',
  '/payload': '载荷生成',
}

type Me = { username?: string; title?: string }

export default function AppShell() {
  const loc = useLocation()
  const nav = useNavigate()
  const { id, palette, setTheme } = useTheme()
  const [brand, setBrand] = useState('管理平台')
  const [user, setUser] = useState('admin')
  const [online, setOnline] = useState(0)
  const [listeners, setListeners] = useState(0)
  const selected = NAV.find((i) => loc.pathname.startsWith(i.key))?.key || '/listeners'
  const pageTitle = loc.pathname.startsWith('/sessions') ? TITLES['/sessions'] : TITLES[selected] || '工作台'

  useEffect(() => {
    void api('/api/me')
      .then((d) => {
        const me = d as Me
        if (me.title) setBrand(me.title)
        if (me.username) setUser(me.username)
      })
      .catch(() => {
        /* keep default */
      })
  }, [])

  useEffect(() => {
    let stop = false
    const tick = async () => {
      try {
        const [ls, ss] = await Promise.all([api('/api/listeners'), api('/api/sessions')])
        if (stop) return
        const list = Array.isArray(ls) ? ls : []
        const hosts = Array.isArray(ss) ? ss : []
        setListeners(list.length)
        setOnline(hosts.filter((h: { status?: string }) => h.status === 'online').length)
      } catch {
        /* ignore */
      }
    }
    void tick()
    const t = setInterval(() => void tick(), 5000)
    return () => {
      stop = true
      clearInterval(t)
    }
  }, [])

  useEffect(() => {
    document.title = `${pageTitle} · ${palette.label}`
  }, [pageTitle, palette.label])

  const go = (path: string) => {
    nav({ pathname: path, search: loc.search })
  }

  return (
    <div className={styles.shell}>
      <a className={styles.skip} href="#main">
        跳到内容
      </a>
      <header className={styles.bar}>
        <div className={styles.mark} aria-hidden>
          C2
        </div>
        <span className={styles.brand}>{brand}</span>
        <nav className={styles.nav} aria-label="主导航">
          {NAV.map((item) => (
            <button
              key={item.key}
              type="button"
              className={styles.navBtn}
              aria-current={selected === item.key ? 'page' : undefined}
              onClick={() => go(item.key)}
            >
              {item.icon}
              <span>{item.label}</span>
            </button>
          ))}
        </nav>
        <h1 className={styles.srOnly}>{pageTitle}</h1>
        <div className={styles.stats} aria-label="概览">
          <span>
            监听 <b>{listeners}</b>
          </span>
          <span>
            在线 <b>{online}</b>
          </span>
        </div>
        <div className={styles.actions}>
          <Select
            className={styles.themeSelect}
            aria-label="配色"
            value={id}
            options={THEME_OPTIONS}
            onChange={(v) => setTheme(v)}
            popupMatchSelectWidth
          />
          <span className={styles.user}>{user}</span>
          <Button
            icon={<LogoutOutlined />}
            onClick={() => {
              clearToken()
              nav({ pathname: '/login', search: loc.search })
            }}
          >
            退出
          </Button>
        </div>
      </header>
      <main id="main" className={styles.main}>
        <Outlet />
      </main>
    </div>
  )
}
