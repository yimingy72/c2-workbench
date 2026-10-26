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
  os,
}: {
  id: string
  online: boolean
  visible: boolean
  os?: string
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
    const onContextMenu = (ev: MouseEvent) => {
      ev.preventDefault()
      if (term.hasSelection()) {
        void copySelection()
        term.clearSelection()
        return
      }
      void navigator.clipboard?.readText?.().then(sendText).catch(() => {
        /* http page: use Ctrl+Shift+V, which goes through the paste event */
      })
    }
    el.current.addEventListener('contextmenu', onContextMenu)

    const ro = new ResizeObserver(doFit)
    ro.observe(el.current)
    if (el.current.parentElement) ro.observe(el.current.parentElement)

    // No local echo: the shell echoes, and drawing every key twice is what made
    // the line look garbled. Arrow keys / control sequences are forwarded as-is
    // so line editing keeps working.
    const isWin = (os || '').toLowerCase().startsWith('win')
    const sendText = (text: string) => {
      // pasted text may carry \n or \r\n; the shell wants the platform's form
      const norm = text.replace(/\r\n/g, '\n').replace(/\r/g, '\n')
      queueRef.current += isWin ? norm.replace(/\n/g, '\r\n') : norm.replace(/\n/g, '\r')
      void pump.current()
    }

    const copySelection = async () => {
      const sel = term.getSelection()
      if (!sel) return false
      try {
        await navigator.clipboard.writeText(sel)
        return true
      } catch {
        // non-secure context (plain http): fall back to the legacy path
        const ta = document.createElement('textarea')
        ta.value = sel
        ta.style.position = 'fixed'
        ta.style.opacity = '0'
        document.body.appendChild(ta)
        ta.select()
        let ok = false
        try {
          ok = document.execCommand('copy')
        } catch {
          ok = false
        }
        ta.remove()
        return ok
      }
    }

    // Browsers only expose the clipboard to a paste event in an insecure
    // context, so read it from there instead of calling the async API.
    const onPaste = (ev: ClipboardEvent) => {
      const text = ev.clipboardData?.getData('text')
      if (!text) return
      ev.preventDefault()
      ev.stopPropagation()
      sendText(text)
    }
    el.current.addEventListener('paste', onPaste, true)

    term.attachCustomKeyEventHandler((ev) => {
      if (ev.type !== 'keydown') return true
      const mod = ev.ctrlKey || ev.metaKey
      if (mod && ev.shiftKey && (ev.key === 'C' || ev.key === 'c')) {
        void copySelection()
        return false
      }
      if (mod && ev.shiftKey && (ev.key === 'V' || ev.key === 'v')) {
        // returning true lets the browser fire the paste event we listen to
        return true
      }
      if (mod && !ev.shiftKey && (ev.key === 'C' || ev.key === 'c') && term.getSelection()) {
        void copySelection()
        return false
      }
      return true
    })

    const sub = term.onData((d) => {
      // cmd.exe on a pipe only accepts CRLF as Enter; a pty treats CR as NL and
      // CRLF would run an extra empty command.
      const isWindows = (os || '').toLowerCase().startsWith('win')
      queueRef.current += d === '\r' ? (isWindows ? '\r\n' : '\r') : d
      void pump.current()

      const st = echoRef.current
      if (d === '\r') {
        // cmd.exe does not echo and its prompt has no trailing newline, so the
        // prompts pile up on one line unless we break it ourselves.
        if (st.on) term.write('\r\n')
        return
      }
      if (d === '\x7f' || d === '\b') {
        if (st.on) term.write('\b \b')
        return
      }
      const printable = d.length === 1 && d >= ' '
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
      el.current?.removeEventListener('paste', onPaste, true)
      el.current?.removeEventListener('contextmenu', onContextMenu)
      sub.dispose()
      ro.disconnect()
      term.dispose()
      termRef.current = null
      fitRef.current = null
      void api(`/api/sessions/${id}/shell`, { method: 'POST', body: JSON.stringify({ action: 'stop' }) }).catch(() => {
        /* ignore */
      })
    }
  }, [id, os])

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
        const isWindows = (os || '').toLowerCase().startsWith('win')
        window.setTimeout(() => {
          // Asking for a fresh prompt runs an empty command. cmd.exe does not
          // echo and prints its prompt without a newline, so the second prompt
          // would land on the same line as the first - break it ourselves.
          if (isWindows) termRef.current?.write('\r\n')
          queueRef.current += isWindows ? '\r\n' : '\r'
          void pump.current()
        }, 200)
      })
      .catch((e) => {
        message.error(e instanceof Error ? e.message : '终端启动失败')
      })
  }, [id, online, visible, message, os])

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
