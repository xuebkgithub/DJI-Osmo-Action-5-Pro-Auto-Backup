<script setup>
import { onMounted, computed } from 'vue'
import { store, boot, logout, refreshAll, notify, errorText } from './store.js'
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
  <div v-if="store.toast" class="toast" :class="store.toast.kind">{{ store.toast.msg }}</div>
</template>
