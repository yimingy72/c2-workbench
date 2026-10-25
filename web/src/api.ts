export function getToken(): string | null {
  return localStorage.getItem('token')
}

export function setToken(t: string) {
  localStorage.setItem('token', t)
}

export function clearToken() {
  localStorage.removeItem('token')
}

function filenameFromCD(cd: string | null, fallback: string) {
  if (!cd) return fallback
  const star = /filename\*\s*=\s*UTF-8''([^;]+)/i.exec(cd)
  if (star) {
    try {
      return decodeURIComponent(star[1])
    } catch {
      return fallback
    }
  }
  return fallback
}

async function parse(res: Response) {
  const text = await res.text()
  let data: unknown = null
  if (text) {
    try {
      data = JSON.parse(text)
    } catch {
      data = text
    }
  }
  if (!res.ok) {
    const msg =
      typeof data === 'object' && data && 'message' in data
        ? String((data as { message: string }).message)
        : res.statusText
    throw new Error(msg)
  }
  return data
}

export async function api(path: string, init: RequestInit = {}) {
  const headers = new Headers(init.headers)
  headers.set('Content-Type', 'application/json')
  const tok = getToken()
  if (tok) headers.set('Authorization', `Bearer ${tok}`)
  let res: Response
  try {
    res = await fetch(path, { ...init, headers })
  } catch {
    throw new Error('无法连接服务器')
  }
  if (res.status === 401) {
    clearToken()
    if (!path.startsWith('/api/login')) {
      window.location.assign('/login')
    }
  }
  return parse(res)
}

export async function postDownload(path: string, body: unknown, fallbackName: string) {
  const headers = new Headers()
  headers.set('Content-Type', 'application/json')
  const tok = getToken()
  if (tok) headers.set('Authorization', `Bearer ${tok}`)
  let res: Response
  try {
    res = await fetch(path, { method: 'POST', headers, body: JSON.stringify(body) })
  } catch {
    throw new Error('无法连接服务器')
  }
  if (res.status === 401) {
    clearToken()
    window.location.assign('/login')
    throw new Error('unauthorized')
  }
  if (!res.ok) {
    await parse(res)
    throw new Error('download failed')
  }
  const blob = await res.blob()
  const name = filenameFromCD(res.headers.get('Content-Disposition'), fallbackName)
  const url = URL.createObjectURL(blob)
  try {
    const a = document.createElement('a')
    a.href = url
    a.download = name
    a.rel = 'noopener'
    document.body.appendChild(a)
    a.click()
    a.remove()
  } finally {
    URL.revokeObjectURL(url)
  }
  return { name, size: blob.size }
}

export async function downloadGet(path: string, fallbackName: string) {
  const headers = new Headers()
  const tok = getToken()
  if (tok) headers.set('Authorization', `Bearer ${tok}`)
  let res: Response
  try {
    res = await fetch(path, { headers })
  } catch {
    throw new Error('无法连接服务器')
  }
  if (res.status === 401) {
    clearToken()
    window.location.assign('/login')
    throw new Error('unauthorized')
  }
  if (!res.ok) {
    await parse(res)
    throw new Error('download failed')
  }
  const blob = await res.blob()
  const name = filenameFromCD(res.headers.get('Content-Disposition'), fallbackName)
  const url = URL.createObjectURL(blob)
  try {
    const a = document.createElement('a')
    a.href = url
    a.download = name
    a.rel = 'noopener'
    document.body.appendChild(a)
    a.click()
    a.remove()
  } finally {
    URL.revokeObjectURL(url)
  }
  return { name, size: blob.size }
}
