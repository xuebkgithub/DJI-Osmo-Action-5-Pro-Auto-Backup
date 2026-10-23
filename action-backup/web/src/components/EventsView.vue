<script setup>
import { ref, computed } from 'vue'
import { store, refreshEvents } from '../store.js'
import { time } from '../format.js'

const q = ref('')
const level = ref('')
const limit = ref(100)
const items = computed(() =>
  store.events.filter((e) => (!level.value || e.level === level.value) && (!q.value || (e.type + ' ' + e.message).toLowerCase().includes(q.value.toLowerCase()))),
)
async function reload() {
  await refreshEvents(limit.value)
}
</script>

<template>
  <div class="row">
    <input type="text" v-model="q" placeholder="按类型或消息过滤" />
    <select v-model="level"><option value="">全部级别</option><option>info</option><option>warn</option><option>error</option></select>
    <select v-model.number="limit" @change="reload"><option :value="100">100</option><option :value="300">300</option><option :value="1000">1000</option></select>
    <button class="small" @click="reload">刷新</button>
    <span class="muted">实时事件通过 SSE 追加到顶部</span>
  </div>
  <div class="card wrap">
    <table>
      <thead><tr><th>时间</th><th>级别</th><th>类型</th><th>消息</th><th class="num">任务</th><th>数据</th></tr></thead>
      <tbody>
        <tr v-for="e in items" :key="e.id || e.created_at + e.type">
          <td style="white-space:nowrap" class="muted">{{ time(e.created_at) }}</td>
          <td><span class="badge" :class="e.level">{{ e.level }}</span></td>
          <td class="mono">{{ e.type }}</td>
          <td>{{ e.message }}</td>
          <td class="num">{{ e.task_id || '' }}</td>
          <td>
            <details v-if="e.data && Object.keys(e.data).length"><summary>详情</summary><pre>{{ JSON.stringify(e.data, null, 1) }}</pre></details>
          </td>
        </tr>
        <tr v-if="!items.length"><td colspan="6" class="empty">没有匹配的事件</td></tr>
      </tbody>
    </table>
  </div>
</template>
