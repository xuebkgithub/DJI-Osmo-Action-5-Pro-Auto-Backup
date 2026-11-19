<script setup>
import { onMounted, computed } from 'vue'
import { store, boot, logout, refreshAll, notify, errorText, dismissInstalling } from './store.js'
import ModeBanner from './components/ModeBanner.vue'
import LoginView from './components/LoginView.vue'
import OverviewView from './components/OverviewView.vue'
import DevicesView from './components/DevicesView.vue'
import PlanView from './components/PlanView.vue'
import TasksView from './components/TasksView.vue'
import EventsView from './components/EventsView.vue'
import SettingsView from './components/SettingsView.vue'
import TestPanel from './components/TestPanel.vue'

const tabs = computed(() => {
  const t = [
    ['overview', '总览'],
    ['devices', '设备与卷'],
    ['plan', '计划'],
    ['tasks', '任务'],
    ['events', '事件'],
    ['settings', '设置'],
  ]
  if (store.mode && store.mode.test_endpoints_enabled) t.push(['test', '测试面板'])
  return t
})

const views = { overview: OverviewView, devices: DevicesView, plan: PlanView, tasks: TasksView, events: EventsView, settings: SettingsView, test: TestPanel }

onMounted(boot)

async function doLogout() {
  try {
    await logout()
  } catch (e) {
    notify(errorText(e), 'error')
  }
}
const location = window.location
async function doRefresh() {
  await refreshAll()
  notify('已刷新')
}
</script>

<template>
  <ModeBanner v-if="store.mode" :mode="store.mode" />
  <template v-if="!store.booted">
    <main class="empty">加载中…</main>
  </template>
  <LoginView v-else-if="!store.session" />
  <template v-else>
    <header class="top">
      <h1>Action Backup</h1>
      <span class="muted" v-if="store.status">v{{ store.status.version }}</span>
      <button
        v-if="store.update && store.update.update_available && store.update.latest"
        class="small update-badge"
        title="查看新版本说明"
        @click="store.tab = 'settings'"
      >有新版本 v{{ store.update.latest.version }}</button>
      <div class="spacer"></div>
      <div class="meta">
        <span><i class="dot" :class="store.sse"></i>{{ store.sse === 'open' ? '实时连接' : store.sse === 'error' ? '重连中' : '未连接' }}</span>
        <span v-if="store.session.username">{{ store.session.username }}</span>
        <button class="small" @click="doRefresh">刷新</button>
        <button class="small" v-if="store.authEnabled" @click="doLogout">退出</button>
      </div>
    </header>
    <nav class="tabs">
      <button v-for="[k, label] in tabs" :key="k" :class="{ active: store.tab === k, test: k === 'test' }" @click="store.tab = k">{{ label }}</button>
    </nav>
    <main>
      <component :is="views[store.tab]" />
    </main>
  </template>
  <div v-if="store.installing" class="overlay">
    <div class="card">
      <h2 class="plain">正在更新到 v{{ store.installing.version }}</h2>
      <template v-if="!store.installing.timedOut">
        <p>安装包已下载并通过签名校验，正在等待设备上的安装任务接手（每分钟检查一次）。</p>
        <p>安装时服务会重启，页面会自动刷新，请不要关闭设备电源。</p>
        <div class="bar indeterminate"><i></i></div>
      </template>
      <template v-else>
        <p class="warn">已等待超过 6 分钟仍未检测到服务重启。</p>
        <p>请手动刷新页面查看结果；如果版本没有变化，到「设置 → 软件更新」看失败原因，或在设备上执行
          <code>journalctl -u action-backup-update</code>。</p>
        <div class="row"><button class="primary" @click="() => location.reload()">刷新页面</button><button @click="dismissInstalling">关闭</button></div>
      </template>
    </div>
  </div>
  <div v-if="store.toast" class="toast" :class="store.toast.kind">{{ store.toast.msg }}</div>
</template>
