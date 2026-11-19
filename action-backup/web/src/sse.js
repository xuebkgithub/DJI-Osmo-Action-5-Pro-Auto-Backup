// SSE 客户端：订阅 /api/v1/events/stream，按事件类型分发；断线自动重连（浏览器内置）。
// EventSource 只能按名字逐个监听，所以后端新增事件类型时必须在 TYPES 里登记，否则页面收不到。
const TYPES = [
  'hello',
  'backup.progress', 'backup.started', 'backup.completed',
  'task.started', 'task.paused', 'task.retry', 'task.cancelled', 'task.failed', 'task.interrupted',
  'plan.generated', 'device.attached', 'device.detached',
  'fault.hit', 'config.changed', 'recovery.interrupted', 'system.started', 'system.stopping',
  'auth.login', 'auth.logout', 'auth.login_failed', 'auth.login_rate_limited',
  'backup.finished', 'storage.mounted', 'storage.unmounted', 'storage.mount_failed', 'notify.failed',
  'update.available', 'update.check_failed', 'update.progress', 'update.requested', 'update.installed', 'update.failed',
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
