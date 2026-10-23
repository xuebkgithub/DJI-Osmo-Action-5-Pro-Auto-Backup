<script setup>
import { ref, onMounted } from 'vue'
import { store, notify, errorText } from '../store.js'
import { get, post, del } from '../api.js'

const sim = ref([])
const faults = ref([])
const POINTS = ['source.open', 'source.read', 'device.detach', 'dest.probe', 'dest.open', 'dest.write', 'dest.sync', 'dest.rename', 'part.corrupt', 'verify.hash', 'db.commit', 'process.crash']
const KINDS = ['error', 'delay', 'stall', 'throttle', 'short_write', 'corrupt', 'disconnect', 'mismatch', 'crash']
const ERRNOS = ['EIO', 'ENOSPC', 'ENODEV', 'EACCES', 'ENOTCONN', 'ETIMEDOUT', 'EBUSY']
const form = ref({ id: '', point: 'dest.write', kind: 'error', errno: 'EIO', ms: 2000, bytes_per_sec: 1048576, offset: 0, length: 4096, trigger: 'once', nth_call: 1, after_bytes: 1048576, probability: 0.1, times: 1, after: '', path_glob: '' })
const advanced = ref('')

async function loadSim() {
  try {
    sim.value = await get('/api/v1/test/sim/devices')
  } catch (e) {
    if (e.status !== 404) notify(errorText(e), 'error')
  }
}
async function loadFaults() {
  try {
    faults.value = await get('/api/v1/test/faults')
  } catch (e) {
    if (e.status !== 404) notify(errorText(e), 'error')
  }
}
onMounted(() => {
  loadSim()
  loadFaults()
})

async function toggle(d) {
  try {
    await post(`/api/v1/test/sim/devices/${d.key}/${d.attached ? 'detach' : 'attach'}`)
    notify(`${d.key} 已${d.attached ? '拔出' : '插入'}`)
    await loadSim()
  } catch (e) {
    notify(errorText(e), 'error')
  }
}

function buildRule() {
  const f = form.value
  const rule = { id: f.id || `${f.point.replace('.', '-')}-${f.kind}-${Date.now().toString(36)}`, point: f.point, action: { kind: f.kind }, trigger: {}, match: {} }
  if (f.kind === 'error') rule.action.errno = f.errno
  if (f.kind === 'delay' || f.kind === 'stall') rule.action.ms = Number(f.ms)
  if (f.kind === 'throttle') rule.action.bytes_per_sec = Number(f.bytes_per_sec)
  if (f.kind === 'corrupt') {
    rule.action.offset = Number(f.offset)
    rule.action.length = Number(f.length)
  }
  switch (f.trigger) {
    case 'once': rule.trigger.once = true; break
    case 'nth_call': rule.trigger.nth_call = Number(f.nth_call); break
    case 'after_bytes': rule.trigger.after_bytes = Number(f.after_bytes); break
    case 'probability': rule.trigger.probability = Number(f.probability); break
    case 'times': rule.trigger.times = Number(f.times); break
    case 'always': break
  }
  if (f.after) rule.trigger.after = f.after
  if (f.path_glob) rule.match.path_glob = f.path_glob
  return rule
}
async function addRule() {
  try {
    const rule = advanced.value.trim() ? JSON.parse(advanced.value) : buildRule()
    faults.value = await post('/api/v1/test/faults', rule)
    notify(`规则 ${rule.id} 已添加`)
    advanced.value = ''
  } catch (e) {
    notify(errorText(e), 'error')
  }
}
async function removeRule(id) {
  try {
    faults.value = await del(`/api/v1/test/faults/${encodeURIComponent(id)}`)
  } catch (e) {
    notify(errorText(e), 'error')
  }
}
async function resetRules() {
  try {
    faults.value = await post('/api/v1/test/faults/reset')
    notify('已清空规则')
  } catch (e) {
    notify(errorText(e), 'error')
  }
}
function fillPreset(name) {
  const presets = {
    'nas-drop-midfile': { point: 'dest.write', kind: 'error', errno: 'ENOTCONN', trigger: 'after_bytes', after_bytes: 2 * 1048576 },
    'slow-nas': { point: 'dest.write', kind: 'throttle', bytes_per_sec: 512 * 1024, trigger: 'always' },
    'camera-unplug': { point: 'source.read', kind: 'disconnect', trigger: 'after_bytes', after_bytes: 1048576 },
    'verify-mismatch': { point: 'verify.hash', kind: 'mismatch', trigger: 'once' },
    'crash-mid-transfer': { point: 'dest.write', kind: 'crash', trigger: 'after_bytes', after_bytes: 1048576 },
  }
  form.value = { ...form.value, id: name, ...presets[name] }
}
</script>

