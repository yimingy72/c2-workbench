import { useEffect, useRef } from 'react'
import { Button, App } from 'antd'
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
  const typedRef = useRef('')
  const echoRef = useRef('')

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
    const sub = term.onData((d) => {
      let send = d
      if (d === '\x7f' || d === '\b') {
        if (!typedRef.current.length) return
        typedRef.current = typedRef.current.slice(0, -1)
        term.write('\b \b')
        send = '\b'
      } else if (d === '\r') {
        echoRef.current = typedRef.current
        typedRef.current = ''
        term.write('\r\n')
        send = '\r\n'
      } else if (d.charCodeAt(0) >= 32 || d === '\t') {
        typedRef.current += d
        term.write(d)
      } else if (d.startsWith('\x1b')) {
        return
      }
      void api(`/api/sessions/${id}/shell/in`, { method: 'POST', body: JSON.stringify({ data: send }) }).catch(() => {
        /* ignore burst */
      })
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
    void api(`/api/sessions/${id}/shell`, { method: 'POST', body: JSON.stringify({ action: 'start' }) }).catch((e) => {
      message.error(e instanceof Error ? e.message : '终端启动失败')
    })
  }, [id, online, visible, message])

  useWs((e) => {
    if (e.session_id !== id || !termRef.current) return
    if (e.kind === 'shell.data' && e.data) {
      let data = e.data
      const echo = echoRef.current
      if (echo) {
        const cuts = [echo + '\r\n', echo + '\n', echo + '\r', echo]
        for (const c of cuts) {
          const i = data.indexOf(c)
          if (i >= 0 && i <= 4) {
            data = data.slice(0, i) + data.slice(i + c.length)
            echoRef.current = ''
            break
          }
        }
      }
      if (data) termRef.current.write(data)
    }
  })

  return (
    <>
      <div className={styles.fsBar}>
        <span>交互终端</span>
        <Button size="small" onClick={() => termRef.current?.clear()}>
          清屏
        </Button>
      </div>
      <div ref={el} className={styles.xterm} />
    </>
  )
}
