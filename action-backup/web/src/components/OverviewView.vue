<script setup>
import { computed, ref } from 'vue'
import { store, refreshStatus, refreshHealth, refreshDevices, notify, errorText } from '../store.js'
import { post } from '../api.js'
import { bytes, speed, percent, duration, ago } from '../format.js'

const busy = ref('')
const writable = computed(() => store.mode && store.mode.destination_write_allowed)
const sched = computed(() => store.status && store.status.scheduler)
const current = computed(() => {
  const c = sched.value && sched.value.current
  if (!c) return null
  const live = store.progress[c.task_id]
  return live ? { ...c, ...live } : c
})
const taskCounts = computed(() => (store.status && store.status.tasks) || {})
const active = computed(() => ['QUEUED', 'TRANSFERRING', 'VERIFYING'].reduce((n, k) => n + (taskCounts.value[k] || 0), 0))
const retryable = computed(() => (taskCounts.value.FAILED || 0) + (taskCounts.value.INTERRUPTED || 0))
const mountControl = computed(() => !!(store.mode && store.mode.storage_mount_control))
const transferring = computed(() => !!(sched.value && sched.value.current))

async function act(name, path, okMsg) {
  busy.value = name
  try {
    const r = await post(path)
    notify(okMsg(r))
    await refreshStatus()
  } catch (e) {
    notify(errorText(e), 'error')
  } finally {
    busy.value = ''
  }
}
const start = () => act('start', '/api/v1/backups/start', (r) => `已入队：新建 ${r.tasks_created}，重排 ${r.tasks_requeued}，冲突 ${r.conflicts}${r.tasks_retry_exhausted ? '，重试耗尽 ' + r.tasks_retry_exhausted : ''}`)
const pause = () => act('pause', '/api/v1/backups/pause', () => '已暂停')
const resume = () => act('resume', '/api/v1/backups/resume', () => '已恢复')
const plan = () => act('plan', '/api/v1/backups/plan', (r) => `计划：上传 ${r.upload.files} 个（${bytes(r.upload.bytes)}），跳过 ${r.skip.files}，冲突 ${r.conflict.files}`)
async function health() {
  busy.value = 'health'
  try {
    await refreshHealth()
    notify(store.health && store.health.status === 'ok' ? '健康检查已刷新：正常' : '健康检查已刷新：仍有异常', store.health && store.health.status === 'ok' ? 'info' : 'error')
  } finally {
    busy.value = ''
  }
}
// 目标存储挂载控制：失败响应（503）里也带 health，直接用它刷新目标存储一栏
async function storageAct(name, path, okMsg, confirmMsg) {
  if (confirmMsg && !window.confirm(confirmMsg)) return
  busy.value = name
  try {
    await post(path)
    notify(okMsg)
  } catch (e) {
    notify(errorText(e), 'error')
  } finally {
    await refreshHealth()
    busy.value = ''
  }
}
const mount = () => storageAct('mount', '/api/v1/storage/mount', 'NAS 已挂载')
const remount = () => storageAct('remount', '/api/v1/storage/remount', 'NAS 已重新挂载', transferring.value ? '有任务正在传输，重新挂载会中断它（之后可重试）。继续？' : '')
const unmount = () => storageAct('unmount', '/api/v1/storage/unmount', 'NAS 已卸载', '卸载后备份无法写入，直到重新挂载。继续？')
async function rescan() {
  busy.value = 'rescan'
  try {
    store.devices = await post('/api/v1/devices/rescan')
    notify(`已重新检测设备：${store.devices.length} 台在位`)
    await Promise.all([refreshStatus(), refreshHealth()])
  } catch (e) {
    notify(errorText(e), 'error')
  } finally {
    busy.value = ''
  }
}
const retryFailed = () => act('retryFailed', '/api/v1/backups/retry-failed', (r) => `已重新入队 ${r.retried} 个失败 / 中断任务${r.skipped ? '，跳过 ' + r.skipped : ''}`)
</script>

