<script setup>
import { ref, onMounted } from 'vue'
import { store, refreshMode, notify, errorText } from '../store.js'
import { get, patch, post } from '../api.js'
import { bytes } from '../format.js'

const cfg = ref(null)
const path = ref('')
const fields = ref([])
const form = ref({})
const saving = ref(false)
const testing = ref(false)
const storageResult = ref(null)

function valueAt(obj, key) {
  return key.split('.').reduce((o, k) => (o == null ? undefined : o[k]), obj)
}
async function load() {
  try {
    const r = await get('/api/v1/config')
    cfg.value = r.config
    path.value = r.path
    fields.value = r.patchable
    const f = {}
    for (const p of r.patchable) f[p.key] = valueAt(r.config, p.key)
    form.value = f
  } catch (e) {
    notify(errorText(e), 'error')
  }
}
onMounted(load)

async function save() {
  const changes = {}
  for (const p of fields.value) {
    const cur = valueAt(cfg.value, p.key)
    let v = form.value[p.key]
    if (p.kind === 'int') v = Number(v)
    if (v !== cur) changes[p.key] = v
  }
  if (!Object.keys(changes).length) {
    notify('没有改动')
    return
  }
  saving.value = true
  try {
    const r = await patch('/api/v1/config', changes)
    cfg.value = r.config
    notify(`已保存 ${Object.keys(changes).length} 项并写回 ${r.path || '内存'}`)
    await refreshMode()
    await load()
  } catch (e) {
    notify(errorText(e), 'error')
  } finally {
    saving.value = false
  }
}
async function testStorage() {
  testing.value = true
  storageResult.value = null
  try {
    storageResult.value = await post('/api/v1/storage/test')
  } catch (e) {
    notify(errorText(e), 'error')
  } finally {
    testing.value = false
  }
}
</script>

<template>
  <div class="grid" v-if="cfg">
    <div class="card" style="grid-column: 1 / -1">
      <h2>可在线修改的配置</h2>
      <p class="muted">修改立即生效并写回配置文件 <code>{{ path || '（内存配置，未写文件）' }}</code>，注释保留。其它项请编辑文件后重启服务。</p>
      <form class="inline" @submit.prevent="save">
        <label v-for="p in fields" :key="p.key">
          <span>{{ p.key }}</span>
          <select v-if="p.kind === 'bool'" :name="p.key" v-model="form[p.key]"><option :value="true">true</option><option :value="false">false</option></select>
          <select v-else-if="p.choices" :name="p.key" v-model="form[p.key]"><option v-for="c in p.choices" :key="c" :value="c">{{ c }}</option></select>
          <input v-else-if="p.kind === 'int'" :name="p.key" type="number" v-model.number="form[p.key]" />
          <input v-else :name="p.key" type="text" v-model="form[p.key]" />
          <span class="muted" style="font-size:11px">{{ p.doc }}</span>
        </label>
        <div><button class="primary" type="submit" :disabled="saving">{{ saving ? '保存中…' : '保存' }}</button></div>
      </form>
    </div>

    <div class="card">
      <h2>目标存储</h2>
      <dl class="kv">
        <dt>类型</dt><dd>{{ cfg.storage.type }}</dd>
        <dt v-if="cfg.storage.share">共享</dt><dd v-if="cfg.storage.share" class="mono">{{ cfg.storage.share }}</dd>
        <dt>挂载点 / 根</dt><dd class="mono">{{ cfg.storage.mount_point || cfg.storage.local_root }}</dd>
        <dt>destination_root</dt><dd class="mono">{{ cfg.storage.destination_root }}</dd>
        <dt>destination_layout</dt><dd class="mono">{{ cfg.storage.destination_layout }}</dd>
        <dt>SMB</dt><dd>{{ cfg.storage.smb_version }}</dd>
      </dl>
      <div class="row"><button @click="testStorage" :disabled="testing">{{ testing ? '检测中…' : '测试连接（只读）' }}</button></div>
      <dl class="kv" v-if="storageResult">
        <dt>结果</dt><dd :class="storageResult.ok ? 'ok' : 'err'">{{ storageResult.ok ? '可用' : storageResult.error }}</dd>
        <dt>文件系统</dt><dd>{{ storageResult.fs_type || '-' }} <span class="muted">{{ storageResult.source }}</span></dd>
        <dt>剩余空间</dt><dd>{{ bytes(storageResult.free_bytes) }}</dd>
        <dt>根目录存在</dt><dd>{{ storageResult.root_exists ? '是' : '否（首次上传时创建）' }}</dd>
        <dt>允许写入</dt><dd>{{ storageResult.write_allowed ? '是' : '否（当前模式）' }}</dd>
        <dt>耗时</dt><dd>{{ storageResult.latency_ms }} ms</dd>
      </dl>
    </div>

    <div class="card">
      <h2>备份策略</h2>
      <dl class="kv">
        <dt>源目录</dt><dd>{{ cfg.backup.source_directory }}</dd>
        <dt>扩展名</dt><dd>{{ cfg.backup.file_extensions.join(', ') }}</dd>
        <dt>内置 / microSD</dt><dd>{{ cfg.backup.include_internal !== false ? '是' : '否' }} / {{ cfg.backup.include_sdcard !== false ? '是' : '否' }}</dd>
        <dt>缓冲区</dt><dd>{{ cfg.backup.buffer_size_mb }} MiB</dd>
        <dt>断点</dt><dd>每 {{ cfg.backup.checkpoint_interval_sec }} s 或 {{ cfg.backup.checkpoint_bytes_mb }} MiB</dd>
        <dt>删除源文件</dt><dd :class="cfg.backup.delete_source ? 'err' : 'ok'">{{ cfg.backup.delete_source ? '是（禁止）' : '否' }}</dd>
      </dl>
    </div>

    <div class="card">
      <h2>服务与认证</h2>
      <dl class="kv">
        <dt>监听</dt><dd class="mono">{{ cfg.server.listen }}</dd>
        <dt>认证</dt><dd>{{ cfg.server.auth.enabled ? '启用' : '未启用' }}</dd>
        <dt>用户名</dt><dd>{{ cfg.server.auth.username || '-' }}</dd>
        <dt>口令哈希</dt><dd class="mono">{{ cfg.server.auth.password_hash || '（未设置）' }}</dd>
        <dt>会话有效期</dt><dd>{{ cfg.server.auth.session_ttl_hours }} 小时</dd>
        <dt>登录限速</dt><dd>每分钟 {{ cfg.server.auth.login_rate_limit_per_min }} 次</dd>
        <dt>数据库</dt><dd class="mono">{{ cfg.database.path }}</dd>
        <dt>日志</dt><dd>{{ cfg.logging.level }} / {{ cfg.logging.format }}</dd>
      </dl>
      <p class="muted">修改口令：在设备上执行 <code>action-backup passwd --config {{ path || '/etc/action-backup/config.yaml' }}</code>，然后重启服务。</p>
    </div>
  </div>
  <div class="empty" v-else>加载中…</div>
</template>
