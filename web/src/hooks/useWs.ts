import { useEffect, useRef } from 'react'
import { getToken } from '../api'

export type WsEvent = {
  kind: string
  session_id?: string
  task_id?: string
  data?: string
  encoding?: string
  mime?: string
  name?: string
  error?: string
  listener_id?: string
}

export function useWs(onMessage: (ev: WsEvent) => void) {
  const ref = useRef(onMessage)

  useEffect(() => {
    ref.current = onMessage
  }, [onMessage])

  useEffect(() => {
    let stop = false
    let ws: WebSocket | null = null
    let timer = 0
    const connect = () => {
      if (stop) return
      const tok = getToken()
      if (!tok) return
      const proto = location.protocol === 'https:' ? 'wss' : 'ws'
      ws = new WebSocket(`${proto}://${location.host}/api/ws?token=${encodeURIComponent(tok)}`)
      ws.onmessage = (e) => {
        try {
          ref.current(JSON.parse(e.data) as WsEvent)
        } catch {
          /* ignore malformed frames */
        }
      }
      ws.onclose = () => {
        if (!stop) timer = window.setTimeout(connect, 1500)
      }
    }
    connect()
    return () => {
      stop = true
      window.clearTimeout(timer)
      ws?.close()
    }
  }, [])
}
