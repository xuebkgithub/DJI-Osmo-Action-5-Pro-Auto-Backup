export function bytes(n) {
  if (n == null || isNaN(n)) return '-'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let v = Number(n)
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v < 10 && i > 0 ? v.toFixed(2) : v < 100 && i > 0 ? v.toFixed(1) : Math.round(v)} ${units[i]}`
}

export function speed(bps) {
  if (!bps) return '-'
  return bytes(bps) + '/s'
}

export function time(s) {
  if (!s) return '-'
  const d = new Date(s)
  if (isNaN(d)) return s
  return d.toLocaleString('zh-CN', { hour12: false })
}

export function ago(s) {
  if (!s) return '-'
  const diff = (Date.now() - new Date(s).getTime()) / 1000
  if (diff < 60) return `${Math.max(0, Math.round(diff))} 秒前`
  if (diff < 3600) return `${Math.round(diff / 60)} 分钟前`
  if (diff < 86400) return `${Math.round(diff / 3600)} 小时前`
  return `${Math.round(diff / 86400)} 天前`
}

export function duration(sec) {
  if (sec == null) return '-'
  sec = Math.round(sec)
  const h = Math.floor(sec / 3600)
  const m = Math.floor((sec % 3600) / 60)
  const s = sec % 60
  return h ? `${h}h ${m}m ${s}s` : m ? `${m}m ${s}s` : `${s}s`
}

export function percent(p) {
  if (p == null) return '-'
  return `${Number(p).toFixed(1)}%`
}
