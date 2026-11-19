// 事件的中文显示。后端事件库里存的是 type + 英文 message + 结构化 data，
// 这里按 type 与 data 现场生成中文，所以历史事件同样生效；
// 没有登记的类型、或 data 不够拼出中文时回退到后端原文，不丢信息。
import { ref, watch } from 'vue'
import { bytes, duration, linkSpeed } from './format.js'

// 显示语言：zh 中文（默认）| raw 后端原文。事件页切换，概览页的最近事件跟随；记在浏览器本地。
const LANG_KEY = 'action-backup.events.lang'
function loadLang() {
  try {
    return localStorage.getItem(LANG_KEY) === 'raw' ? 'raw' : 'zh'
  } catch {
    return 'zh'
  }
}
export const eventLang = ref(loadLang())
watch(eventLang, (v) => {
  try {
    localStorage.setItem(LANG_KEY, v)
  } catch {
    /* 存不了（隐私模式等）就只在本次会话内生效 */
  }
})

export const LEVEL_LABELS = { debug: '调试', info: '信息', warn: '警告', error: '错误' }

// 类型 → 中文名。顺序即「类型」下拉里的顺序。
export const TYPE_LABELS = {
  'device.attached': '相机插入',
  'device.detached': '相机拔出',
  'plan.generated': '生成备份计划',
  'backup.started': '开始备份',
  'backup.finished': '备份完成',
  'backup.completed': '文件备份完成',
  'task.started': '开始传输',
  'task.paused': '任务暂停',
  'task.cancelled': '任务取消',
  'task.interrupted': '任务中断',
  'task.failed': '任务失败',
  'task.retry': '任务重试',
  'storage.mounted': '存储已挂载',
  'storage.unmounted': '存储已卸载',
  'storage.mount_failed': '存储挂载失败',
  'recovery.interrupted': '启动恢复',
  'config.changed': '配置修改',
  'notify.failed': '通知发送失败',
  'update.available': '有新版本',
  'update.check_failed': '检查更新失败',
  'update.requested': '更新已就绪',
  'update.installed': '更新完成',
  'update.failed': '更新失败',
  'auth.login': '登录',
  'auth.logout': '退出登录',
  'auth.login_failed': '登录失败',
  'auth.login_rate_limited': '登录限速',
  'system.started': '服务启动',
  'system.stopping': '服务停止',
  'fault.hit': '故障注入命中',
}

const MODE_LABELS = { production: '正式', 'dry-run': '演练', simulation: '模拟' }

const n = (v) => (typeof v === 'number' ? v : 0)
const has = (d, ...keys) => keys.every((k) => d[k] !== undefined && d[k] !== null && d[k] !== '')

