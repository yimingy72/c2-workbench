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
      termRef.current.write(e.data)
    }
  })

  return <div ref={el} className={styles.xterm} />
}
