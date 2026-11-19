<script setup>
import { ref, computed, onMounted } from 'vue'
import { store, refreshMode, refreshUpdate, checkUpdate, applyUpdate, notify, errorText } from '../store.js'
import { get, patch, post } from '../api.js'
import { bytes, time, ago } from '../format.js'

const cfg = ref(null)
const path = ref('')
const fields = ref([])
const form = ref({})
const saving = ref(false)
const testing = ref(false)
const storageResult = ref(null)
const notifyTesting = ref('')
const notifyResult = ref(null)

const generalFields = computed(() => fields.value.filter((p) => !p.group || p.group === 'general'))
const notifyFields = computed(() => fields.value.filter((p) => p.group === 'notify'))
const notifySection = (prefix) => notifyFields.value.filter((p) => p.key.startsWith(prefix))
const updateFields = computed(() => fields.value.filter((p) => p.group === 'update'))
const checking = ref(false)
const upd = computed(() => store.update)

const inst = computed(() => (upd.value && upd.value.install) || { stage: 'idle' })
const installBusy = computed(() => ['downloading', 'verifying', 'waiting_idle', 'requested'].includes(inst.value.stage))
const applying = ref(false)
const STAGE_TEXT = { downloading: '正在下载安装包', verifying: '正在校验签名与哈希', waiting_idle: '安装包已就绪，等待备份任务结束后安装', requested: '已提交安装请求，等待安装任务接手' }
const downloadPercent = computed(() => (inst.value.total_bytes ? Math.min(100, (inst.value.downloaded_bytes / inst.value.total_bytes) * 100) : 0))

async function doApplyUpdate() {
  const v = upd.value.latest.version
  if (!window.confirm(`更新到 v${v}？\n\n安装时服务会重启（约 1 分钟不可用）。有备份在进行时会等它结束再安装；安装失败会自动回滚到当前版本。`)) return
  applying.value = true
  try {
    await applyUpdate()
    notify(`开始下载 v${v}`)
  } catch (e) {
    notify(errorText(e), 'error')
  } finally {
    applying.value = false
  }
}

// doCheckUpdate 先保存未提交的改动（例如刚切了通道），再立即检查一次。
async function doCheckUpdate() {
  if (Object.keys(pendingChanges()).length && !(await save())) return
  checking.value = true
  try {
    const u = await checkUpdate()
    if (u.dev_build) notify('已取回最新版本信息（当前为开发构建，不做更新判定）')
    else notify(u.update_available ? `发现新版本 v${u.latest.version}` : '已是最新版本')
  } catch (e) {
    notify(errorText(e), 'error')
  } finally {
    checking.value = false
  }
}

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
onMounted(() => {
  load()
  refreshUpdate().catch(() => {})
})

function pendingChanges() {
  const changes = {}
  for (const p of fields.value) {
    const cur = valueAt(cfg.value, p.key)
    let v = form.value[p.key]
    if (p.kind === 'int') v = Number(v)
    if (p.kind === 'string' && typeof v === 'string') v = v.trim()
    if (p.secret && v === '***') continue
    if (v !== cur) changes[p.key] = v
  }
  return changes
}

// save 把改动 PATCH 到服务端；返回是否成功（没有改动也算成功）。
async function save() {
  const changes = pendingChanges()
  if (!Object.keys(changes).length) {
    notify('没有改动')
    return true
  }
  saving.value = true
  try {
    const r = await patch('/api/v1/config', changes)
    cfg.value = r.config
    notify(`已保存 ${Object.keys(changes).length} 项并写回 ${r.path || '内存'}`)
    await refreshMode()
    await load()
    return true
  } catch (e) {
    notify(errorText(e), 'error')
    return false
  } finally {
    saving.value = false
  }
}

// testNotify 先保存未提交的改动（服务端用运行配置发送），再向指定渠道发测试消息。
async function testNotify(channel) {
  notifyResult.value = null
  if (Object.keys(pendingChanges()).length && !(await save())) return
  notifyTesting.value = channel
  try {
    notifyResult.value = await post('/api/v1/notifications/test', { channel })
  } catch (e) {
    notify(errorText(e), 'error')
  } finally {
    notifyTesting.value = ''
  }
}

