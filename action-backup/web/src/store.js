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
  devices: [],
  plan: null,
  events: [],
  progress: {}, // task_id -> progress data
  sse: 'closed', // closed | open | error
  toast: null,
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
  const results = await Promise.allSettled([refreshStatus(), refreshHealth(), refreshDevices(), refreshPlan(), refreshEvents()])
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
      scheduleRefresh()
    }
  })
}

function stopStream() {
  if (sse) sse.close()
  sse = null
  store.sse = 'closed'
}
