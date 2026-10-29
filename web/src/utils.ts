export function fmtBytes(n?: number, digits = 1): string {
  if (!n || n <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']
  let i = 0
  let v = n
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toFixed(i === 0 ? 0 : digits)} ${units[i]}`
}

export function fmtRate(n?: number): string {
  return `${fmtBytes(n || 0)}/s`
}

export function fmtDuration(sec?: number): string {
  if (!sec || sec <= 0) return '-'
  const d = Math.floor(sec / 86400)
  const h = Math.floor((sec % 86400) / 3600)
  const m = Math.floor((sec % 3600) / 60)
  if (d > 0) return `${d} 天 ${h} 小时`
  if (h > 0) return `${h} 小时 ${m} 分`
  return `${m} 分钟`
}

export function fmtTime(ts?: number | string): string {
  if (!ts) return '-'
  const d = typeof ts === 'number' ? new Date(ts < 1e12 ? ts * 1000 : ts) : new Date(ts)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}

export function fmtClock(ts?: number): string {
  if (!ts) return ''
  const d = new Date(ts)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}

export function percentClass(v: number): string {
  if (v >= 90) return 'danger'
  if (v >= 75) return 'warn'
  return ''
}

export function num(n?: number, d = 1): string {
  if (n === undefined || n === null || Number.isNaN(n)) return '0'
  return Number(n).toFixed(d)
}

// copyText 复制文本，成功返回 true。
// navigator.clipboard 只在安全上下文（HTTPS 或 localhost）下存在，
// 局域网用 http://IP:端口 访问面板时它是 undefined，必须兜底到 execCommand。
export async function copyText(text: string): Promise<boolean> {
  if (!text) return false
  if (navigator.clipboard && window.isSecureContext) {
    try {
      await navigator.clipboard.writeText(text)
      return true
    } catch {
      // 权限被拒等情况，继续走兜底
    }
  }
  return legacyCopy(text)
}

function legacyCopy(text: string): boolean {
  const ta = document.createElement('textarea')
  ta.value = text
  ta.setAttribute('readonly', '')
  // 挪到视口外但保持可聚焦，避免页面滚动或闪一下
  ta.style.position = 'fixed'
  ta.style.top = '0'
  ta.style.left = '-9999px'
  document.body.appendChild(ta)
  ta.focus()
  ta.select()
  ta.setSelectionRange(0, ta.value.length) // iOS Safari 需要显式设置选区
  let ok = false
  try {
    ok = document.execCommand('copy')
  } catch {
    ok = false
  }
  document.body.removeChild(ta)
  return ok
}
