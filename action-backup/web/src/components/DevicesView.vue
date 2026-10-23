<script setup>
import { ref } from 'vue'
import { store, refreshDevices, notify, errorText } from '../store.js'
import { post } from '../api.js'
import { bytes, ago } from '../format.js'

const busy = ref(false)
async function rescan() {
  busy.value = true
  try {
    store.devices = await post('/api/v1/devices/rescan')
    notify('已重新扫描')
  } catch (e) {
    if (e.code === 'NOT_IMPLEMENTED') {
      notify('当前后端不支持主动扫描，等待自动发现', 'error')
      await refreshDevices()
    } else notify(errorText(e), 'error')
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="row">
    <button @click="rescan" :disabled="busy">重新扫描</button>
    <span class="muted">{{ store.devices.length }} 台设备在位</span>
  </div>
  <div class="empty card" v-if="!store.devices.length">没有检测到相机。请用 USB 连接 Osmo Action 5 Pro<span v-if="store.mode && store.mode.test_endpoints_enabled">，或在测试面板模拟插入</span>。</div>
  <div class="card" v-for="d in store.devices" :key="d.key" style="margin-bottom:12px">
    <h2 class="plain">{{ d.name }} <span class="badge warn" v-if="d.weak_identity">弱身份</span></h2>
    <dl class="kv">
      <dt>序列号</dt><dd class="mono">{{ d.serial || '-' }}</dd>
      <dt>USB</dt><dd class="mono">{{ d.vendor_id }}:{{ d.product_id }}</dd>
      <dt>设备键</dt><dd class="mono">{{ d.key }}</dd>
      <dt>最近看到</dt><dd>{{ ago(d.last_seen_at) }}</dd>
    </dl>
    <h3>存储卷</h3>
    <div class="wrap">
      <table>
        <thead><tr><th>类型</th><th>卷标</th><th class="num">LUN</th><th>文件系统</th><th class="num">容量</th><th>挂载点</th></tr></thead>
        <tbody>
          <tr v-for="v in d.volumes" :key="v.key">
            <td><span class="badge">{{ v.type }}</span></td>
            <td>{{ v.label || '-' }}</td>
            <td class="num">{{ v.lun }}</td>
            <td>{{ v.filesystem || '-' }}</td>
            <td class="num">{{ bytes(v.size_bytes) }}</td>
            <td class="mono">{{ v.mount_path }}</td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>
