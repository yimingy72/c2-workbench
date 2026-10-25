export function statusLabel(s: string) {
  if (s === 'online') return '在线'
  if (s === 'stale') return '停滞'
  if (s === 'dead') return '离线'
  return s
}

export function statusTone(s: string): 'success' | 'warning' | 'default' {
  if (s === 'online') return 'success'
  if (s === 'stale') return 'warning'
  return 'default'
}

export function formatSeen(ts: number) {
  if (!ts) return '—'
  const sec = Math.max(0, Math.floor(Date.now() / 1000 - ts))
  if (sec < 5) return '刚刚'
  if (sec < 60) return `${sec}秒前`
  if (sec < 3600) return `${Math.floor(sec / 60)}分钟前`
  if (sec < 86400) return `${Math.floor(sec / 3600)}小时前`
  return new Date(ts * 1000).toLocaleString('zh-CN', { hour12: false })
}