// 类型 → (data, event) => 中文消息；返回空值表示拼不出来，回退原文。
const MESSAGES = {
  'device.attached': (d) => {
    if (!has(d, 'name')) return null
    let s = `${d.name} 已插入`
    if (d.serial) s += `（SN ${d.serial}）`
    if (n(d.volumes)) s += `，${d.volumes} 个卷`
    if (d.link_speed_mbps) s += `，${d.usb_version ? `USB ${d.usb_version} · ` : ''}${linkSpeed(d.link_speed_mbps)}`
    if (d.weak_identity) s += '；序列号无法识别'
    if (d.usb_link_limited) s += '；当前为 USB 2.0 连接，备份速度受限，请检查线材或接口'
    return s
  },
  'device.detached': (d) => `相机已拔出${d.device_key ? `（${d.device_key}）` : ''}`,
  'plan.generated': (d) =>
    has(d, 'upload_files')
      ? `计划：待上传 ${d.upload_files} 个文件（${bytes(n(d.upload_bytes))}），跳过 ${n(d.skip_files)} 个，冲突 ${n(d.conflict_files)} 个${n(d.warnings) ? `，${d.warnings} 条警告` : ''}`
      : null,
  'backup.started': (d) => {
    if (!has(d, 'created')) return null
    const parts = [`新建 ${d.created} 个任务`]
    if (n(d.requeued)) parts.push(`重新入队 ${d.requeued} 个`)
    if (n(d.upload_bytes)) parts.push(`待上传 ${bytes(d.upload_bytes)}`)
    if (n(d.conflicts)) parts.push(`冲突 ${d.conflicts} 个${n(d.overwritten) ? `（覆盖 ${d.overwritten} 个）` : ''}`)
    if (n(d.exhausted)) parts.push(`${d.exhausted} 个已用尽重试`)
    return (n(d.created) + n(d.requeued) === 0 ? '无需备份：' : '开始备份：') + parts.join('，')
  },
  'backup.finished': (d) => {
    if (!has(d, 'completed')) return null
    const parts = [`成功 ${d.completed} 个文件 / ${bytes(n(d.bytes))}`]
    if (n(d.failed) || n(d.interrupted)) parts.push(`失败 ${n(d.failed)} 个，中断 ${n(d.interrupted)} 个`)
    if (n(d.cancelled)) parts.push(`取消 ${d.cancelled} 个`)
    if (n(d.duration_sec)) parts.push(`用时 ${duration(d.duration_sec)}`)
    return '本批备份结束：' + parts.join('，')
  },
  'backup.completed': (d) =>
    has(d, 'path') ? `已完成 ${d.path}${n(d.bytes) ? `（${bytes(d.bytes)}）` : ''}${d.resumed ? '，断点续传' : ''}` : null,
  'task.started': (d) => (has(d, 'path') ? `开始传输 ${d.path}${n(d.size) ? `（${bytes(d.size)}）` : ''}` : null),
  'task.paused': (d) => (has(d, 'path') ? `已暂停 ${d.path}，已传 ${bytes(n(d.bytes))}` : null),
  'task.cancelled': (d, e) => (has(d, 'path') ? `已取消 ${d.path}` : e.task_id ? `任务 #${e.task_id} 已取消（尚未开始传输）` : null),
  'task.interrupted': (d) => (has(d, 'path') ? `传输中断 ${d.path}${d.error ? `：${d.error}` : ''}` : null),
  'task.failed': (d) => (has(d, 'path') ? `传输失败 ${d.path}${d.error ? `：${d.error}` : ''}` : null),
  'task.retry': (d, e) => (e.task_id ? `任务 #${e.task_id} 已重新入队${d.path ? `：${d.path}` : ''}` : null),
  'storage.mounted': (d) => `备份目标已挂载${d.root ? `：${d.root}` : ''}${d.fs ? `（${d.fs}）` : ''}`,
  'storage.unmounted': (d) => `备份目标已卸载${d.mount_point ? `：${d.mount_point}` : ''}`,
  'storage.mount_failed': (d) => `备份目标挂载失败${d.error ? `：${d.error}` : ''}`,
  'recovery.interrupted': (d) => (has(d, 'count') ? `上次停止时有 ${d.count} 个任务正在进行，已标记为中断` : null),
  'config.changed': (d) => (d.changes ? `配置已修改：${Object.keys(d.changes).join('、')}` : null),
  'notify.failed': (d) => (has(d, 'channel') ? `通知经 ${d.channel} 发送失败${d.error ? `：${d.error}` : ''}` : null),
  'update.available': (d) =>
    has(d, 'version') ? `发现新版本 ${d.version}${d.prerelease ? '（预发布）' : ''}${d.current ? `，当前 ${d.current}` : ''}` : null,
  'update.check_failed': (d) => `检查更新失败${d.error ? `：${d.error}` : ''}`,
  'update.requested': (d) => (has(d, 'to') ? `v${d.to} 已下载并通过签名校验，等待安装任务接手（服务将重启）` : null),
  'update.installed': (d) => (has(d, 'to') ? `已从 v${d.from || '?'} 更新到 v${d.to}` : null),
  'update.failed': (d) => {
    if (!has(d, 'step')) return null
    const steps = { prepare: '准备', download: '下载', verify: '校验', wait_idle: '等待空闲', request: '提交请求', stage: '取包', extract: '解包', inspect: '检查安装包', stop: '停止服务', backup: '备份', install: '执行 install.sh', restart: '重启服务', health: '健康检查', recover: '恢复被打断的安装' }
    let s = `更新${d.to ? `到 v${d.to} ` : ''}失败（${steps[d.step] || d.step}）${d.error ? `：${d.error}` : ''}`
    if (d.rolled_back) s += d.rollback_error ? `；回滚未完成：${d.rollback_error}` : '；已回滚到原版本'
    return s
  },
  'auth.login': (d) => `${d.username || '用户'} 登录成功${d.ip ? `（${d.ip}）` : ''}`,
  'auth.logout': (d) => `${d.username || '用户'} 已退出登录`,
  'auth.login_failed': (d) => `登录失败${d.username ? `：用户名 ${d.username}` : ''}${d.ip ? `（${d.ip}）` : ''}`,
  'auth.login_rate_limited': (d) => `登录过于频繁，已限速${d.ip ? `（${d.ip}）` : ''}`,
  'system.started': (d) =>
    `服务已启动${d.version ? `，版本 ${d.version}` : ''}${d.mode ? `，${MODE_LABELS[d.mode] || d.mode}模式` : ''}`,
  'system.stopping': () => '服务正在停止',
  'fault.hit': (d) => (has(d, 'rule', 'point') ? `故障注入规则 ${d.rule} 在 ${d.point} 命中（${d.action || '-'}）` : null),
}

export function typeLabel(type) {
  return TYPE_LABELS[type] || type
}

export function levelLabel(level) {
  return LEVEL_LABELS[level] || level
}

export function eventMessage(e) {
  const f = MESSAGES[e.type]
  if (!f) return e.message
  try {
    return f(e.data || {}, e) || e.message
  } catch {
    return e.message
  }
}
