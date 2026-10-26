import { useEffect, useRef } from 'react'
import { App } from 'antd'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import { api } from '../api'
import { useWs } from '../hooks/useWs'
import styles from './console.module.css'

export default function TerminalPanel({
  id,
  online,
  visible,
}: {
  id: string
  online: boolean
  visible: boolean
}) {
  const { message } = App.useApp()
  const el = useRef<HTMLDivElement>(null)
  const termRef = useRef<Terminal | null>(null)
  const fitRef = useRef<FitAddon | null>(null)
  const queueRef = useRef('')
  const sendingRef = useRef(false)
  // Echo state for the "local echo only when needed" rule:
  //   on=false  → the shell is expected to echo; we draw nothing ourselves
  //   on=true   → the shell stayed silent, so the panel echoes the keys
  const echoRef = useRef<{ on: boolean; awaiting: string; timer: number; lastData: number }>({
    on: false,
    awaiting: '',
    timer: 0,
    lastData: 0,
  })

  // Input is sent one request at a time.
  //
  // It used to fire one POST per keystroke: concurrent requests have no
  // ordering guarantee, so "ip a" could reach the shell as "ip", " a", "i" and
  // the echo came back scrambled ("ipi p aa"). Chaining the sends through a
  // queue keeps the byte order the user typed.
  const pump = useRef(async () => {
    if (sendingRef.current) return
    sendingRef.current = true
    try {
      while (queueRef.current) {
        const chunk = queueRef.current
        queueRef.current = ''
        await api(`/api/sessions/${id}/shell/in`, {
          method: 'POST',
          body: JSON.stringify({ data: chunk }),
        }).catch(() => {
          /* connection hiccup: the next chunk will retry the path */
        })
      }
    } finally {
      sendingRef.current = false
    }
  })

  useEffect(() => {
    if (!el.current) return
    const term = new Terminal({
      convertEol: true,
      cursorBlink: true,
      fontSize: 13,
      fontFamily: "'IBM Plex Mono', 'PingFang SC', 'Microsoft YaHei', 'Noto Sans SC', ui-monospace, monospace",
      theme: { background: '#2A2C34', foreground: '#ffffff', cursor: '#ffffff' },
    })
    const fit = new FitAddon()
    term.loadAddon(fit)
    term.open(el.current)
    fitRef.current = fit
    termRef.current = term
    const doFit = () => {
      try {
        fit.fit()
      } catch {
        /* layout not ready */
      }
    }
    doFit()
    const t0 = window.setTimeout(doFit, 50)
    const t1 = window.setTimeout(doFit, 200)
    const ro = new ResizeObserver(doFit)
    ro.observe(el.current)
    if (el.current.parentElement) ro.observe(el.current.parentElement)

    // No local echo: the shell echoes, and drawing every key twice is what made
    // the line look garbled. Arrow keys / control sequences are forwarded as-is
    // so line editing keeps working.
    const sub = term.onData((d) => {
      queueRef.current += d === '\r' ? '\r\n' : d
      void pump.current()

      const st = echoRef.current
      const printable = d.length === 1 && d >= ' ' && d !== '\x7f'
      if (!printable) return
      if (st.on) {
        // this shell does not echo (pipes / echo disabled): draw immediately
        term.write(d)
        return
      }
      // optimistic: assume the shell echoes. If nothing comes back shortly,
      // switch to local echo so the operator can still see what they type.
      st.awaiting += d
      if (st.timer) window.clearTimeout(st.timer)
      st.timer = window.setTimeout(() => {
        st.timer = 0
        if (st.awaiting && Date.now() - st.lastData > 150) {
          st.on = true
          term.write(st.awaiting)
          st.awaiting = ''
        }
      }, 220)
    })
    return () => {
      window.clearTimeout(t0)
      window.clearTimeout(t1)
      sub.dispose()
      ro.disconnect()
      term.dispose()
      termRef.current = null
      fitRef.current = null
      void api(`/api/sessions/${id}/shell`, { method: 'POST', body: JSON.stringify({ action: 'stop' }) }).catch(() => {
        /* ignore */
      })
    }
  }, [id])

  useEffect(() => {
    if (visible) {
      const t = window.setTimeout(() => fitRef.current?.fit(), 30)
      return () => window.clearTimeout(t)
    }
  }, [visible])

  useEffect(() => {
    if (!online || !id || !visible) return
    void api(`/api/sessions/${id}/shell`, { method: 'POST', body: JSON.stringify({ action: 'start' }) })
      .then(() => {
        // The shell prints its banner and first prompt the moment it starts,
        // which is usually before the websocket subscription is up, so that
        // output is lost and the tab looks blank. One newline asks for a fresh
        // prompt once we are definitely listening.
        window.setTimeout(() => {
          queueRef.current += '\r'
          void pump.current()
        }, 200)
      })
      .catch((e) => {
        message.error(e instanceof Error ? e.message : '终端启动失败')
      })
  }, [id, online, visible, message])

  useWs((e) => {
    if (e.session_id !== id || !termRef.current) return
    if (e.kind === 'shell.data' && e.data) {
      const st = echoRef.current
      st.lastData = Date.now()
      if (st.timer) {
        window.clearTimeout(st.timer)
        st.timer = 0
      }
      if (st.on && st.awaiting && e.data.includes(st.awaiting)) {
        // the shell does echo after all (e.g. a pty showed up): stop doubling
        st.on = false
      }
      st.awaiting = ''
      termRef.current.write(e.data)
    }
  })

  return <div ref={el} className={styles.xterm} />
}
