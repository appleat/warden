const TOKEN_KEY = 'warden_token'
const HOST_KEY = 'warden_host'

// 项目更名（GoServerMgr → Warden）后的登录态迁移：
// 把旧 key 搬到新 key，用户升级后不需要重新登录。
;(function migrateLegacyKeys() {
  const legacy: [string, string][] = [
    ['gsm_token', TOKEN_KEY],
    ['gsm_host', HOST_KEY]
  ]
  for (const [oldKey, newKey] of legacy) {
    const v = localStorage.getItem(oldKey)
    if (v !== null && localStorage.getItem(newKey) === null) {
      localStorage.setItem(newKey, v)
    }
    localStorage.removeItem(oldKey)
  }
})()

// ---------- 当前主机 ----------
// 除账号、主机台账、监控、审计、告警设置外，其余接口都跟随当前主机。
const HOST_SCOPED = [
  '/api/dashboard',
  '/api/docker',
  '/api/containers',
  '/api/images',
  '/api/volumes',
  '/api/networks',
  '/api/nacos',
  '/api/redis',
  '/api/system/'
]
// 注意：/api/files 故意不放进 HOST_SCOPED。文件管理后端按请求里的 ?id=（SSH 主机）
// 在本地磁盘与远程 SFTP 间切换，而非跟随顶部「纳管主机」；若自动附加 host 会被
// HostProxy 转发到远程 Agent，反而取不到文件。

export function getHostId(): number {
  const v = Number(localStorage.getItem(HOST_KEY) || 1)
  return Number.isFinite(v) && v > 0 ? v : 1
}

export function setHostId(id: number) {
  localStorage.setItem(HOST_KEY, String(id))
}

/** 给手写的 URL（下载、上传）补上主机参数 */
export function hostQuery(): string {
  return `host=${getHostId()}`
}

export function isHostScoped(path: string): boolean {
  return HOST_SCOPED.some((p) => path.startsWith(p))
}

export function getToken(): string {
  return localStorage.getItem(TOKEN_KEY) || ''
}

export function setToken(t: string) {
  if (t) localStorage.setItem(TOKEN_KEY, t)
  else localStorage.removeItem(TOKEN_KEY)
}

export class ApiError extends Error {
  status: number
  constructor(msg: string, status: number) {
    super(msg)
    this.status = status
  }
}

export interface ApiResp<T> {
  code: number
  msg: string
  data: T
}

interface ReqOpts {
  method?: string
  body?: any
  query?: Record<string, any>
  form?: FormData
}

function buildQuery(q?: Record<string, any>) {
  if (!q) return ''
  const p = new URLSearchParams()
  Object.entries(q).forEach(([k, v]) => {
    if (v === undefined || v === null || v === '') return
    p.append(k, String(v))
  })
  const s = p.toString()
  return s ? `?${s}` : ''
}

export async function api<T = any>(path: string, opts: ReqOpts = {}): Promise<T> {
  const headers: Record<string, string> = {}
  const token = getToken()
  if (token) headers['Authorization'] = `Bearer ${token}`

  let body: BodyInit | undefined
  if (opts.form) {
    body = opts.form
  } else if (opts.body !== undefined) {
    headers['Content-Type'] = 'application/json'
    body = JSON.stringify(opts.body)
  }

  // 自动带上当前主机，实现「切换主机即切换视角」
  let query = opts.query
  if (isHostScoped(path)) {
    query = { ...(opts.query || {}), host: getHostId() }
  }

  const res = await fetch(path + buildQuery(query), {
    method: opts.method || (body ? 'POST' : 'GET'),
    headers,
    body,
    credentials: 'same-origin'
  })

  let payload: ApiResp<T> | null = null
  const text = await res.text()
  try {
    payload = text ? JSON.parse(text) : null
  } catch {
    payload = null
  }

  if (res.status === 401) {
    setToken('')
    if (!location.pathname.endsWith('/login')) {
      location.href = '/login'
    }
    throw new ApiError(payload?.msg || '登录已失效', 401)
  }
  if (!res.ok || (payload && payload.code !== 0)) {
    throw new ApiError(payload?.msg || `请求失败 (${res.status})`, res.status)
  }
  return (payload?.data ?? null) as T
}

export function wsURL(path: string, params: Record<string, any> = {}) {
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
  const q = new URLSearchParams()
  Object.entries(params).forEach(([k, v]) => {
    if (v !== undefined && v !== null && v !== '') q.append(k, String(v))
  })
  q.append('token', getToken())
  q.append('host', String(getHostId()))
  return `${proto}//${location.host}${path}?${q.toString()}`
}

export function errMsg(e: any): string {
  if (e instanceof ApiError) return e.message
  return e?.message || String(e)
}
