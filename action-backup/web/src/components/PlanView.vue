<script setup>
import { ref, computed } from 'vue'
import { store, refreshPlan, refreshStatus, notify, errorText } from '../store.js'
import { post } from '../api.js'
import { bytes, time } from '../format.js'

const busy = ref('')
const filter = ref('all')
const writable = computed(() => store.mode && store.mode.destination_write_allowed)
const items = computed(() => {
  const p = store.plan
  if (!p) return []
  return filter.value === 'all' ? p.items : p.items.filter((i) => i.action === filter.value)
})

async function generate() {
  busy.value = 'plan'
  try {
    store.plan = await post('/api/v1/backups/plan')
    notify('计划已生成')
  } catch (e) {
    notify(errorText(e), 'error')
  } finally {
    busy.value = ''
  }
}
async function start() {
  busy.value = 'start'
  try {
    const r = await post('/api/v1/backups/start')
    store.plan = r.plan
    notify(`已入队：新建 ${r.tasks_created}，重排 ${r.tasks_requeued}，冲突 ${r.conflicts}`)
    await refreshStatus()
    store.tab = 'tasks'
  } catch (e) {
    notify(errorText(e), 'error')
  } finally {
    busy.value = ''
  }
}
</script>

<template>
  <div class="row">
    <button @click="generate" :disabled="busy !== ''">{{ busy === 'plan' ? '扫描中…' : '生成计划' }}</button>
    <button class="primary" @click="start" :disabled="!writable || busy !== ''">扫描并开始备份</button>
    <button class="small" @click="refreshPlan">载入最近计划</button>
    <span class="muted" v-if="!writable">当前模式只规划不写入</span>
  </div>
  <div class="empty card" v-if="!store.plan">尚未生成计划。</div>
  <template v-else>
    <div class="grid">
      <div class="card"><h2>上传</h2><div class="big ok">{{ store.plan.upload.files }}</div><div class="muted">{{ bytes(store.plan.upload.bytes) }}</div></div>
      <div class="card"><h2>跳过</h2><div class="big muted">{{ store.plan.skip.files }}</div><div class="muted">{{ bytes(store.plan.skip.bytes) }}</div></div>
      <div class="card"><h2>冲突</h2><div class="big" :class="store.plan.conflict.files ? 'warn' : 'muted'">{{ store.plan.conflict.files }}</div><div class="muted">{{ bytes(store.plan.conflict.bytes) }}</div></div>
      <div class="card">
        <h2>扫描</h2>
        <dl class="kv">
          <dt>生成于</dt><dd>{{ time(store.plan.generated_at) }}</dd>
          <dt>设备 / 卷</dt><dd>{{ store.plan.devices }} / {{ store.plan.volumes }}</dd>
          <dt>扫描文件</dt><dd>{{ store.plan.scanned_files }}</dd>
          <dt>目标已检查</dt><dd :class="store.plan.target_checked ? 'ok' : 'warn'">{{ store.plan.target_checked ? '是' : '否（目标不可用，未做跳过判断）' }}</dd>
          <dt>耗时</dt><dd>{{ store.plan.duration_ms }} ms</dd>
        </dl>
      </div>
    </div>
    <div class="card warn" v-if="store.plan.warnings && store.plan.warnings.length" style="margin-top:12px">
      <h2>警告</h2>
      <ul><li v-for="w in store.plan.warnings" :key="w">{{ w }}</li></ul>
    </div>
    <div class="card" style="margin-top:12px">
      <div class="row">
        <h2 style="margin:0">条目</h2>
        <select v-model="filter">
          <option value="all">全部（{{ store.plan.items.length }}）</option>
          <option value="upload">upload</option>
          <option value="skip">skip</option>
          <option value="conflict">conflict</option>
        </select>
      </div>
      <div class="wrap">
        <table>
          <thead><tr><th>动作</th><th>卷</th><th>路径</th><th class="num">大小</th><th>原因</th><th>目标</th></tr></thead>
          <tbody>
            <tr v-for="it in items" :key="it.volume + it.path">
              <td><span class="badge" :class="it.action">{{ it.action }}</span></td>
              <td>{{ it.volume }}</td>
              <td class="mono">{{ it.path }}</td>
              <td class="num">{{ bytes(it.size) }}</td>
              <td>{{ it.reason }}</td>
              <td class="mono muted">{{ it.destination || '-' }}</td>
            </tr>
            <tr v-if="!items.length"><td colspan="6" class="empty">无条目</td></tr>
          </tbody>
        </table>
      </div>
    </div>
  </template>
</template>