<template>
  <div class="card warn" style="margin-bottom:12px">
    <strong>测试面板</strong>：仅 faultinject 构建且非 production 模式可见。这里的操作会影响正在进行的备份。
  </div>
  <div class="grid">
    <div class="card" v-if="sim.length">
      <h2>模拟设备</h2>
      <table>
        <tbody>
          <tr v-for="d in sim" :key="d.key">
            <td class="mono">{{ d.key }}</td>
            <td><span class="badge" :class="d.attached ? 'info' : ''">{{ d.attached ? '已插入' : '已拔出' }}</span></td>
            <td class="right"><button class="small" @click="toggle(d)">{{ d.attached ? '拔出' : '插入' }}</button></td>
          </tr>
        </tbody>
      </table>
    </div>
    <div class="card" style="grid-column: 1 / -1" v-if="store.mode && store.mode.faultinject_build">
      <h2>故障规则</h2>
      <div class="row">
        <span class="muted">预设：</span>
        <button class="small" v-for="p in ['nas-drop-midfile','slow-nas','camera-unplug','verify-mismatch','crash-mid-transfer']" :key="p" @click="fillPreset(p)">{{ p }}</button>
      </div>
      <form class="inline" @submit.prevent="addRule">
        <label><span>id（可空，自动生成）</span><input type="text" v-model="form.id" /></label>
        <label><span>注入点</span><select v-model="form.point"><option v-for="p in POINTS" :key="p">{{ p }}</option></select></label>
        <label><span>动作</span><select v-model="form.kind"><option v-for="k in KINDS" :key="k">{{ k }}</option></select></label>
        <label v-if="form.kind === 'error'"><span>errno</span><select v-model="form.errno"><option v-for="e in ERRNOS" :key="e">{{ e }}</option></select></label>
        <label v-if="form.kind === 'delay' || form.kind === 'stall'"><span>毫秒</span><input type="number" v-model.number="form.ms" /></label>
        <label v-if="form.kind === 'throttle'"><span>bytes/s</span><input type="number" v-model.number="form.bytes_per_sec" /></label>
        <template v-if="form.kind === 'corrupt'">
          <label><span>offset（-1 为末尾）</span><input type="number" v-model.number="form.offset" /></label>
          <label><span>length</span><input type="number" v-model.number="form.length" /></label>
        </template>
        <label><span>触发</span>
          <select v-model="form.trigger"><option value="once">once</option><option value="nth_call">nth_call</option><option value="after_bytes">after_bytes</option><option value="probability">probability</option><option value="times">times</option><option value="always">always</option></select>
        </label>
        <label v-if="form.trigger === 'nth_call'"><span>第 N 次</span><input type="number" v-model.number="form.nth_call" /></label>
        <label v-if="form.trigger === 'after_bytes'"><span>累计字节</span><input type="number" v-model.number="form.after_bytes" /></label>
        <label v-if="form.trigger === 'probability'"><span>概率 0-1</span><input type="number" step="0.01" min="0" max="1" v-model.number="form.probability" /></label>
        <label v-if="form.trigger === 'times'"><span>最多次数</span><input type="number" v-model.number="form.times" /></label>
        <label><span>after（依赖规则 id）</span><input type="text" v-model="form.after" /></label>
        <label><span>path_glob</span><input type="text" v-model="form.path_glob" placeholder="DCIM/**/*_0002_D.MP4" /></label>
        <div><button class="primary" type="submit">添加规则</button></div>
      </form>
      <details style="margin-top:8px">
        <summary>或直接粘贴规则 JSON（优先于表单）</summary>
        <textarea v-model="advanced" rows="5" style="width:100%;font-family:var(--mono);font-size:12px;margin-top:6px" placeholder='{"id":"x","point":"dest.write","trigger":{"once":true},"action":{"kind":"error","errno":"EIO"}}'></textarea>
      </details>
      <div class="row" style="margin-top:10px">
        <h3 style="margin:0">当前规则（{{ faults.length }}）</h3>
        <button class="small" @click="loadFaults">刷新</button>
        <button class="small danger" @click="resetRules" :disabled="!faults.length">清空</button>
      </div>
      <div class="wrap">
        <table>
          <thead><tr><th>id</th><th>注入点</th><th>触发</th><th>动作</th><th>匹配</th><th class="num">调用</th><th class="num">命中</th><th></th></tr></thead>
          <tbody>
            <tr v-for="s in faults" :key="s.rule.id">
              <td class="mono">{{ s.rule.id }}</td>
              <td class="mono">{{ s.rule.point }}</td>
              <td class="mono">{{ JSON.stringify(s.rule.trigger) }}</td>
              <td class="mono">{{ JSON.stringify(s.rule.action) }}</td>
              <td class="mono">{{ s.rule.match && s.rule.match.path_glob || '*' }}</td>
              <td class="num">{{ s.calls }}</td>
              <td class="num" :class="s.hits ? 'warn' : ''">{{ s.hits }}</td>
              <td class="right"><button class="small danger" @click="removeRule(s.rule.id)">删除</button></td>
            </tr>
            <tr v-if="!faults.length"><td colspan="8" class="empty">没有规则</td></tr>
          </tbody>
        </table>
      </div>
    </div>
  </div>
</template>