function fieldLabel(p) {
  return p.label || p.key
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
        <label v-for="p in generalFields" :key="p.key">
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

    <div class="card" style="grid-column: 1 / -1" v-if="notifyFields.length">
      <h2>通知</h2>
      <p class="muted">
        相机插入、开始备份、备份完成、备份失败时推送到手机。MeoW 设备可直连；Telegram 在设备上直连不到 api.telegram.org，需填代理。
        改动保存后立即生效；「发送测试」会先保存再发。
      </p>
      <form class="notify" @submit.prevent="save">
        <fieldset>
          <legend>开关与事件</legend>
          <div class="inline">
            <label v-for="p in [...notifySection('notifications.enabled'), ...notifySection('notifications.on.')]" :key="p.key" class="check">
              <input type="checkbox" :name="p.key" v-model="form[p.key]" />
              <span>{{ fieldLabel(p) }}</span>
              <span class="muted" style="font-size:11px">{{ p.doc }}</span>
            </label>
            <label v-for="p in notifySection('notifications.site_url')" :key="p.key">
              <span>{{ fieldLabel(p) }}</span>
              <input type="text" :name="p.key" v-model="form[p.key]" placeholder="http://172.20.31.6:8080" />
              <span class="muted" style="font-size:11px">{{ p.doc }}</span>
            </label>
          </div>
        </fieldset>
        <fieldset>
          <legend>MeoW</legend>
          <div class="inline">
            <template v-for="p in notifySection('notifications.meow.')" :key="p.key">
              <label v-if="p.kind === 'bool'" class="check">
                <input type="checkbox" :name="p.key" v-model="form[p.key]" />
                <span>{{ fieldLabel(p) }}</span>
                <span class="muted" style="font-size:11px">{{ p.doc }}</span>
              </label>
              <label v-else>
                <span>{{ fieldLabel(p) }}</span>
                <input type="text" :name="p.key" v-model="form[p.key]" autocomplete="off" />
                <span class="muted" style="font-size:11px">{{ p.doc }}</span>
              </label>
            </template>
            <div><button type="button" @click="testNotify('meow')" :disabled="!!notifyTesting || saving">{{ notifyTesting === 'meow' ? '发送中…' : '发送测试到 MeoW' }}</button></div>
          </div>
        </fieldset>
        <fieldset>
          <legend>Telegram</legend>
          <div class="inline">
            <template v-for="p in notifySection('notifications.telegram.')" :key="p.key">
              <label v-if="p.kind === 'bool'" class="check">
                <input type="checkbox" :name="p.key" v-model="form[p.key]" />
                <span>{{ fieldLabel(p) }}</span>
                <span class="muted" style="font-size:11px">{{ p.doc }}</span>
              </label>
              <label v-else>
                <span>{{ fieldLabel(p) }}</span>
                <input :type="p.secret ? 'password' : 'text'" :name="p.key" v-model="form[p.key]" autocomplete="off" />
                <span class="muted" style="font-size:11px">{{ p.doc }}</span>
              </label>
            </template>
            <div><button type="button" @click="testNotify('telegram')" :disabled="!!notifyTesting || saving">{{ notifyTesting === 'telegram' ? '发送中…' : '发送测试到 Telegram' }}</button></div>
          </div>
        </fieldset>
        <div class="row">
          <button class="primary" type="submit" :disabled="saving">{{ saving ? '保存中…' : '保存通知设置' }}</button>
          <span v-if="notifyResult" :class="notifyResult.ok ? 'ok' : 'err'">
            {{ notifyResult.channel }}：{{ notifyResult.ok ? `已发送（${notifyResult.latency_ms} ms）` : notifyResult.error }}
          </span>
        </div>
      </form>
    </div>

    <div class="card" style="grid-column: 1 / -1" v-if="upd">
      <h2>软件更新</h2>
      <p class="update-headline" :class="upd.update_available ? 'warn' : ''">
        <template v-if="upd.update_available && upd.latest">有新版本 v{{ upd.latest.version }}<span v-if="upd.latest.prerelease">（预发布）</span>，当前 v{{ upd.current_version }}</template>
        <template v-else-if="upd.dev_build">当前为开发构建 {{ upd.current_version }}，不做定期检查，也不判定是否有新版本</template>
        <template v-else-if="upd.latest">已是最新版本 v{{ upd.current_version }}</template>
        <template v-else>当前 v{{ upd.current_version }}，尚未检查过新版本</template>
      </p>
      <dl class="kv">
        <dt>当前版本</dt><dd>v{{ upd.current_version }} <span class="muted">{{ upd.platform }}</span></dd>
        <template v-if="upd.latest">
          <dt>最新版本</dt>
          <dd>
            v{{ upd.latest.version }}
            <span class="badge warn" v-if="upd.latest.prerelease">预发布</span>
            <span class="muted" v-if="upd.latest.published_at">发布于 {{ time(upd.latest.published_at) }}</span>
          </dd>
          <dt>安装包</dt>
          <dd v-if="upd.latest.asset">
            <a :href="upd.latest.asset.url" target="_blank" rel="noopener noreferrer" class="mono">{{ upd.latest.asset.name }}</a>
            <span class="muted">（{{ bytes(upd.latest.asset.size) }}）</span>
          </dd>
          <dd v-else class="muted">该版本没有发布 {{ upd.platform }} 的安装包</dd>
        </template>
        <dt>上次检查</dt>
        <dd>
          {{ upd.checked_at ? `${time(upd.checked_at)}（${ago(upd.checked_at)}）` : '从未成功检查' }}
          <span class="muted" v-if="upd.next_check_at">，下次 {{ time(upd.next_check_at) }}</span>
        </dd>
        <template v-if="upd.last_error">
          <dt>检查失败</dt><dd class="err">{{ upd.last_error }}<span class="muted" v-if="upd.last_error_at">（{{ ago(upd.last_error_at) }}）</span></dd>
        </template>
      </dl>
      <details v-if="upd.latest && upd.latest.notes" :open="upd.update_available">
        <summary>v{{ upd.latest.version }} 发布说明</summary>
        <pre class="notes">{{ upd.latest.notes }}<template v-if="upd.latest.notes_truncated">
…（已截断，完整内容见发布页）</template></pre>
      </details>
      <template v-if="installBusy">
        <p>
          <strong>{{ STAGE_TEXT[inst.stage] }}</strong>
          <span class="muted" v-if="inst.version">（v{{ inst.version }}）</span>
        </p>
        <div class="row" v-if="inst.stage === 'downloading'">
          <div class="bar" style="flex:1"><i :style="{ width: downloadPercent + '%' }"></i></div>
          <span class="mono">{{ bytes(inst.downloaded_bytes || 0) }} / {{ bytes(inst.total_bytes) }}</span>
        </div>
      </template>
      <p class="err" v-else-if="inst.stage === 'failed'">更新失败：{{ inst.error }}</p>
      <p v-if="inst.last_result && !installBusy" :class="inst.last_result.ok ? 'muted' : 'err'">
        上次安装（{{ time(inst.last_result.finished_at) }}）：
        <template v-if="inst.last_result.ok">v{{ inst.last_result.from }} → v{{ inst.last_result.to }} 成功</template>
        <template v-else>
          {{ inst.last_result.to ? `v${inst.last_result.to} ` : '' }}失败于 {{ inst.last_result.step }}：{{ inst.last_result.error }}
          <template v-if="inst.last_result.rolled_back">{{ inst.last_result.rollback_error ? `；回滚未完成：${inst.last_result.rollback_error}` : '；已回滚到原版本' }}</template>
        </template>
      </p>
      <template v-if="upd.update_available && upd.latest">
        <p class="muted" v-if="!upd.install_supported">
          本机不支持网页一键安装（需要 Linux 正式构建，且已用带更新定时任务的安装包装过一次）。请下载安装包后在设备上执行 <code>install.sh</code>，或在开发机上用 <code>make deploy RESTART=1</code> 部署。
        </p>
        <p class="muted" v-else-if="!upd.latest.signed || !upd.latest.asset">
          v{{ upd.latest.version }} {{ upd.latest.asset ? '没有签名文件（SHA256SUMS.sig）' : `没有 ${upd.platform} 的安装包` }}，不能自动安装，请手动安装。
        </p>
      </template>
      <form class="inline" @submit.prevent="save">
        <label v-for="p in updateFields" :key="p.key">
          <span>{{ fieldLabel(p) }}</span>
          <select v-if="p.kind === 'bool'" :name="p.key" v-model="form[p.key]"><option :value="true">开启</option><option :value="false">关闭</option></select>
          <select v-else-if="p.choices" :name="p.key" v-model="form[p.key]"><option v-for="c in p.choices" :key="c" :value="c">{{ c }}</option></select>
          <input v-else-if="p.kind === 'int'" :name="p.key" type="number" min="1" max="720" v-model.number="form[p.key]" />
          <input v-else :name="p.key" type="text" v-model="form[p.key]" />
          <span class="muted" style="font-size:11px">{{ p.doc }}</span>
        </label>
        <div class="row">
          <button type="button" @click="doCheckUpdate" :disabled="checking || saving || upd.checking">{{ checking || upd.checking ? '检查中…' : '立即检查' }}</button>
          <button
            v-if="upd.update_available && upd.install_supported && upd.latest && upd.latest.signed && upd.latest.asset"
            class="primary" type="button" @click="doApplyUpdate" :disabled="applying || installBusy"
          >{{ installBusy ? '更新中…' : inst.stage === 'failed' ? `重试更新到 v${upd.latest.version}` : `立即更新到 v${upd.latest.version}` }}</button>
          <button type="submit" :disabled="saving">{{ saving ? '保存中…' : '保存' }}</button>
          <a v-if="upd.latest" :href="upd.latest.url" target="_blank" rel="noopener noreferrer">打开发布页</a>
        </div>
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
