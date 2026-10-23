// SSE 客户端：订阅 /api/v1/events/stream，按事件类型分发；断线自动重连（浏览器内置）。
const TYPES = [
  'hello',
  'backup.progress', 'backup.started', 'backup.completed',
  'task.started', 'task.paused', 'task.retry', 'task.cancelled', 'task.failed', 'task.interrupted',
  'plan.generated', 'device.attached', 'device.detached',
  'fault.hit', 'config.changed', 'recovery.interrupted', 'system.started', 'system.stopping',
  'auth.login', 'auth.logout', 'auth.login_failed', 'auth.login_rate_limited',
]

export function connect(handler) {
  let es = null
  let closed = false
  const open = () => {
    if (closed) return
    es = new EventSource('/api/v1/events/stream', { withCredentials: true })
    for (const t of TYPES) {
      es.addEventListener(t, (ev) => {
        let data = null
        try {
          data = JSON.parse(ev.data)
        } catch {
          data = null
        }
        handler(t, data, ev)
      })
    }
    es.onopen = () => handler('$open')
    es.onerror = () => handler('$error')
  }
  open()
  return {
    close() {
      closed = true
      if (es) es.close()
    },
  }
}
