<script setup>
import { ref } from 'vue'
import { login } from '../store.js'

const username = ref('')
const password = ref('')
const error = ref('')
const busy = ref(false)

async function submit() {
  error.value = ''
  busy.value = true
  try {
    await login(username.value, password.value)
  } catch (e) {
    if (e.status === 429) error.value = '尝试过于频繁，请一分钟后再试'
    else if (e.status === 401) error.value = '用户名或密码错误'
    else error.value = e.message || String(e)
  } finally {
    busy.value = false
    password.value = ''
  }
}
</script>

<template>
  <main>
    <form class="card login" @submit.prevent="submit">
      <h1 style="font-size:18px;margin:0 0 6px">Action Backup</h1>
      <p class="muted" style="margin:0">DJI Osmo Action 5 Pro 自动备份</p>
      <label>用户名</label>
      <input type="text" v-model="username" autocomplete="username" required autofocus />
      <label>密码</label>
      <input type="password" v-model="password" autocomplete="current-password" required />
      <div class="row" style="margin-top:14px">
        <button class="primary" type="submit" :disabled="busy">{{ busy ? '登录中…' : '登录' }}</button>
      </div>
      <div class="err" v-if="error">{{ error }}</div>
    </form>
  </main>
</template>