<template>
  <div class="grid">
    <div class="card">
      <h2>服务</h2>
      <dl class="kv" v-if="store.status">
        <dt>版本</dt><dd>{{ store.status.version }}</dd>
        <dt>模式</dt><dd>{{ store.status.mode }} / {{ store.status.platform }}</dd>
        <dt>运行时长</dt><dd>{{ duration(store.status.uptime_seconds) }}</dd>
        <dt>目标</dt><dd class="mono">{{ store.status.storage.kind }} · {{ store.status.storage.root }}</dd>
        <dt>认证</dt><dd>{{ store.status.auth && store.status.auth.enabled ? `启用（${store.status.auth.active_sessions} 个会话）` : '未启用' }}</dd>
      </dl>
    </div>
    <div class="card">
      <h2>健康</h2>
      <template v-if="store.health">
        <div class="big" :class="store.health.status === 'ok' ? 'ok' : 'warn'">{{ store.health.status }}</div>
        <dl class="kv">
          <dt>数据库</dt><dd :class="store.health.database && store.health.database.ok ? 'ok' : 'err'">{{ store.health.database ? (store.health.database.ok ? '正常' : store.health.database.error) : '-' }}</dd>
          <dt>目标存储</dt>
          <dd :class="store.health.storage && store.health.storage.ok ? 'ok' : 'err'">
            <template v-if="store.health.storage">
              {{ store.health.storage.ok ? `正常，剩余 ${bytes(store.health.storage.free_bytes)}` : store.health.storage.error }}
              <span class="muted" v-if="store.health.storage.fs_type">（{{ store.health.storage.fs_type }}）</span>
            </template>
          </dd>
          <dt>设备</dt><dd>{{ store.health.devices ? store.health.devices.attached : 0 }} 台在位</dd>
        </dl>
        <div class="row">
          <button class="small" @click="health" :disabled="busy !== ''">重新检查</button>
          <template v-if="mountControl">
            <button class="small" @click="mount" :disabled="busy !== '' || (store.health.storage && store.health.storage.ok)" title="按配置挂载 NAS（已挂载且正常时无需操作）">挂载 NAS</button>
            <button class="small" @click="remount" :disabled="busy !== ''" title="先卸载再挂载：NAS 掉线残留、只读或改了凭据后用">重新挂载</button>
            <button class="small danger" @click="unmount" :disabled="busy !== '' || transferring" title="有任务在传输时不可卸载">卸载</button>
          </template>
          <span class="muted">{{ ago(store.health.checked_at) }}</span>
        </div>
      </template>
    </div>
    <div class="card">
      <h2>任务</h2>
      <div class="big">{{ active }} <span class="muted" style="font-size:13px;font-weight:400">进行中</span></div>
      <dl class="kv">
        <template v-for="k in ['PENDING','QUEUED','TRANSFERRING','VERIFYING','COMPLETED','FAILED','INTERRUPTED','SKIPPED','CANCELLED']" :key="k">
          <dt><span class="badge" :class="k">{{ k }}</span></dt><dd>{{ taskCounts[k] || 0 }}</dd>
        </template>
      </dl>
    </div>
    <div class="card">
      <h2>控制</h2>
      <p class="muted" v-if="!writable">当前模式禁止写入目标，只能生成计划。</p>
      <div class="row">
        <button @click="plan" :disabled="busy !== ''">生成计划</button>
        <button class="primary" @click="start" :disabled="!writable || busy !== ''">开始备份</button>
        <button @click="pause" :disabled="!writable || busy !== '' || !sched || sched.paused">暂停</button>
        <button @click="resume" :disabled="!writable || busy !== '' || !sched || !sched.paused">恢复</button>
      </div>
      <p v-if="sched" class="muted">调度器：{{ sched.running ? '运行' : '停止' }}{{ sched.paused ? '，已暂停' : '' }}</p>
      <div class="row">
        <button class="small" @click="rescan" :disabled="busy !== ''" title="重新枚举相机与卷（插上没反应时用）">重新检测设备</button>
        <button class="small" @click="retryFailed" :disabled="!writable || busy !== '' || !retryable" title="把全部 FAILED / INTERRUPTED 任务重新入队，含已用尽自动重试的">重试失败任务{{ retryable ? `（${retryable}）` : '' }}</button>
      </div>
    </div>
  </div>

  <div class="card" style="margin-top:12px" v-if="current">
    <h2>正在传输 · 任务 #{{ current.task_id }}</h2>
    <div class="mono">{{ current.relative_path }}</div>
    <div class="row">
      <div class="bar" style="flex:1"><i :style="{ width: Math.min(100, current.percent || 0) + '%' }"></i></div>
      <span class="mono">{{ percent(current.percent) }}</span>
    </div>
    <div class="row muted">
      <span>{{ bytes(current.bytes) }} / {{ bytes(current.total) }}</span>
      <span>{{ speed(current.bytes_per_sec) }}</span>
      <span v-if="current.elapsed_seconds != null">已用 {{ duration(current.elapsed_seconds) }}</span>
      <span v-if="current.bytes_per_sec && current.total > current.bytes">预计剩余 {{ duration((current.total - current.bytes) / current.bytes_per_sec) }}</span>
    </div>
  </div>

  <div class="card" style="margin-top:12px">
    <h2>最近事件</h2>
    <table>
      <tbody>
        <tr v-for="e in store.events.slice(0, 8)" :key="e.id || e.created_at + e.type">
          <td class="muted" style="white-space:nowrap">{{ ago(e.created_at) }}</td>
          <td><span class="badge" :class="e.level">{{ e.type }}</span></td>
          <td>{{ e.message }}</td>
        </tr>
        <tr v-if="!store.events.length"><td class="empty" colspan="3">暂无事件</td></tr>
      </tbody>
    </table>
  </div>
</template>
