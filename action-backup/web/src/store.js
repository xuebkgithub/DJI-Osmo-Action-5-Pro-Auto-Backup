import { reactive } from 'vue'
import { get, post, setCSRF, onUnauthorized } from './api.js'
import { connect } from './sse.js'

export const store = reactive({
  booted: false,
  session: null, // {auth_enabled, authenticated, username, csrf_token}
  authEnabled: false,
  mode: null,
  status: null,
  health: null,
  update: null, // GET /api/v1/system/update：当前 / 最新版本与检查状态
  devices: [],
  plan: null,
  events: [],
  progress: {}, // task_id -> progress data
  sse: 'closed', // closed | open | error
  toast: null,
  // 安装更新期间的全屏遮罩：null | { version, since, timedOut }
  installing: null,
  tab: 'overview',
})

let sse = null
let refreshTimer = null
let toastTimer = null

export function notify(msg, kind = 'info') {
  store.toast = { msg, kind }
  clearTimeout(toastTimer)
  toastTimer = setTimeout(() => (store.toast = null), kind === 'error' ? 6000 : 3000)
}

export function errorText(e) {
  return e && e.message ? `${e.code ? e.code + ': ' : ''}${e.message}` : String(e)
}

export async function boot() {
  onUnauthorized(() => {
    if (store.session && store.session.authenticated) logoutLocal()
  })
  try {
    const me = await get('/api/v1/auth/me')
    applySession(me)
  } catch (e) {
    if (e.status === 401) {
      store.authEnabled = true
      store.session = null
    } else {
      notify(errorText(e), 'error')
    }
  }
  store.booted = true
  if (store.session) await afterLogin()
}

function applySession(me) {
  store.session = me
  store.authEnabled = !!me.auth_enabled
  setCSRF(me.csrf_token || '')
}

export async function login(username, password) {
  const me = await post('/api/v1/auth/login', { username, password })
  applySession(me)
  await afterLogin()
}

export async function logout() {
  try {
    await post('/api/v1/auth/logout')
  } finally {
    logoutLocal()
  }
}

function logoutLocal() {
  stopStream()
  store.session = null
  setCSRF('')
  store.authEnabled = true
}

async function afterLogin() {
  await Promise.all([refreshMode(), refreshAll()])
  startStream()
}

export async function refreshMode() {
  store.mode = await get('/api/v1/system/mode')
}

export async function refreshStatus() {
  store.status = await get('/api/v1/system/status')
}

export async function refreshHealth() {
  try {
    store.health = await get('/api/v1/system/health')
  } catch (e) {
    // 503 时 body 仍是 health 数据
    if (e.status === 503) {
      try {
        const r = await fetch('/api/v1/system/health', { credentials: 'same-origin' })
        const j = await r.json()
        store.health = j.data
        return
      } catch {
        /* ignore */
      }
    }
    store.health = { status: 'unknown', error: errorText(e) }
  }
}

export async function refreshUpdate() {
  store.update = await get('/api/v1/system/update')
  watchInstall()
}

// applyUpdate 开始安装最新版本：服务端下载、验签、等备份空闲后提交安装请求，立即返回。
export async function applyUpdate() {
  store.update = await post('/api/v1/system/update/apply')
  watchInstall()
  return store.update
}

// 安装请求提交后，root 定时任务（每分钟一次）会停掉并重启服务。这段时间页面显示遮罩并轮询，
// 判定「服务已经重启过」的三个信号任一成立即刷新页面（前端资源是嵌在二进制里的，必须重新加载）：
// 请求失败过之后又通了；返回 401（会话在内存里，重启即失效）；started_at 变了。
let installTimer = null
function watchInstall() {
  const inst = store.update && store.update.install
  if (!inst || inst.stage !== 'requested') {
    if (store.installing && !store.installing.timedOut) stopInstallWatch()
    return
  }
  if (installTimer) return
  store.installing = { version: inst.version, since: Date.now(), timedOut: false }
  const startedAt = store.status && store.status.started_at
  let sawDown = false
  installTimer = setInterval(async () => {
    if (Date.now() - store.installing.since > 6 * 60 * 1000) {
      store.installing = { ...store.installing, timedOut: true }
      clearInterval(installTimer)
      installTimer = null
      return
    }
    try {
      const r = await fetch('/api/v1/system/status', { credentials: 'same-origin', cache: 'no-store' })
      if (r.status === 401) return location.reload()
      const j = await r.json().catch(() => null)
      if (sawDown || (j && j.data && startedAt && j.data.started_at !== startedAt)) location.reload()
    } catch {
      sawDown = true
    }
  }, 1500)
}
function stopInstallWatch() {
  clearInterval(installTimer)
  installTimer = null
  store.installing = null
}
export function dismissInstalling() {
  stopInstallWatch()
}

// checkUpdate 立即向 GitHub 查一次。失败时接口返回 502，但响应体里仍带当前状态（含上次成功的结果）。
export async function checkUpdate() {
  try {
    store.update = await post('/api/v1/system/update/check')
  } catch (e) {
    await refreshUpdate().catch(() => {})
    throw e
  }
  return store.update
}

export async function refreshDevices() {
  store.devices = await get('/api/v1/devices')
}

export async function refreshPlan() {
  try {
    store.plan = await get('/api/v1/backups/plan')
  } catch (e) {
    if (e.code === 'NO_PLAN') store.plan = null
    else throw e
  }
}

export async function refreshEvents(limit = 100) {
  store.events = await get(`/api/v1/events?limit=${limit}`)
}

export async function refreshAll() {
  const results = await Promise.allSettled([refreshStatus(), refreshHealth(), refreshDevices(), refreshPlan(), refreshEvents(), refreshUpdate()])
  const failed = results.find((r) => r.status === 'rejected')
  if (failed && failed.reason && failed.reason.status !== 401) notify(errorText(failed.reason), 'error')
}

function scheduleRefresh() {
  clearTimeout(refreshTimer)
  refreshTimer = setTimeout(() => {
    refreshStatus().catch(() => {})
    refreshDevices().catch(() => {})
    refreshEvents().catch(() => {})
  }, 300)
}

function startStream() {
  stopStream()
  sse = connect((type, data) => {
    switch (type) {
      case '$open':
        store.sse = 'open'
        return
      case '$error':
        store.sse = 'error'
        return
      case 'hello':
        return
      case 'update.progress':
        // 下载 / 安装请求的进度：不进事件列表，只更新「软件更新」卡片
        if (data && data.data && store.update && store.update.install) {
          store.update = { ...store.update, install: { ...store.update.install, ...data.data } }
          if (data.data.stage === 'requested' || data.data.stage === 'failed') refreshUpdate().catch(() => {})
        }
        return
      case 'backup.progress':
        if (data && data.data) store.progress = { ...store.progress, [data.data.task_id]: data.data }
        return
    }
    if (data && data.type) {
      store.events = [data, ...store.events].slice(0, 200)
      if (data.task_id && /^(backup\.completed|task\.(failed|cancelled|interrupted|paused))$/.test(data.type)) {
        const p = { ...store.progress }
        delete p[data.task_id]
        store.progress = p
      }
      if (type === 'plan.generated') refreshPlan().catch(() => {})
      if (/^storage\./.test(type)) refreshHealth().catch(() => {})
      // 新版本检查结果变化，或在线改了 update.* 配置（通道、开关）
      if (/^update\./.test(type) || type === 'config.changed') refreshUpdate().catch(() => {})
      scheduleRefresh()
    }
  })
}

function stopStream() {
  if (sse) sse.close()
  sse = null
  store.sse = 'closed'
}
