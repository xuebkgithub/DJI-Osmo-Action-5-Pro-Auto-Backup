// REST 客户端：同源 Cookie 会话，写操作自动附带 X-CSRF-Token，401 时触发回调。
const state = { csrf: '', onUnauthorized: null }

export class ApiError extends Error {
  constructor(status, code, message, retryable) {
    super(message || code || `HTTP ${status}`)
    this.status = status
    this.code = code || 'HTTP_' + status
    this.retryable = !!retryable
  }
}

export function setCSRF(token) {
  state.csrf = token || ''
}

export function onUnauthorized(fn) {
  state.onUnauthorized = fn
}

export async function api(method, path, body) {
  const headers = { Accept: 'application/json' }
  if (body !== undefined) headers['Content-Type'] = 'application/json'
  if (method !== 'GET' && state.csrf) headers['X-CSRF-Token'] = state.csrf
  const res = await fetch(path, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
    credentials: 'same-origin',
  })
  let json = null
  try {
    json = await res.json()
  } catch {
    json = null
  }
  if (!res.ok) {
    const e = json && json.error ? json.error : {}
    if (res.status === 401 && e.code === 'UNAUTHENTICATED' && state.onUnauthorized) state.onUnauthorized()
    throw new ApiError(res.status, e.code, e.message, e.retryable)
  }
  return json ? json.data : null
}

export const get = (path) => api('GET', path)
export const post = (path, body) => api('POST', path, body)
export const patch = (path, body) => api('PATCH', path, body)
export const del = (path) => api('DELETE', path)
