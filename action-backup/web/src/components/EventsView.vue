<script setup>
import { ref, computed } from 'vue'
import { store, refreshEvents } from '../store.js'
import { time } from '../format.js'
import { TYPE_LABELS, typeLabel, levelLabel, eventMessage, eventLang as lang } from '../eventText.js'

const q = ref('')
const level = ref('')
const type = ref('')
const limit = ref(100)
const zh = computed(() => lang.value === 'zh')

// 已登记的类型 + 当前列表里出现过的未登记类型
const types = computed(() => {
  const known = Object.keys(TYPE_LABELS)
  const extra = [...new Set(store.events.map((e) => e.type))].filter((t) => !TYPE_LABELS[t] && t !== 'backup.progress').sort()
  return [...known, ...extra]
})

const rows = computed(() =>
  store.events.map((e) => ({ e, typeText: zh.value ? typeLabel(e.type) : e.type, message: zh.value ? eventMessage(e) : e.message })),
)
const items = computed(() => {
  const kw = q.value.trim().toLowerCase()
  return rows.value.filter(
    ({ e, typeText, message }) =>
      (!level.value || e.level === level.value) &&
      (!type.value || e.type === type.value) &&
      // 中英文都参与搜索：切到中文后仍能用 task.failed 或原文里的路径找到
      (!kw || [e.type, e.message, typeText, message].join(' ').toLowerCase().includes(kw)),
  )
})
async function reload() {
  await refreshEvents(limit.value)
}
</script>

<template>
  <div class="row">
    <input type="text" v-model="q" placeholder="按类型或消息过滤" />
    <select v-model="level">
      <option value="">全部级别</option>
      <option v-for="l in ['info', 'warn', 'error']" :key="l" :value="l">{{ zh ? levelLabel(l) : l }}</option>
    </select>
    <select v-model="type">
      <option value="">全部类型</option>
      <option v-for="t in types" :key="t" :value="t">{{ zh ? typeLabel(t) : t }}</option>
    </select>
    <select v-model.number="limit" @change="reload"><option :value="100">100</option><option :value="300">300</option><option :value="1000">1000</option></select>
    <select v-model="lang" title="事件类型与消息的显示语言"><option value="zh">中文</option><option value="raw">原文</option></select>
    <button class="small" @click="reload">刷新</button>
    <span class="muted">实时事件通过 SSE 追加到顶部</span>
  </div>
  <div class="card wrap">
    <table>
      <thead><tr><th>时间</th><th>级别</th><th>类型</th><th>消息</th><th class="num">任务</th><th>数据</th></tr></thead>
      <tbody>
        <tr v-for="{ e, typeText, message } in items" :key="e.id || e.created_at + e.type">
          <td style="white-space:nowrap" class="muted">{{ time(e.created_at) }}</td>
          <td><span class="badge" :class="e.level">{{ zh ? levelLabel(e.level) : e.level }}</span></td>
          <td :class="{ mono: !zh }" :title="zh ? e.type : ''" style="white-space:nowrap">{{ typeText }}</td>
          <td :title="zh && message !== e.message ? e.message : ''">{{ message }}</td>
          <td class="num">{{ e.task_id || '' }}</td>
          <td>
            <details v-if="e.data && Object.keys(e.data).length"><summary style="white-space:nowrap">详情</summary><pre>{{ JSON.stringify(e.data, null, 1) }}</pre></details>
          </td>
        </tr>
        <tr v-if="!items.length"><td colspan="6" class="empty">没有匹配的事件</td></tr>
      </tbody>
    </table>
  </div>
</template>
