<script setup>
import { ref, computed, onMounted, watch } from 'vue'
import { store, refreshStatus, notify, errorText } from '../store.js'
import { get, post } from '../api.js'
import { bytes, percent, speed, time } from '../format.js'

const STATUSES = ['', 'PENDING', 'QUEUED', 'TRANSFERRING', 'VERIFYING', 'COMPLETED', 'FAILED', 'INTERRUPTED', 'SKIPPED', 'CANCELLED']
const status = ref('')
const limit = ref(50)
const offset = ref(0)
const total = ref(0)
const items = ref([])
const loading = ref(false)
const expanded = ref(null)
const writable = computed(() => store.mode && store.mode.destination_write_allowed)

async function load() {
  loading.value = true
  try {
    const q = `limit=${limit.value}&offset=${offset.value}${status.value ? '&status=' + status.value : ''}`
    const r = await get(`/api/v1/tasks?${q}`)
    items.value = r.items || []
    total.value = r.total
  } catch (e) {
    notify(errorText(e), 'error')
  } finally {
    loading.value = false
  }
}
onMounted(load)
watch([status, limit], () => {
  offset.value = 0
  load()
})
// 事件驱动刷新：任务状态变化事件到达后重新拉列表
let timer = null
watch(
  () => store.events.length && store.events[0],
  (e) => {
    if (e && /^(task\.|backup\.(started|completed))/.test(e.type)) {
      clearTimeout(timer)
      timer = setTimeout(load, 300)
    }
  },
)

function progressOf(t) {
  return store.progress[t.id] || (store.status && store.status.scheduler && store.status.scheduler.current && store.status.scheduler.current.task_id === t.id ? store.status.scheduler.current : null)
}
function pct(t) {
  const p = progressOf(t)
  if (p) return p.percent
  if (t.status === 'COMPLETED') return 100
  return t.file_size ? (t.bytes_transferred / t.file_size) * 100 : 0
}
async function act(t, what) {
  try {
    await post(`/api/v1/tasks/${t.id}/${what}`)
    notify(`任务 #${t.id} 已${what === 'cancel' ? '取消' : '重试'}`)
    await Promise.all([load(), refreshStatus()])
  } catch (e) {
    notify(errorText(e), 'error')
  }
}
const canCancel = (t) => ['PENDING', 'QUEUED', 'TRANSFERRING', 'VERIFYING', 'INTERRUPTED', 'FAILED'].includes(t.status)
const canRetry = (t) => ['FAILED', 'INTERRUPTED'].includes(t.status)
</script>

<template>
  <div class="row">
    <select v-model="status">
      <option v-for="s in STATUSES" :key="s" :value="s">{{ s || '全部状态' }}</option>
    </select>
    <select v-model.number="limit"><option :value="20">20</option><option :value="50">50</option><option :value="100">100</option><option :value="200">200</option></select>
    <button class="small" @click="load" :disabled="loading">刷新</button>
    <span class="muted">共 {{ total }} 条</span>
    <div class="spacer" style="flex:1"></div>
    <button class="small" :disabled="offset === 0" @click="offset = Math.max(0, offset - limit); load()">上一页</button>
    <button class="small" :disabled="offset + limit >= total" @click="offset += limit; load()">下一页</button>
  </div>
  <div class="card wrap">
    <table>
      <thead>
        <tr><th class="num">#</th><th>状态</th><th>文件</th><th class="num">大小</th><th>进度</th><th class="num">重试</th><th>校验</th><th>更新</th><th></th></tr>
      </thead>
      <tbody>
        <template v-for="t in items" :key="t.id">
          <tr @click="expanded = expanded === t.id ? null : t.id" style="cursor:pointer">
            <td class="num">{{ t.id }}</td>
            <td><span class="badge" :class="t.status">{{ t.status }}</span></td>
            <td class="mono">{{ t.volume_type }}/{{ t.relative_path }}</td>
            <td class="num">{{ bytes(t.file_size) }}</td>
            <td>
              <div class="row" style="margin:0">
                <div class="bar"><i :style="{ width: Math.min(100, pct(t)) + '%' }"></i></div>
                <span class="mono muted">{{ percent(pct(t)) }}</span>
                <span class="mono muted" v-if="progressOf(t)">{{ speed(progressOf(t).bytes_per_sec) }}</span>
              </div>
            </td>
            <td class="num">{{ t.retry_count }}</td>
            <td>{{ t.verify_mode }}</td>
            <td class="muted">{{ time(t.completed_at || t.started_at || t.created_at) }}</td>
            <td style="white-space:nowrap" @click.stop>
              <button class="small" v-if="writable && canRetry(t)" @click="act(t, 'retry')">重试</button>
              <button class="small danger" v-if="writable && canCancel(t)" @click="act(t, 'cancel')">取消</button>
            </td>
          </tr>
          <tr v-if="expanded === t.id">
            <td colspan="9">
              <dl class="kv">
                <dt>目标路径</dt><dd class="mono">{{ t.destination_path }}</dd>
                <dt>设备 / 卷</dt><dd class="mono">{{ t.device_key }} / {{ t.volume_key }}</dd>
                <dt>指纹</dt><dd class="mono">{{ t.fingerprint }}</dd>
                <dt>已传输</dt><dd>{{ bytes(t.bytes_transferred) }}</dd>
                <dt v-if="t.checksum_sha256">SHA-256</dt><dd v-if="t.checksum_sha256" class="mono">{{ t.checksum_sha256 }}</dd>
                <dt v-if="t.last_error">最近错误</dt><dd v-if="t.last_error" class="err">{{ t.last_error }}</dd>
                <dt>创建 / 开始 / 完成</dt><dd>{{ time(t.created_at) }} / {{ time(t.started_at) }} / {{ time(t.completed_at) }}</dd>
              </dl>
            </td>
          </tr>
        </template>
        <tr v-if="!items.length"><td colspan="9" class="empty">{{ loading ? '加载中…' : '没有任务' }}</td></tr>
      </tbody>
    </table>
  </div>
</template>
