# DJI Osmo Action 5 Pro 自动备份系统

**软件架构设计方案 V1.6**

| 项目 | 内容 |
| --- | --- |
| 目标硬件 | 网心云 OEC-Turbo |
| 操作系统 | 已选定的 ARM64 Linux 固件 |
| 开发机 | macOS（Apple Silicon 或 Intel），交叉编译 ARM64 |
| 开发语言 | Go 1.25，遵循 Go 标准项目布局 |
| 运行模式 | `production`、`dry-run`、`simulation` |
| SMB 传输 | Linux CIFS 挂载，自动协商 SMB 2.1 到 3.1.1，禁止 SMB1 |
| 数据库 | SQLite（`modernc.org/sqlite` v1.59，纯 Go，写事务 `BEGIN IMMEDIATE`） |
| 校验策略 | 默认基础校验，可选 SHA-256 |
| 部署方式 | systemd 原生部署：主服务 `action-backup.service`（无特权）+ root 挂载守护 `action-backup-mounter.service`；`make deploy` 经 SSH 打包、上传、安装、重启 |
| 实现进度 | V0.1 至 V0.8 已完成（V0.6 / V0.7 实机验证于 2026-10-27）；V0.9 软件更新第一阶段（检查 + 页面提示）与第二阶段（下载、验签、安装、重启、回滚）已于 2026-11-19 实现并在 mac 上验证；第二阶段的 root 侧安装尚未实机验证（§19.6.9），第三阶段未实现 |
| 软件更新 | 自己实现，不依赖第三方自更新库；更新源为 GitHub Releases；分三阶段：检查提示 → 验签安装重启回滚 → 空闲自动安装（§19） |
| 管理方式 | Web UI + REST API + SSE，用户名密码登录 |
| 备份来源 | DJI Osmo Action 5 Pro 内部存储及 microSD |
| 备份目标 | SMB NAS `//172.20.100.254/home`（实测 SMB 3.1.1），目录布局可配置模板 |
| 性能目标 | 81.1 GB 全量备份在 30 分钟内（≥ 45 MB/s）。实机 2026-10-27：串行读写 + 64 MB 检查点 24.1 MB/s → 检查点 512 MB + 读写流水线 66–70 MB/s（裸 CIFS 写 72），折算约 20 分钟，见 §17 |

### V1.6 相对 V1.5 的变更（软件更新）

1. **新增第 19 章「软件更新」**：记录调研结论（为什么不用 `creativeprojects/go-selfupdate`）、整体结构（无特权主服务检查与下载 + root 定时任务验签安装）、信任链（ed25519 签名）、三阶段划分与各阶段设计。用户 2026-11-19 确认方案并决定先做第一阶段。
2. **第一阶段已实现（V0.9）**：新包 `internal/update`，定期（默认 12 小时）与手动查询 GitHub Releases，按语义化版本比较，结果经 `GET /system/update` 输出、发 `update.available` / `update.check_failed` 事件、可推送通知；只发 HTTPS GET，不下载、不安装、不重启（§19.5）。
3. **API**：新增 `GET /system/update`、`POST /system/update/check`，均需登录（§9）。
4. **配置**：新增配置段 `update`（`check_enabled`、`check_interval_hours`、`channel`、`api_base_url`），前三项进 `PATCH /config` 白名单；`notifications.on` 增加 `update_available`（§10.4、§10.5）。发布仓库地址写死在代码里，不可配置。
5. **Web UI**：顶栏版本号旁显示「有新版本 vX.Y.Z」徽标，「设置」页新增「软件更新」卡片（当前 / 最新版本、发布说明、安装包链接、立即检查、检查开关 / 间隔 / 通道）；事件中文文案登记两个新类型（§19.5）。
6. **依赖**：新增直接依赖 `golang.org/x/mod`（只用 `semver`），Go 版本要求不变（1.25.0）。
7. **本版未回写的旧内容**：V0.8 的 USB 协议版本显示与事件中文显示没有单独出架构版本，结论在 `docs/03-decisions.md`（2026-11-19 各条）。
8. **第二阶段已实现、未实机验证（V0.9）**：主服务下载并验签后写请求标记，`action-backup-update.timer` 每分钟以 root 拉起 `action-backup self-update --apply`，验签、解包、执行包内 `install.sh`、重启两个服务、健康检查，失败回滚（含数据库）。发布流水线对 `SHA256SUMS` 做 ed25519 签名。新增路由 `POST /system/update/apply`、事件 `update.progress / requested / installed / failed`、两个 systemd 单元（§19.6）。

### V1.5 相对 V1.4 的变更（按 V0.6 实现回写）

1. **Linux 设备检测**：`internal/platform/linux` 轮询 sysfs（`/sys/bus/usb/devices` 的 `idVendor / idProduct / product / serial`，接口 → `host*` → `target*` → `h:c:t:l` 的 `block/`）与 udev 数据库 `/run/udev/data/b<maj>:<min>`（`ID_FS_UUID / LABEL / TYPE`），不用 netlink、不读 `/dev/sd*`，全程无特权（§11.3）。
2. **挂载守护**：`action-backup mount-helper` 子命令以 root 运行为 `action-backup-mounter.service`，unix socket `/run/action-backup-mounter/helper.sock`（root:actionbackup 0660），请求 `mount-source / umount-source / mount-target / umount-target / ping`，路径全部从配置推导，客户端只能给 `/dev/sd[a-z]+`。源卷 `ro,nosuid,nodev,noexec,uid=actionbackup,umask=0077`；CIFS 经 `mount.cifs`，SMB1 拒绝。不用 sudoers（`NoNewPrivileges=true` 下 setuid 无效）（§5、§13）。
3. **systemd**：主服务 `Requires=action-backup-mounter.service`、`ReadWritePaths=/etc/action-backup`（`PATCH /config` 写回）与 `/run/action-backup-mounter`；守护单元**不能**带 `PrivateTmp / ProtectHome / ProtectSystem`（私有挂载命名空间）也**不能**用 `RuntimeDirectory=`（目录下有只读挂载时启动前递归 chown 报 EROFS），两条都是实机踩坑结论（§13）。
4. **平台接口**：新增 `StorageMounter`（`MountTarget / UnmountTarget`），`Backend.Storage` 仅 Linux cifs 非空；app 启动与每个 reconcile 周期检查目标不可用时重挂。`device.Manager` 新增 `OnDetached`，拔出后卸载源卷。`domain.Volume` 增加 `DevicePath`（不入库）（§5）。
5. **配置**：`device.mount_root` 默认 `/run/action-backup-mounter/source`；新增 `storage.allow_local_destination`（dry-run / production 显式允许 `type: local`，NAS 不可达时写本地目录）；`checkpoint_interval_sec` 默认 5 → 30，`checkpoint_bytes_mb` 默认 64 → 512（§10）。
6. **传输**：Worker 改为读写流水线（预读 goroutine + 两块缓冲），归档后 `Target.SetModTime` 保留源 mtime；检查点 fsync 频率是 CIFS 吞吐的决定因素（§7.1、§17、§18 问题 9）。
7. **部署**：`make deploy` = `scripts/deploy.sh`（打包 → scp → `install.sh` → 可选重启两个单元）；`install.sh` 把 `/etc/action-backup` 设为 `root:actionbackup 0770`；`scripts/test-performance.sh` 实现（§13.1）。
8. **实机结论**：相机序列号 `0A1B2C3D`、LUN 0 内置 48 GB `OsmoAction`、LUN 1 microSD 116 GB，与 mac 一致；exFAT 时间戳 `+0800` 与 mac 一致；SMB 3.1.1；首次吞吐 24.1 MB/s，瓶颈为每个检查点的 fsync（§17、§18）。
9. **推送通知（V0.7）**：新包 `internal/notify`，订阅事件总线，把 `device.attached`、`backup.started`、`backup.finished`（新增的批次汇总事件，调度器在队列清空时发）、`task.failed / task.interrupted`（30 秒窗口合并）翻译成中文消息，经 MeoW（`POST https://api.chuckfang.com/{昵称}` JSON）与 Telegram Bot（`sendMessage`，可配 http / socks5 代理）推送；独立 goroutine 串行发送，10 秒超时、3 次指数退避重试、同渠道最小间隔 1 秒，最终失败记 `notify.failed` 事件（不再触发通知）。配置段 `notifications`（§10.4）全部进 `PATCH /config` 白名单，`bot_token` 标记 `secret`：API 输出 `***`、回传 `***` 视为未改、`config.changed` 事件脱敏；Telegram 请求 URL 含 token，错误信息一律替换为 `***`。新路由 `POST /notifications/test`（§9）。设备实测 `api.chuckfang.com` 可直连，`api.telegram.org` DNS 被污染不可达，Telegram 必须填 `proxy_url`（`docs/evidence/20261027T0720Z-v07-notifications/`）。

### V1.4 相对 V1.3 的变更（按 V0.5 实现回写）

1. **认证**：`internal/api/auth` 实现会话、CSRF、登录限速；门禁中间件在最外层，`/api/` 下除 `/auth/login`、`/system/health` 外全部 401；Cookie 不设 `Secure`（局域网 HTTP）；未知用户名仍做 bcrypt 比较；`auth.*` 事件入库（§9）。
2. **SSE**：`/events/stream` 绕过 30 秒 `TimeoutHandler`，转发事件总线全部事件并合并同任务进度，15 秒心跳（§9）。
3. **配置接口**：`PATCH /config` 白名单固定为 6 项，用 yaml.v3 Node 原地改写文件保留注释，运行时即时生效并发 `config.changed`；`POST /storage/test` 只读（§9、§10）。
4. **passwd**：非终端输入必须 `--stdin`，口令至少 8 字符，不改 `auth.enabled`（§9）。
5. **Web UI**：Vue 3 + Vite，无路由库，单页 tab；`web/dist` 入库并固定文件名，纯 Go 构建不依赖 Node（§4、§11.2）。
6. **运行时可调**：`backup.Service` 校验方式 / 冲突策略与 `scheduler.Options` 加锁，重连周期每轮重新读取。
7. **阶段与验收**：V0.5 标记完成并链接证据；§16 改为 V0.6 任务（§14、§16）。

### V1.3 相对 V1.2 的变更（按 V0.1 至 V0.4 实现回写）

1. **状态机**：新增 `TRANSFERRING → QUEUED`，仅用于用户暂停，保留断点、不计重试（§7）。
2. **重试与失败分类**：源 / 设备 / 目标不可用 → `INTERRUPTED`，由调度器周期性重连自动重试；目标在线时的写、刷盘、重命名、落库失败与校验失败 → `FAILED`，只人工重试；自动重试受 `backup.retry_count` 限制，人工重试不限（§7.3）。
3. **校验失败丢弃断点与 `.part`**，下次从头重传；断点的 `prefix_sha256` 列保存 SHA-256 内部状态（§8）。
4. **Schema**：`devices.weak_identity`、`volumes.label / lun / size_bytes`、`events.data`、`schema_migrations.name`；DSN 增加 `_txlock=immediate`（§6）。
5. **dry-run 门禁**违规返回 `ErrModeReadOnly` 错误而不是 panic（§12.1）。
6. **计划与冲突**：`Plan` 增加 `target_checked`、`warnings`；目标不可用时已完成文件按 `upload / target_unverified` 处理；目标已有文件而数据库无记录 → `conflict`，由 `backup.on_conflict`（manual / overwrite / skip）决定（§7.3）。
7. **故障注入**：触发条件增加 `after: <rule-id>`；注入点、动作、API 按实现更新；崩溃动作退出码 137（§12.4）。
8. **darwin 后端**：`ioreg -a` + 自带 plist 解析器，不用 `system_profiler`；序列号来自产品名 `SN:` 字段；卷按 LUN 分类（§11.3）。
9. **目录与接口**：工程目录、核心接口按实际代码更新（§4、§5）。
10. **配置**：`backup.include_internal / include_sdcard` 省略即 true，新增 `on_conflict`、`reconcile_interval_sec`；新增 `configs/config.mac-dryrun.yaml`（§10）。
11. **工具链**：Go 1.25.5、依赖锁定在 Go 1.25 兼容版本、`GOPROXY=https://proxy.golang.org,direct`（§11.1）。
12. **阶段与验收**：V0.1 至 V0.4 标记完成并链接证据；真 NAS 上传因 NAS 不可达待补（§14）。

### V1.2 相对 V1.1 的变更

1. 落实 `docs/03-decisions.md` 第 3 至 7 条决策。
2. 工具链固定为 Go 1.25、Node 25、pnpm 10、golangci-lint；工程结构遵循 Go 标准项目布局。
3. Web 认证改为**用户名密码**：登录接口、HttpOnly 会话 Cookie、CSRF Token、口令 bcrypt 存储、`action-backup passwd` 子命令。
4. 目标目录布局改为可配置模板 `storage.destination_layout`，默认 `{volume_type}/{relative_path}`。
5. 取消 mac 上 `diskutil` 只读重挂载方案，只读保护仅依赖程序自身从不写源卷；删除 `scripts/mac-mount-ro.sh` 和 `simulation.volumes.remount_readonly`。
6. `storage.smb_version` 默认 `auto`，由 CIFS 协商 2.1 到 3.1.1，实机确认后可固定。
7. 第 18 章问题 1、2 改为"待探测脚本输出"。
8. 2026-10-23 补充 mac 实测结论（§1.4）：USB 序列号为占位值、LUN 与卷的对应、真实 DCIM 结构、单文件最大 15.8 GB、mtime 秒级精度；据此修订设备身份、卷分类、fingerprint 归一化和 fixtures 命名。

### V1.1 相对 V1.0 的变更

1. 新增第 11 章 **macOS 开发环境**：工具链、交叉编译、平台抽象层、mac 上接真相机和真 NAS 的方式。
2. 新增第 12 章 **测试模式**：`dry-run`（实机只扫描和规划）、`simulation`（本地目录模拟相机和 NAS）、故障注入、测试分层。
3. 工程目录增加 `internal/platform`（darwin / linux / sim 三种后端）、`internal/runmode`、`internal/faultinject`、`tests/fixtures` 生成脚本。
4. 配置文件增加 `mode`、`storage.type` 三种取值、`simulation`、`faults`；新增 `configs/config.dev.yaml`。
5. REST API 增加运行模式查询、备份计划预览、测试专用接口。
6. SQLite 驱动由"评估"改为**选定** `modernc.org/sqlite`，保证 `CGO_ENABLED=0` 交叉编译。
7. 扫描器明确忽略 macOS 产生的 `._*`、`.DS_Store` 等元数据文件；`fingerprint` 中的修改时间归一化到 exFAT 的 2 秒精度。
8. OEC-Turbo 暂不能 SSH，部署章节改为先产出安装包，`make deploy` 预留。
9. 开发阶段重新排序：先在 mac 上用 simulation 跑通全流程，Linux 平台适配放到设备可用后集成。
10. 新增第 18 章 **待确认问题**。

## 1. 项目目标与约束

相机拍摄完成后通过 USB 接入 OEC-Turbo，系统自动识别两个 exFAT 存储卷，递归扫描 `DCIM`，查询历史记录并将未备份或未完成的文件流式传输到 NAS。完成校验后正式归档，并通过 Web 页面展示进度、速率、历史和错误信息。

### 1.1 功能目标

1. 支持 USB 自动识别、热插拔和服务启动时重新发现设备。
2. 支持内部存储及 microSD 的独立识别与只读挂载。
3. 默认备份 MP4，可配置启用 LRF、JPG、DNG 等格式。
4. 保留原始目录结构和文件名称。
5. 根据设备、卷、相对路径、大小和修改时间生成增量备份计划。
6. 支持 SMB 大文件上传、临时文件、断点续传和重试。
7. 默认基础校验，可选端到端 SHA-256 校验。
8. 支持 Web 配置、任务控制、状态推送和日志查询。
9. 禁止自动删除或修改相机原始文件。
10. 同一套代码可在 macOS 开发机上以 simulation 模式完整运行，在实机上以 dry-run 模式安全预演。

### 1.2 已有验证数据

| 指标 | 结果 |
| --- | --- |
| USB Vendor ID | `0x0506` |
| USB Product ID | `0x8002` |
| USB 协商速率 | 5 Gbps |
| 文件系统 | 两个存储卷均为 exFAT |
| 内置存储容量 | 约 51.5 GB |
| microSD 容量 | 约 124.7 GB |
| microSD 已用数据 | 约 81.1 GB |
| 单个约 5 GB 视频读取速度 | 约 70.93 MB/s，在 macOS 上实测 |
| 最低端到端平均吞吐 | 约 45.1 MB/s |
| 建议持续吞吐 | 60 MB/s 以上 |

以上相机读取测试不能直接代表 OEC-Turbo 到 NAS 的实际吞吐。30 分钟目标应统计扫描、上传及选定校验流程的总耗时。mac 上经 Finder smbfs 挂载测得的写入速度同样不能代表 Linux CIFS 的表现，只能用于功能验证。

### 1.3 运行模式总览

| 模式 | 运行位置 | 设备来源 | 目标写入 | 数据库 | 用途 |
| --- | --- | --- | --- | --- | --- |
| `production` | OEC-Turbo | udev + 只读挂载 | 真实写入 CIFS | `/var/lib/action-backup/backup.db` | 正式运行 |
| `dry-run` | OEC-Turbo 或 mac | 真实设备 | **零写入**，只输出计划 | 只读查询，不写任务 | 实机首轮验证、备份前预览 |
| `simulation` | mac 为主 | 本地目录 fixtures，或 `/Volumes` 下真相机 | 本地目录，或 Finder 挂载的真 NAS | 独立开发库 | 日常开发、集成测试、故障注入 |

三种模式共用同一套 Domain、Sync Engine、Scheduler、Backup Worker 和 Verify 代码，差异只存在于 `internal/platform` 后端和 `internal/runmode` 的写入门禁。

### 1.4 mac 实测结论（2026-10-23）

原始输出：`docs/evidence/20261023T020156Z-mac-probe/`。

| 项目 | 结果 |
| --- | --- |
| USB 身份 | `idVendor` 0x0506（DJI）、`idProduct` 0x8002、`bcdDevice` 0x0515、SuperSpeed 5 Gbps |
| USB 序列号 | `iSerialNumber` 固定为 `123456789ABCDEF`，**不是唯一值**；真实序列号只出现在产品名字符串 `OsmoAction5pro_SN:0A1B2C3D` 中 |
| 大容量存储 | Linux USB gadget，SCSI 厂商 `Linux`、产品 `File-Stor Gadget`，两个 LUN |
| LUN 0 | `disk4`，卷名 `OsmoAction`，51.5 GB，内置存储，当前 DCIM 为空 |
| LUN 1 | `disk5`，卷名 `Untitled`，124.7 GB，microSD，已用 42.5 GB |
| 两卷 UUID | exFAT 卷有各自 Volume UUID，由 exFAT 卷序列号派生 |
| DCIM 结构 | `DCIM/DJI_002_XZH/`、`DCIM/DJI_003/`，目录名随相机设置变化，不是 `100MEDIA` |
| 文件命名 | `DJI_YYYYMMDDhhmmss_NNNN_D.MP4`，同名 `.LRF` 为低码率代理文件 |
| 单文件大小 | 5.03 GB、13.13 GB、15.84 GB，远超 4 GB |
| mtime 精度 | 秒级，存在奇数秒（`13:53:07`），不是 2 秒 |
| macOS 写入痕迹 | 两卷根目录出现 `.fseventsd`，`MISC/._AC004.db`；说明 mac 会向相机卷写入 |
| Spotlight | 两卷索引均已禁用 |
| 卷根其他目录 | `MISC`、`LOST+FOUND`、`logs`、`System Volume Information`，均不扫描 |
| NAS | 192.168.1.100 本次 ping 与 445 端口均超时，SMB 版本仍待测 |

对设计的影响：

1. **设备身份**不能用 USB `iSerialNumber`。`device_key` 取产品名字符串中 `SN:` 之后的序列号（Linux 读 sysfs `product` 属性，mac 读 ioreg `USB Product Name`），解析失败时回退为 VID+PID+iSerial 并在事件中标记为弱身份。
2. **卷分类**以 LUN 顺序为主：LUN 0 为 `internal`，LUN 1 为 `sdcard`；容量区间为第二依据；卷名 `OsmoAction` 仅作旁证，`Untitled` 为用户格式化卷标，不可依赖。
3. **卷身份** `volume_key` 使用 exFAT 卷序列号，换卡后自动成为新卷。
4. **fingerprint** 的 mtime 归一化改为截断到整秒并统一 UTC。
5. **fixtures** 目录命名改为 `DCIM/DJI_003/`，大文件 profile 包含一个 16 GB 文件。
6. **性能目标**保留 81.1 GB / 30 分钟作为规划值，当前卡内实际数据为 42.5 GB。
7. mac 会在相机卷写入 `.fseventsd`，这是 macOS 行为而非本程序行为，扫描器忽略即可。

## 2. 技术栈

| 功能 | 技术 | 说明 |
| --- | --- | --- |
| 核心应用 | Go 1.25 | 单体模块化，`CGO_ENABLED=0` 交叉编译 ARM64 |
| USB 检测（Linux） | udev、sysfs | 监听事件并支持主动枚举 |
| USB 检测（macOS） | `diskutil`、`/Volumes` 轮询 | 仅开发用，见第 11 章 |
| 存储卷访问 | Linux exFAT / macOS exFAT | 源存储卷必须只读挂载或只读打开 |
| 文件扫描 | `os`、`filepath` | 递归扫描 DCIM |
| 增量备份 | Go Sync Engine | 任务规划、变化识别、冲突处理 |
| 数据传输 | Go 流式 I/O | 固定缓冲区，直接写入目标挂载目录 |
| SMB（Linux） | Linux CIFS 客户端 | `vers=auto` 协商 2.1 到 3.1.1，禁止 SMB1 |
| SMB（macOS 开发） | Finder / `mount_smbfs` 预挂载 | 程序只验证挂载类型，不负责挂载 |
| 元数据 | SQLite | `modernc.org/sqlite`，纯 Go，无 CGO |
| REST API | Go `net/http` | 轻量 HTTP 服务，用户名密码会话认证 |
| Web UI | Vue 3 + Vite，Node 25 | 构建后用 `embed` 嵌入 Go 程序 |
| 实时状态 | SSE | 推送聚合后的进度与事件 |
| 后台服务 | systemd | 开机自启、自动重启 |
| 日志 | slog / journald | 结构化日志，mac 上输出到 stdout |
| 故障注入 | `internal/faultinject` | 仅 `-tags faultinject` 构建时编译 |
| 测试 | `go test -race`；单元 / 集成 / 恢复三套，恢复套件含子进程崩溃重启 | 见第 12 章 |

首版只运行一个传输 Worker，默认缓冲区为 4 MiB，后续通过性能测试调整。

## 3. 总体架构与数据流

```text
DJI Osmo Action 5 Pro
  │ USB 3.x
  ▼
Platform Backend ─────────────────────────────────────────┐
  linux: udev/sysfs + mount helper                        │
  darwin: diskutil + /Volumes                             │ internal/platform
  sim:    配置定义的目录型设备                             │
  ▼                                                       │
Device Manager → Volume Manager (识别内部存储及 microSD) ◄┘
  │ 只读挂载 / 只读打开
  ▼
File Scanner (递归扫描 DCIM，忽略系统元数据文件)
  │
  ▼
Sync Engine (SQLite 历史记录对比) ──► Plan（dry-run 到此为止，输出计划）
  │
  ▼
Task Scheduler (单 Worker 队列)
  │
  ▼
Backup Worker (分块读取、断点恢复)  ◄── Fault Injector（测试构建）
  │
  ▼
Storage Probe (确认目标确实是期望的 CIFS / smbfs / 允许的本地目录)
  │
  ▼
目标临时文件 *.part
  │
  ▼
Verify Service (基础或 SHA-256)
  │
  ▼
正式文件重命名 + SQLite 状态更新
  │
  ▼
Web API / SSE / 日志
```

采用模块化单体应用，划分五个逻辑层：

- **Interface**：HTTP API、SSE、Web 页面。
- **Application**：调度、恢复、备份业务编排、运行模式门禁。
- **Domain**：设备、卷、源文件、任务、状态转换。
- **Infrastructure**：SQLite、CIFS、文件 I/O。
- **Platform**：操作系统相关的设备发现、挂载、挂载点校验，按 `GOOS` 和运行模式选择后端。

## 4. 工程目录

以下为 V0.6 实际目录（`*` 标记尚未实现的文件）。

```text
action-backup/
├── cmd/action-backup/
│   ├── main.go                   # --config / --check / --version；子命令 passwd、mount-helper
│   ├── passwd.go                 # action-backup passwd（bcrypt 写回配置）
│   └── mounthelper.go            # action-backup mount-helper：root 挂载守护入口
├── internal/
│   ├── config/                   # config.go 结构与默认值；validate.go 模式组合校验
│   ├── runmode/mode.go           # Mode、Guard（AllowDestinationWrite / TestEndpointsEnabled）
│   ├── platform/
│   │   ├── platform.go           # DeviceDetector / VolumeMounter / StorageProbe；类型别名指向 raw、fsinfo
│   │   ├── select.go             # 按 GOOS 与模式选择后端
│   │   ├── multi.go              # MultiDetector / MultiMounter
│   │   ├── notimpl.go            # 占位探测器 / 挂载器
│   │   ├── native_{darwin,linux,other}.go
│   │   ├── raw/                  # Device / Volume / Event 共享类型
│   │   ├── fsinfo/               # 挂载期望与匹配规则
│   │   ├── plist/                # Apple XML plist 解析器（标准库实现）
│   │   ├── darwin/               # ioreg.go 解析、detector.go 轮询、probe.go statfs、testdata/*.plist
│   │   ├── linux/                # sysfs.go + udevdb.go 枚举、detector.go 轮询、mountinfo.go、probe.go statfs（linux tag）、
│   │   │                         # helper_proto.go 协议与客户端（Mounter / StorageMounter）、helper_rules.go 白名单、
│   │   │                         # helper_server.go 守护、helper_ops_linux.go mount(2) / mount.cifs（linux tag）
│   │   └── sim/                  # 目录型模拟相机、LocalProbe、MountPassthrough
│   ├── device/manager.go         # 枚举、事件、持久化、OnAttached / OnDetached 回调
│   ├── domain/                   # device、volume、file、task（状态机）、plan、errors
│   ├── scanner/scanner.go        # 只读递归扫描
│   ├── syncengine/               # comparator.go 决策、engine.go 计划
│   ├── backup/                   # worker.go 传输 / 断点 / 校验；service.go 计划转任务
│   ├── scheduler/scheduler.go    # 单 Worker 队列、暂停 / 取消 / 重试、重连检查
│   ├── storage/                  # target.go 目标与写入门禁；layout/ 模板
│   ├── verify/ *                 # 校验逻辑目前在 backup/worker.go 内
│   ├── faultinject/              # points.go、rules.go；enabled.go（-tags faultinject）/ disabled.go
│   ├── database/                 # sqlite.go、migrations/0001_init.sql、repository/{devices,files,tasks,events}.go
│   ├── events/broadcaster.go     # 持久化 + 订阅；PublishTransient 用于进度
│   ├── update/                   # update.go 查询 GitHub Releases、状态、定期循环；version.go 版本比较与开发构建判定（§19）
│   ├── api/                      # router.go、handlers.go、middleware.go、response.go
│   ├── app/app.go                # 装配与生命周期
│   └── version/
├── web/
│   ├── embed.go                  # go:embed dist
│   ├── package.json vite.config.js index.html
│   ├── src/                      # Vue 3：App.vue、store.js、api.js、sse.js、components/
│   └── dist/                     # 构建产物，已入库，文件名固定（index.html、assets/index.js、assets/index.css）
├── configs/
│   ├── config.example.yaml       # 实机 production
│   ├── config.dryrun.yaml        # 实机 dry-run
│   ├── config.dev.yaml           # mac simulation（fixtures）
│   └── config.mac-dryrun.yaml    # mac 接真相机 dry-run
├── deployments/                  # action-backup.service、action-backup-mounter.service、99-action-backup.rules、install.sh、mount-helper/README.md
├── scripts/                      # build-arm64.sh、package.sh、deploy.sh、gen-fixtures.sh、test-performance.sh
├── tests/
│   ├── integration/app_test.go   # -tags faultinject,integration
│   ├── recovery/                 # -tags faultinject,recovery：harness、recovery、points、crash（子进程）
│   ├── fixtures/                 # 生成产物，不入库
│   └── manifests/
├── docs/development.md
├── Makefile · .golangci.yml · go.mod · go.sum · README.md
```

## 5. 模块职责与接口

| 模块 | 主要职责 | 建议接口 |
| --- | --- | --- |
| Platform Backend | 操作系统相关的设备发现、挂载、挂载点校验、目标挂载 | `DeviceDetector`、`VolumeMounter`、`StorageProbe`、`StorageMounter` |
| Run Mode | 模式解析、写入门禁、测试接口开关 | `Current`、`AllowDestinationWrite` |
| Device Manager | 设备发现、热插拔、USB 身份 | `Discover`、`Watch` |
| Volume Manager | 双卷枚举、分类、只读挂载和卸载 | `Discover`、`MountReadOnly`、`Unmount` |
| File Scanner | DCIM 递归扫描、文件元数据 | `Scan` |
| Sync Engine | 增量判断、任务计划、冲突 | `Plan` |
| Task Scheduler | 排队、暂停、恢复、重试 | `Enqueue`、`Pause`、`Resume` |
| Backup Worker | 流式传输、进度、断点 | `Transfer` |
| Storage Manager | 目标健康检查、路径和容量 | `CheckHealth` |
| Verify Service | 基础校验或 SHA-256 | `Verify` |
| Fault Injector | 测试构建中按规则注入故障 | `Hit(point, ctx) error` |
| Database | 存储和查询持久化数据 | Repository |
| Web API | 查询、设置、任务控制、测试控制 | HTTP Handlers |
| Event Manager | 广播进度和错误 | `Publish`、`Subscribe` |

核心接口（V0.6 实际签名）：

```go
// internal/platform（类型别名：RawDevice = raw.Device，MountInfo = fsinfo.Info 等）
type DeviceDetector interface {
    Discover(ctx context.Context) ([]RawDevice, error)
    Watch(ctx context.Context) (<-chan DeviceEvent, error)
}
type VolumeMounter interface {
    MountReadOnly(ctx context.Context, volume RawVolume) (string, error)
    Unmount(ctx context.Context, volume RawVolume) error
}
type StorageProbe interface {
    Probe(ctx context.Context, path string, expect MountExpectation) (MountInfo, error)
}
type StorageMounter interface {                 // 仅 Linux cifs：经挂载守护挂 / 卸 storage.share
    MountTarget(ctx context.Context) error
    UnmountTarget(ctx context.Context) error
}
// Linux 实现：linux.Detector（sysfs + udev 轮询）、linux.Mounter / linux.StorageMounter（socket 客户端）、linux.Probe；
// 守护端 linux.Server.Handle(ctx, HelperRequest) HelperResponse，规则 planSource / planTarget。

// internal/runmode
func (g *Guard) Mode() Mode
func (g *Guard) AllowDestinationWrite() bool   // dry-run 为 false
func (g *Guard) TestEndpointsEnabled() bool    // faultinject 构建且非 production

// internal/scanner
func (s *Scanner) Scan(ctx context.Context, device domain.Device, vol domain.Volume, mountPath string) (Result, error)

// internal/syncengine
func (e *Engine) Plan(ctx context.Context) (*domain.Plan, error)   // 只读，不写任务
func (c Comparator) Decide(ctx, volumeID int64, f domain.SourceFile, dest string) (domain.PlanAction, string, error)

// internal/backup
func (w *Worker) Run(ctx context.Context, job Job) Outcome        // Outcome.Status 为建议终态
func (s *Service) Start(ctx context.Context) (*StartResult, error) // 计划 → 任务 → 入队

// internal/scheduler
func (s *Scheduler) Enqueue(ctx, ids []int64) (int, error)
func (s *Scheduler) Pause() / Resume()
func (s *Scheduler) Cancel(ctx, id int64) error
func (s *Scheduler) Retry(ctx, id int64) error                    // 仅 FAILED / INTERRUPTED
func (s *Scheduler) Reconcile(ctx) int                            // 重新入队可恢复的 INTERRUPTED
func (s *Scheduler) State() State                                 // running / paused / current 进度

// internal/storage
func (t *Target) Probe(ctx) (platform.MountInfo, error)
func (t *Target) CreatePart / Commit / RemovePart / SetModTime    // 全部先过 Guard 与 Probe
func (t *Target) Resolve(rel string) (string, error)               // 越界校验

// internal/faultinject
type Injector interface { Hit(ctx context.Context, point Point, meta HitMeta) error }
type Controller interface { Injector; Rules(); Add(Rule) error; Remove(id) bool; Reset(); SetOnHit; SetDisconnect }
```

类型 `Device`、`Volume`、`SourceFile`、`BackupTask`、`Plan` 等在 `internal/domain` 中定义。`Plan` 包含待上传、跳过、冲突三类条目及字节总量，供 dry-run 输出和 API 预览共用。

## 6. SQLite 数据库

### 6.1 逻辑关系

```text
devices (设备)
  └── volumes (内部存储、microSD)
       └── source_files (源文件历史版本)
            └── backup_tasks (备份尝试与状态)
                 ├── checkpoints (可信传输断点)
                 └── events (任务相关事件)
```

`events` 允许无任务 ID 的系统事件。

### 6.2 初始化 Schema

```sql
PRAGMA foreign_keys = ON;
PRAGMA journal_mode = WAL;
PRAGMA synchronous = FULL;
PRAGMA busy_timeout = 5000;
-- 以上 PRAGMA 通过 DSN 设置，另加 _txlock=immediate：写事务一开始即取写锁，避免先读后写升级时直接 SQLITE_BUSY

CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS devices (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    device_key TEXT NOT NULL UNIQUE,
    vendor_id TEXT NOT NULL,
    product_id TEXT NOT NULL,
    serial_number TEXT,
    name TEXT NOT NULL,
    weak_identity INTEGER NOT NULL DEFAULT 0,   -- 序列号不可信（占位值或无法解析）
    last_seen_at TEXT,
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS volumes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    device_id INTEGER NOT NULL REFERENCES devices(id),
    volume_key TEXT NOT NULL,                   -- exFAT 卷序列号派生：mac VolumeUUID，Linux blkid UUID
    volume_type TEXT NOT NULL
        CHECK (volume_type IN ('internal','sdcard','unknown')),
    filesystem TEXT NOT NULL,
    label TEXT,
    lun INTEGER NOT NULL DEFAULT -1,
    size_bytes INTEGER NOT NULL DEFAULT 0,
    mount_path TEXT,
    last_seen_at TEXT,
    UNIQUE(device_id, volume_key)
);

CREATE TABLE IF NOT EXISTS source_files (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    volume_id INTEGER NOT NULL REFERENCES volumes(id),
    relative_path TEXT NOT NULL,
    file_size INTEGER NOT NULL CHECK (file_size >= 0),
    modified_time_ns INTEGER NOT NULL,
    fingerprint TEXT NOT NULL,
    last_seen_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(volume_id, relative_path, fingerprint)
);

CREATE TABLE IF NOT EXISTS backup_tasks (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    file_id INTEGER NOT NULL REFERENCES source_files(id),
    destination_path TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'PENDING'
        CHECK (status IN (
            'PENDING','QUEUED','TRANSFERRING',
            'VERIFYING','COMPLETED','FAILED',
            'INTERRUPTED','SKIPPED','CANCELLED'
        )),
    bytes_transferred INTEGER NOT NULL DEFAULT 0,
    retry_count INTEGER NOT NULL DEFAULT 0,
    verify_mode TEXT NOT NULL DEFAULT 'basic',
    checksum_sha256 TEXT,
    last_error TEXT,
    started_at TEXT,
    completed_at TEXT,
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS checkpoints (
    task_id INTEGER PRIMARY KEY
        REFERENCES backup_tasks(id) ON DELETE CASCADE,
    offset_bytes INTEGER NOT NULL DEFAULT 0,
    part_path TEXT NOT NULL,
    prefix_sha256 TEXT,                         -- base64 的 SHA-256 内部状态（MarshalBinary），basic 模式为空
    updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id INTEGER REFERENCES backup_tasks(id),
    level TEXT NOT NULL,
    event_type TEXT NOT NULL,
    message TEXT NOT NULL,
    data TEXT,                                  -- JSON 附加数据
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_files_volume_path
    ON source_files(volume_id, relative_path);
CREATE INDEX IF NOT EXISTS idx_tasks_status
    ON backup_tasks(status, created_at);
CREATE INDEX IF NOT EXISTS idx_tasks_file
    ON backup_tasks(file_id);
CREATE INDEX IF NOT EXISTS idx_events_created
    ON events(created_at);
```

数据库规则：

- 相同路径发生变化时保留不同文件版本，不能简单覆盖历史状态。
- `fingerprint` 使用设备与卷身份、相对路径、文件大小、修改时间等生成，避免只看文件名。
- **修改时间在参与 `fingerprint` 前截断到整秒并统一为 UTC**。exFAT 时间戳为 2 秒 DOS 时间加 10 ms 增量字段并带时区偏移，mac 实测报告秒级值；Linux exfat 驱动与 macOS 对时区字段的解释可能不一致，归一化后可避免同一文件在不同主机上产生不同指纹。
- 一个数据库文件只属于一台主机。mac 开发库与实机库不互相拷贝。
- `COMPLETED` 代表正式目标文件已成功确认。增量跳过前仍需确认 NAS 目标存在且必要元数据匹配。
- SQLite 保存在 OEC-Turbo 本地持久化文件系统，不放在 NAS 或 `/run`。mac 上默认在工程目录 `./dev/backup-dev.db`。
- dry-run 模式不插入 `source_files`、任务和断点，只写 `events`；`Plan` 本身不落库，保存在内存并以 `plan.generated` 事件发布。
- 计划中的 skip 项不建 `SKIPPED` 任务，避免每次插卡都新增记录；历史查询以 `COMPLETED` 任务为准。
- `source_files` 在建任务时按 `(volume_id, relative_path, fingerprint)` upsert；同一源文件未终结的任务被复用而不是重复创建。
- 数据库迁移使用 `schema_migrations` 表做明确的版本管理。
- 断点记录只能前进至经过确认的可靠写入位置。
- `backup_tasks.destination_path` 保存按 `destination_layout` 渲染后的实际相对路径；模板变更不影响历史任务判断。
- WAL 配合合理的 checkpoint 策略，避免异常磁盘占用。

## 7. 任务状态机

```text
PENDING → QUEUED → TRANSFERRING → VERIFYING → COMPLETED
               ▲           │           │
               │           ├───────────┴──→ FAILED
               │           ├──────────────→ INTERRUPTED
               └───────────┘ (用户暂停，保留断点)
非终态 ────────────────────────────────→ CANCELLED

已存在可信备份：PENDING → SKIPPED（V0.4 实现中不落库）
恢复或重试：INTERRUPTED / FAILED → QUEUED（retry_count + 1）
```

| 当前状态 | 条件 | 下一状态 |
| --- | --- | --- |
| PENDING | 生成任务并入队 | QUEUED |
| QUEUED | Worker 开始 | TRANSFERRING |
| TRANSFERRING | 文件传输完成 | VERIFYING |
| VERIFYING | 校验及归档完成 | COMPLETED |
| TRANSFERRING | USB/网络断开 | INTERRUPTED |
| TRANSFERRING、VERIFYING | 校验或写入失败 | FAILED |
| TRANSFERRING | 用户暂停 | QUEUED（保留断点，不计重试） |
| INTERRUPTED、FAILED | 可恢复或允许重试 | QUEUED（retry_count + 1） |
| PENDING | 已有可信备份 | SKIPPED |
| 非终态 | 用户取消 | CANCELLED |

用户暂停由调度器管理：当前任务在下一个检查点停下并回到 `QUEUED`，断点保留，不计入 `retry_count`。取消会删除 `.part`。状态转移在 `repository.Tasks.Transition` 内按上表校验并落库，非法转移返回 `ErrInvalidTransition`；`retry` 接口只接受 `FAILED` / `INTERRUPTED`。

dry-run 不进入状态机：Sync Engine 产出 `Plan` 后流程结束，`Plan` 以 `plan.generated` 事件发布并保存在内存中供 API 查询，不写 `backup_tasks`。

### 7.1 文件传输及归档

1. 验证相机卷和目标存储已经就绪（`StorageProbe` 确认挂载类型）。
2. 检查已完成文件及相关任务记录。
3. 创建目标 `.part` 文件或验证历史临时文件。
4. 从源文件指定偏移位置分块读取，顺序写入目标。
5. 定期确认持久化并更新 SQLite 断点。
6. 数据写完后按配置执行基础或 SHA-256 校验。
7. 在同一目标文件系统内完成临时文件正式重命名。
8. 更新数据库任务状态，发送完成事件。

每一步在测试构建中都经过一个故障注入点，见 12.4。

### 7.2 中断恢复

服务重启后：

1. 把上次未完成的 `TRANSFERRING`、`VERIFYING` 任务标记为 `INTERRUPTED`。
2. 重新发现设备及卷，确认源文件身份没有变化。
3. 检查目标挂载和 `.part` 文件。
4. 对照数据库 checkpoint 与临时文件实际大小及有效数据前缀。
5. 对未确认的后续数据截断或重新校验。
6. 对可信断点继续，否则重新上传当前文件。
7. 重新执行文件完成校验，正式归档。

不得只根据 `.part` 大小认定数据正确。V0.3 实现：只信任数据库断点偏移，`.part` 超出偏移的部分截断；断点大于 `.part` 实际大小、哈希状态缺失或无法恢复时视为不可信，删除断点从头重传。

### 7.3 失败分类、重试与冲突策略

| 故障 | 任务状态 | 后续 |
| --- | --- | --- |
| 源打不开、读错误、设备拔出、规划后源文件身份变化前的读失败 | INTERRUPTED | 自动重连重试 |
| 目标探测失败（未挂载、NAS 掉线、挂载点变成本地目录） | INTERRUPTED | 自动重连重试 |
| 写入失败且事后探测目标仍失败 | INTERRUPTED | 自动重连重试 |
| 写入、fsync、重命名、断点落库失败且目标在线 | FAILED | 人工 retry |
| 校验失败（源身份变化、字节数不符、SHA-256 不一致） | FAILED，断点与 `.part` 丢弃 | 人工 retry，从头重传 |
| 用户取消 | CANCELLED，`.part` 删除 | 无 |

- **自动重试**：调度器每 `backup.reconcile_interval_sec` 执行一次重连检查，对设备在位、目标探测通过且 `retry_count < backup.retry_count` 的 INTERRUPTED 任务重新入队；`backups/start` 也会重新入队未用尽次数的 FAILED / INTERRUPTED 任务，并在结果中报告 `tasks_retry_exhausted`。
- **人工重试**不受次数限制。
- **冲突**：目标已有正式文件而数据库无完成记录、或记录指纹不同、或大小不符时，计划标记 `conflict`。`backup.on_conflict`：`manual`（默认，不建任务不动目标）、`overwrite`（照常上传覆盖）、`skip`（忽略）。
- **目标不可用时的计划**：无法核对目标，已完成文件按 `upload / target_unverified` 列出，`Plan.target_checked = false` 并附警告；宁可多列不漏。

## 8. 完整性校验

### 8.1 默认 basic

- 源文件身份、大小及修改时间复核。
- 累计成功写入的字节数校验。
- 目标临时文件大小校验。
- 检查写入、同步、关闭和重命名的错误。
- 成功后记录完整的备份元数据。

基础校验不提供完整内容逐字节一致性保证。

### 8.2 可选 sha256

- 传输中计算完整源文件 SHA-256。
- 从目标文件重新计算完整 SHA-256 并比较。
- 断点恢复时从 `checkpoints.prefix_sha256` 恢复 SHA-256 内部状态，无需重读目标前缀。
- CIFS 目标重新读取会额外消耗网络时间和 NAS I/O。
- 任一校验失败都会删除断点与 `.part`，下次重试从头开始，避免在已损坏的前缀上反复失败。

simulation 模式下 fixtures 自带 `manifest.sha256`，集成测试用它核对最终归档文件，与程序自身的校验结果互相印证。

## 9. REST API

统一前缀：`/api/v1`。

| HTTP | 路径 | 作用 | 可用模式 |
| --- | --- | --- | --- |
| GET | `/system/status` | 版本、模式、任务计数、调度器状态与当前进度 | 全部 |
| GET | `/system/health` | USB、NAS、数据库健康情况 | 全部 |
| GET | `/system/mode` | 当前运行模式、平台后端、是否测试构建 | 全部 |
| GET | `/system/update` | 新版本检查的当前状态（当前 / 最新版本、发布说明、上次检查时间、错误）；不触发网络请求 | 全部 |
| POST | `/system/update/check` | 立即向 GitHub 查一次并返回最新状态；失败 502 `UPDATE_CHECK_FAILED`，响应体同时带当前状态 | 全部 |
| POST | `/system/update/apply` | 开始安装最新版本（下载、验签、等空闲、提交安装请求），202 立即返回；不支持时 501，其它拒绝 409（§19.6.1） | Linux 正式构建 |
| POST | `/auth/login` | 用户名密码登录，签发会话 Cookie | 全部 |
| POST | `/auth/logout` | 注销当前会话 | 全部 |
| GET | `/auth/me` | 当前会话信息和 CSRF Token | 全部 |
| GET | `/devices` | 设备列表 | 全部 |
| GET | `/devices/{id}/volumes` | 存储卷列表 | 全部 |
| POST | `/devices/rescan` | 重新扫描设备 | 全部 |
| POST | `/backups/plan` | 扫描并生成备份计划，不写入 | 全部 |
| GET | `/backups/plan` | 最近一次计划 | 全部 |
| GET | `/tasks` | 任务列表、分页、过滤 | 全部 |
| GET | `/tasks/{id}` | 任务详情，正在传输时附实时进度 | 全部 |
| POST | `/backups/start` | 扫描、规划、建任务、入队；返回计划与建任务统计 | production、simulation |
| POST | `/backups/pause` | 暂停备份 | production、simulation |
| POST | `/backups/resume` | 恢复备份 | production、simulation |
| POST | `/tasks/{id}/retry` | 重试失败任务 | production、simulation |
| POST | `/tasks/{id}/cancel` | 取消任务 | production、simulation |
| GET | `/backups/history` | 历史备份 | 全部 |
| GET | `/config` | 脱敏配置 | 全部 |
| PATCH | `/config` | 修改白名单配置项 | 全部 |
| POST | `/storage/test` | 测试目标存储连接 | 全部 |
| POST | `/notifications/test` | 向指定渠道（`{"channel":"meow"|"telegram"}`）发一条测试消息，不看开关只要求字段齐全 | 全部 |
| GET | `/events` | 历史事件 | 全部 |
| GET | `/events/stream` | SSE 实时进度 | 全部 |
| GET | `/test/faults` | 当前故障规则 | 测试构建且非 production |
| POST | `/test/faults` | 新增故障规则 | 同上 |
| DELETE | `/test/faults/{id}` | 删除故障规则 | 同上 |
| POST | `/test/sim/devices/{key}/attach` | 模拟插入设备 | simulation |
| POST | `/test/sim/devices/{key}/detach` | 模拟拔出设备 | simulation |
| GET | `/test/faults` | 当前故障规则与命中计数 | 测试构建且非 production |
| POST | `/test/faults` | 新增故障规则（JSON） | 同上 |
| DELETE | `/test/faults/{id}` | 删除故障规则 | 同上 |
| POST | `/test/faults/reset` | 清空故障规则 | 同上 |
| GET | `/test/sim/devices` | 模拟设备及插入状态 | simulation |
| POST | `/test/reset` * | 清空测试数据库和目标目录 | 未实现 |

在 dry-run 模式调用 `/backups/start`、pause、resume、cancel、retry 返回 `409`，错误码 `MODE_READ_ONLY`。`/test/*` 在 production 构建中不注册路由，返回 `404`。`server.auth.enabled: true` 时未登录访问返回 `401 UNAUTHENTICATED`，写操作缺少或错误的 CSRF Token 返回 `403 CSRF_INVALID`，登录限速返回 `429 RATE_LIMITED`（带 `Retry-After: 60`）。`GET /devices/{id}/volumes` 的 `{id}` 接受数据库 ID 或设备键。

示例响应：

```json
{
  "data": {
    "task_id": 1024,
    "status": "TRANSFERRING",
    "total_bytes": 5031028178,
    "transferred_bytes": 2147483648,
    "speed_bytes_per_sec": 58600000
  }
}
```

`GET /system/mode`：

```json
{
  "data": {
    "mode": "simulation",
    "platform": "darwin",
    "device_backend": "sim",
    "storage_backend": "local",
    "faultinject_build": true,
    "destination_write_allowed": true
  }
}
```

`POST /backups/plan`：

```json
{
  "data": {
    "generated_at": "2026-10-22T10:15:00Z",
    "devices": 1,
    "volumes": 2,
    "upload": { "files": 37, "bytes": 81123456789 },
    "skip": { "files": 12, "bytes": 25000000000 },
    "conflict": { "files": 0 },
    "items": [
      { "volume": "sdcard", "path": "DCIM/100MEDIA/DJI_20261020_0001_D.MP4",
        "size": 5031028178, "action": "upload", "reason": "not_backed_up" }
    ]
  }
}
```

错误格式：

```json
{
  "error": {
    "code": "SMB_UNAVAILABLE",
    "message": "NAS SMB share is unavailable",
    "retryable": true
  }
}
```

SSE：

```text
event: backup.progress
data: {"task_id":1024,"progress":42.6,"speed":58600000}

event: backup.completed
data: {"task_id":1024,"verified":true}

event: plan.generated
data: {"upload_files":37,"upload_bytes":81123456789,"skip_files":12}

event: fault.hit
data: {"rule":"nas-drop-midfile","point":"dest.write","task_id":1024}
```

认证与安全（V0.5 实现，`internal/api/auth`）：

- 除 `/auth/login`、静态资源和 `/system/health` 外，所有 `/api/` 接口要求有效会话；门禁中间件位于超时包装之外，所以 SSE 同样受保护。认证未启用时中间件直接放行，`/auth/me` 返回 `auth_enabled: false`。production 模式的配置校验强制 `server.auth.enabled: true`。
- 口令以 bcrypt（cost 12）保存在配置文件，`action-backup passwd --config <path> [--username <name>] [--stdin]` 交互式读两次口令写入，非终端输入必须显式 `--stdin`，至少 8 字符，不接受命令行明文参数，不自动改 `auth.enabled`。
- 登录成功签发 `crypto/rand` 32 字节会话 ID，存放在 `ab_session` Cookie（`HttpOnly; SameSite=Strict; Path=/`；局域网 HTTP 部署不设 `Secure`），服务端内存会话表，默认 24 小时过期，重启后需重新登录。
- 写操作（POST、PATCH、DELETE）必须携带 `X-CSRF-Token`，值为每会话随机 32 字节，由登录响应与 `/auth/me` 返回。
- 登录接口按来源 IP（`RemoteAddr`，不信任代理头）限速，默认每分钟 5 次，成功与失败都计数；用户名不匹配时仍执行一次 bcrypt 比较以避免时序泄露。`auth.login`、`auth.logout`、`auth.login_failed`（含 IP 与用户名）、`auth.login_rate_limited` 记入 `events`。
- 输入校验、请求超时、分页限制和日志脱敏同 V1.0。Web UI 在非 production 模式下顶部显示模式横幅，并在测试构建下显示测试面板。

SSE 实现细节：连接即发送 `hello`（模式、版本、服务器时间），之后转发事件总线的全部事件，`event:` 为事件类型，`id:` 为持久化事件 ID（瞬态进度事件无 `id`），`data:` 为完整事件记录 JSON；一次刷出中同一任务的多条 `backup.progress` 只保留最新；每 15 秒发送 `: ping` 注释保活；该路由不经 30 秒 `http.TimeoutHandler`（它会缓冲响应且不实现 Flusher），其余接口仍经超时包装。

`GET /config` 返回 `{path, config, patchable}`，`config` 为脱敏副本（`password_hash` 为 `***`）。`PATCH /config` 只接受白名单：`device.auto_backup`、`backup.verify_mode`、`backup.on_conflict`、`backup.retry_count`（0 到 100）、`backup.reconcile_interval_sec`（1 到 3600）、`logging.level`；流程为类型校验 → 应用到副本并 `Validate` → 用 yaml.v3 Node 原地改写配置文件（保留注释与顺序，临时文件 + rename 原子替换）→ 应用到运行中的组件 → 发 `config.changed` 事件。`POST /storage/test` 只读：挂载类型、来源、剩余空间、根目录是否存在、耗时，不写任何文件。

V0.7 起白名单增加 `notifications.*`（§10.4）；V0.9 起增加 `update.check_enabled`、`update.check_interval_hours`（1 到 720）、`update.channel`（`stable` / `prerelease`）与 `notifications.on.update_available`。`update.api_base_url` 与发布仓库地址**不在**白名单：网页不能改更新源。`GET /system/update` 的字段见 §19.5。

## 10. 配置文件

### 10.1 实机 production：`/etc/action-backup/config.yaml`

```yaml
mode: "production"            # production | dry-run | simulation

server:
  listen: "0.0.0.0:8080"
  auth:
    enabled: true
    username: "admin"
    password_hash: "$2a$12$..."       # 由 action-backup passwd 生成
    session_ttl_hours: 24
    login_rate_limit_per_min: 5

device:
  vendor_id: "0506"
  product_id: "8002"
  auto_detect: true
  auto_backup: true
  mount_root: "/run/action-backup-mounter/source"   # 守护只读挂载相机卷到 <mount_root>/sdX
  readonly: true

backup:
  include_internal: true        # 省略即 true
  include_sdcard: true
  source_directory: "DCIM"
  file_extensions:
    - ".mp4"
  on_conflict: "manual"         # manual | overwrite | skip，见 §7.3
  reconcile_interval_sec: 30    # INTERRUPTED 任务重连检查周期
  ignore_patterns:              # 默认值，通常不需改
    - "._*"
    - ".DS_Store"
    - ".Spotlight-V100"
    - ".fseventsd"
    - ".Trashes"
    - "System Volume Information"
  worker_count: 1
  buffer_size_mb: 4
  retry_count: 3                # 自动重试上限；人工 retry 不受限
  checkpoint_interval_sec: 30   # 每个检查点 fsync 一次；实机 5 s / 64 MB 把 CIFS 吞吐压到 24 MB/s
  checkpoint_bytes_mb: 512
  verify_mode: "basic"
  delete_source: false

storage:
  type: "cifs"                  # cifs | premounted | local
  share: "//192.168.1.100/VideoBackup"
  mount_point: "/mnt/action-backup-nas"
  smb_version: "auto"               # auto = 协商 2.1 到 3.1.1；或固定 "3.1.1"
  credentials_file: "/etc/action-backup/smb.credentials"
  # NAS 不可用时可改本地目录目标（显式开关，默认禁止）：
  #   type: "local"
  #   local_root: "/var/lib/action-backup/local-target"   # 必须在服务 ReadWritePaths 内
  #   allow_local_destination: true
  destination_root: "DJI-Action5Pro"
  destination_layout: "{volume_type}/{relative_path}"

database:
  path: "/var/lib/action-backup/backup.db"

logging:
  level: "info"
  format: "json"
```

`storage.type` 含义：

| 取值 | 行为 | 允许的模式 |
| --- | --- | --- |
| `cifs` | 挂载守护 `action-backup-mounter` 经 `mount.cifs` 挂载（`vers` 自动协商 2.1–3.1.1，SMB1 拒绝），主服务用 `statfs` 确认 `CIFS_SUPER_MAGIC`；启动与每个 reconcile 周期检查并重挂 | production、dry-run（Linux） |
| `premounted` | 操作系统已挂载，程序只用 `StorageProbe` 确认该路径是 `cifs` 或 `smbfs` 且来源匹配 `share` | 全部 |
| `local` | 普通本地目录 | simulation（`simulation.allow_local_destination: true`）；dry-run / production 须显式 `storage.allow_local_destination: true` |

`storage.destination_layout` 是目标路径模板，最终路径为 `<mount_point>/<destination_root>/<渲染结果>`。占位符：

| 占位符 | 含义 | 示例 |
| --- | --- | --- |
| `{relative_path}` | 源文件相对卷根的路径，含目录和文件名 | `DCIM/100MEDIA/DJI_20261020_0001_D.MP4` |
| `{filename}` | 仅文件名 | `DJI_20261020_0001_D.MP4` |
| `{volume_type}` | `internal` 或 `sdcard` | `sdcard` |
| `{volume_label}` | 卷标，为空时回退为 `{volume_type}` | `DJI_SD` |
| `{serial}` | 设备序列号，为空时回退为 `unknown` | `1ZXXXX` |
| `{device_name}` | 设备名 | `Osmo-Action-5-Pro` |
| `{mtime:LAYOUT}` | 源文件修改时间，Go 时间格式 | `{mtime:2006/01-02}` → `2026/10-20` |
| `{date}` | 等价于 `{mtime:2006-01-02}` | `2026-10-20` |

示例：

```yaml
destination_layout: "{volume_type}/{relative_path}"                 # 默认
destination_layout: "{serial}/{volume_type}/{relative_path}"        # 按设备分
destination_layout: "{mtime:2006}/{mtime:01}/{filename}"            # 按年月扁平
```

校验规则：模板必须包含 `{relative_path}` 或 `{filename}`；渲染结果不得含 `..`、绝对路径或空段；渲染后必须仍位于 `destination_root` 之下，否则任务 `FAILED`。修改模板只影响新任务，历史任务按 `backup_tasks.destination_path` 中保存的实际路径判断是否已完成。

### 10.2 实机 dry-run：`configs/config.dryrun.yaml`

与 production 相同，仅 `mode: "dry-run"`，且 `server.auth.enabled: false`（认证到 V0.5 才实现）。另有 `configs/config.mac-dryrun.yaml`：mac 接真相机、`storage.type: premounted` 指向 Finder 挂载点，NAS 未挂载时计划仍生成但 `target_checked: false`。此模式下 `device.auto_backup` 触发的动作是生成计划而非上传，目标目录不会出现任何文件。

### 10.3 mac simulation：`configs/config.dev.yaml`

```yaml
mode: "simulation"

server:
  listen: "127.0.0.1:8080"
  auth:
    enabled: false                # 开发时免登录；设为 true 可测试登录流程

device:
  vendor_id: "0506"
  product_id: "8002"
  auto_detect: true
  auto_backup: false            # 开发时手动触发
  readonly: true

simulation:
  device_source: "fixtures"     # fixtures | volumes | both
  fixtures:
    devices:
      - key: "sim-cam-1"
        serial: "SIM0001"
        volumes:
          - type: "internal"
            path: "./tests/fixtures/cam1/internal"
          - type: "sdcard"
            path: "./tests/fixtures/cam1/sdcard"
    attached_on_start: true
  volumes:                      # 读取 mac 自动挂载的真相机
    poll_interval_sec: 2
    match_by: "usb_vid_pid"     # usb_vid_pid | volume_name
  allow_local_destination: true

backup:
  include_internal: true
  include_sdcard: true
  source_directory: "DCIM"
  file_extensions: [".mp4"]
  worker_count: 1
  buffer_size_mb: 4
  retry_count: 3
  checkpoint_interval_sec: 2
  checkpoint_bytes_mb: 16
  verify_mode: "sha256"
  delete_source: false

storage:
  type: "local"                 # 改为 premounted 即可写 Finder 挂载的真 NAS
  local_root: "./dev/nas"
  # premounted 示例：
  # type: "premounted"
  # mount_point: "/Volumes/VideoBackup"
  # share: "//192.168.1.100/VideoBackup"
  destination_root: "DJI-Action5Pro-TEST"
  destination_layout: "{volume_type}/{relative_path}"

faults:
  enabled: true
  rules: []                     # 规则结构见 §12.4；也可运行时通过 /api/v1/test/faults 增删

database:
  path: "./dev/backup-dev.db"

logging:
  level: "debug"
  format: "text"
```

配置合法性由 `internal/config/modes.go` 统一校验：

- `mode: production` 禁止 `simulation`、`faults` 字段，禁止 `storage.type: local`。
- `mode: simulation` 使用真 NAS 时，`destination_root` 必须以 `-TEST` 结尾，否则拒绝启动。
- `faults.enabled: true` 但不是 faultinject 构建时，启动失败并提示。
- `server.auth.enabled: true` 时 `password_hash` 必须是合法 bcrypt 串；production 模式强制 `enabled: true`。
- `destination_layout` 按上表规则校验，非法模板拒绝启动。
- SMB 用户名与密码存放在独立凭据文件中，权限限制为 `0600`，不能在 Web API 和日志中直接暴露。

### 10.4 推送通知：`notifications`

```yaml
notifications:
  enabled: false              # 总开关；为 true 时至少要启用一个渠道
  site_url: ""                # 通知里附带的网页链接，如 http://172.20.31.6:8080；留空不附
  on:                         # 省略即 true
    device_attached: true     # 相机插入并识别
    backup_started: true      # 计划完成、任务入队（新建 / 重入队数、待上传大小）
    backup_finished: true     # 一批任务全部结束发一条汇总（成功数 / 大小 / 用时 / 失败数）
    backup_failed: true       # task.failed / task.interrupted，30 秒内合并为一条
    update_available: true    # 检查到新版本（update.available），每个版本只发一次（V0.9）
  meow:
    enabled: false
    nickname: ""              # MeoW App 注册的昵称；昵称即密钥，无其它凭据
    # base_url: https://api.chuckfang.com   仅测试时改
  telegram:
    enabled: false
    bot_token: ""             # API 输出与事件中脱敏为 ***
    chat_id: ""
    proxy_url: ""             # 设备直连不到 api.telegram.org，填 http://host:port 或 socks5://host:port
    # base_url: https://api.telegram.org    仅测试时改
```

以上除 `base_url` 外全部可在网页「设置 → 通知」修改，立即生效并写回文件。MeoW 非会员限速 3 条 / 3 秒、15 条 / 分钟、60 条 / 小时，分发器按同渠道 1 秒间隔串行发送并合并失败事件，正常使用不会触碰。

### 10.5 新版本检查：`update`

```yaml
update:
  check_enabled: true         # 定期检查；省略即 true。关闭后仍可在网页手动检查
  check_interval_hours: 12    # 1–720
  channel: stable             # stable 只看正式版 | prerelease 含 rc / beta 等预发布
  # api_base_url: https://api.github.com   仅测试时改
```

前三项可在网页「设置 → 软件更新」修改，立即生效并写回文件；切换通道会丢弃上一次的检查结果。发布仓库（`xuebkgithub/DJI-Osmo-Action-5-Pro-Auto-Backup`）是 `internal/update.Repository` 常量，不提供配置项。HTTP 客户端走环境变量代理（`HTTPS_PROXY`），设备到 GitHub 不通时可在 systemd 单元里用 `Environment=` 设置；专门的代理 / 镜像配置项留到第三阶段（§19.7）。

## 11. macOS 开发环境

### 11.1 工具链

| 工具 | 版本 | 用途 |
| --- | --- | --- |
| Go | 1.25.5（已安装） | 主程序，`go.mod` 写 `go 1.25`；依赖锁定 `modernc.org/sqlite v1.59.0`、`golang.org/x/sys v0.47.0`（更新版本要求 Go 1.26）；goproxy.cn 不可用时 `GOPROXY=https://proxy.golang.org,direct` |
| Node.js | 25.5.0（已安装，非 LTS，仅做 Web 构建可接受） | Web UI 构建 |
| pnpm | 10.33.0（已安装） | Web 依赖管理 |
| golangci-lint | brew 最新（已安装） | 静态检查，合并前零告警 |
| diskutil、system_profiler | macOS 自带 | darwin 平台后端 |
| `mount_smbfs` / Finder | macOS 自带 | 预挂载 NAS |

不需要 Docker、虚拟机或 CGO。`modernc.org/sqlite` 是纯 Go，`CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build` 可以直接在 mac 上产出实机二进制。

### 11.2 Makefile 目标

```makefile
tools              # 检查 go / node / pnpm 版本
fixtures           # scripts/gen-fixtures.sh 生成模拟相机卷
dev                # go run -tags faultinject ./cmd/action-backup --config configs/config.dev.yaml
web-install        # pnpm --dir web install --frozen-lockfile
web-dev            # pnpm --dir web dev，Vite 代理 /api 到 127.0.0.1:8080
web-build          # 构建 web/dist（固定文件名，已入库），改前端后必须执行并提交
lint               # golangci-lint run
test               # go test ./...
test-integration   # go test -tags faultinject,integration ./tests/integration/...
test-recovery      # go test -tags faultinject,recovery ./tests/recovery/...
build-arm64        # CGO_ENABLED=0 GOOS=linux GOARCH=arm64，production 构建，无 faultinject
build-arm64-test   # 同上但带 -tags faultinject，二进制名 action-backup-test
package            # dist/action-backup-<version>-linux-arm64.tar.gz，含 service、rules、helper、install.sh
deploy             # 预留；OEC-Turbo 可 SSH 后实现 scp + systemctl restart
```

`build-arm64` 与 `build-arm64-test` 产物名称不同，install.sh 只接受前者安装到 `/opt/action-backup/action-backup`，避免把测试构建误装为正式服务。

### 11.3 平台抽象层

`internal/platform/select.go` 按以下规则选择后端：

| GOOS | mode | DeviceDetector | VolumeMounter | StorageProbe |
| --- | --- | --- | --- | --- |
| linux | production / dry-run | sysfs + udev 数据库轮询 | 挂载守护（unix socket，root） | statfs CIFS magic + mountinfo |
| linux | simulation | sim | sim（目录即挂载点） | sim（本地目录）或 linux probe |
| darwin | dry-run | diskutil | darwin（只读重新挂载可选） | darwin smbfs probe |
| darwin | simulation | sim、diskutil 或两者 | 同上 | sim 或 darwin probe |
| darwin | production | 拒绝启动 | | |

linux 后端细节（V0.6 实现，实机 OEC-Turbo 2026-10-27）：

- **设备发现**：每 2 秒枚举 `/sys/bus/usb/devices/*`，匹配 `idVendor / idProduct`；序列号从 `product`（`OsmoAction5pro_SN:0A1B2C3D`）解析，`serial` 是占位 `123456789ABCDEF`。沿接口 → `host*` → `target*` 找 `h:c:t:l` 目录，`l` 即 LUN，`block/<name>` 给出 `sda / sdb`；`size`（512 字节扇区）为 0 视为无介质跳过；`dev`（`8:0`）对应 `/run/udev/data/b8:0`，取 `ID_FS_UUID / ID_FS_LABEL / ID_FS_TYPE`。任一 LUN 的块设备或 udev 记录未就绪则整台设备等下一轮。卷键 `uuid:<小写 uuid>`，缺 UUID 时 `usb:<serial>:lun<n>:<size>`。已在 `<mount_root>/<name>` 挂载且来源一致时直接带 `MountPath`（服务重启后沿用）。
- **只读挂载**：`MountReadOnly` 经 socket 请求守护 `mount-source`；守护反查 sysfs 确认设备属于配置的 VID/PID、udev 的文件系统为 exfat / vfat，`mount(2)` 以 `MS_RDONLY|MS_NOSUID|MS_NODEV|MS_NOEXEC`、`uid/gid=actionbackup,umask=0077,iocharset=utf8` 挂到 `<mount_root>/<name>`；已挂载但 `statfs` 失败（拔出残留）先 `MNT_DETACH` 再挂。拔出事件 → `OnDetached` → `umount-source`。挂载点名随内核设备名变化（实测重插后 `sdb → sdc`），外部不要写死。
- **目标挂载**：`StorageMounter.MountTarget` → 守护 `mount-target`：校验 `share` 形如 `//host/share`、`mount_point` 绝对、`credentials_file` 存在且 0600、`smb_version` ∈ {auto, 2.0, 2.1, 3.0, 3.02, 3.1.1}，调 `mount -t cifs ... -o credentials=,uid=,gid=,file_mode=0660,dir_mode=0770,nosuid,nodev,noexec,iocharset=utf8[,vers=]`。实测对 `//172.20.100.254/home` 协商 3.1.1。
- **只读保护**：挂载本身只读，程序仍只以 `O_RDONLY` 打开源文件。

darwin 后端细节：

- **设备发现**（V0.2 实现）：每 `poll_interval_sec` 执行 `ioreg -p IOUSB -a -l -w0`，用自带的 plist 解析器找 `idVendor / idProduct` 匹配的节点；对每个相机执行 `ioreg -p IOService -a -l -w0 -r -n <节点名>` 取子树中的 `IOSCSILogicalUnitNub`（LUN）与整盘 `IOMedia`（BSD 名）；再对每个 BSD 名执行 `diskutil info -plist` 取 `VolumeName`、`MountPoint`、`FilesystemType`、`Size`、`VolumeUUID`。所有卷都有挂载点才视为就绪，否则等待下一轮。`system_profiler SPUSBDataType` 在本机返回空，不使用。
- **卷分类**：实测 LUN 0 为内置存储 `OsmoAction`，LUN 1 为 microSD。优先用 LUN 顺序，其次按容量区间（50 GB 级为 internal），卷名只作旁证。mac 上 LUN 通过 `ioreg -r -c IOSCSILogicalUnitNub -l` 的 `SCSI Logical Unit Number` 与 `BSD Name` 对应；Linux 上通过 sysfs `scsi_device` 的 `h:c:t:l` 中的 `l`。规则与 linux 后端共用 `internal/volume/classify.go`。
- **设备序列号**：从 ioreg `USB Product Name`（`OsmoAction5pro_SN:xxxx`）解析，不用 `USB Serial Number`。
- **只读保护**：macOS 默认以读写方式自动挂载 exFAT，不做重新只读挂载。保护完全依赖程序自身：源卷路径只用 `O_RDONLY` 打开，`scanner` 和 `backup` 包对源路径没有任何创建、写入、重命名、删除调用，并有单元测试用只读 `fstest.MapFS` 和文件系统权限两种方式验证。
- **macOS 污染问题**：相机接 mac 后 Finder 和 Spotlight 会在卷上写入 `._*`、`.DS_Store`、`.Spotlight-V100`、`.fseventsd`。扫描器一律忽略；实机 Linux 上这些文件同样存在，不会被当作备份对象。
- **目标存储**：在 Finder 中挂载 NAS 后，`storage.type: premounted`，`mount_point: /Volumes/<share>`。probe 用 `statfs` 检查 `f_fstypename == "smbfs"` 且 `f_mntfromname` 包含配置的 `share` 主机与共享名。Finder 断开后 `/Volumes/<share>` 目录会消失或变为普通目录，probe 必须失败。
- **fsync**：`File.Sync()` 在 smbfs 上可用；但 macOS smbfs 与 Linux CIFS 的缓存行为不同，吞吐数据不可互相推断。

### 11.4 mac 上的两种数据源

| 来源 | 配置 | 适用 |
| --- | --- | --- |
| fixtures 目录 | `simulation.device_source: fixtures` | 单元与集成测试、故障注入、CI |
| `/Volumes` 真相机 | `simulation.device_source: volumes` 或 `both` | 扫描器、分类规则、真实文件名与时间戳验证 |

真 NAS 与本地目录目标同理切换 `storage.type`。四种组合都应在 V0.3 完成前跑通一次。

### 11.5 开发循环

1. `make fixtures` 生成 `tests/fixtures/cam1`。
2. 终端一：`make dev`；终端二：`make web-dev`。
3. 浏览器打开 Vite 地址，顶部显示 `SIMULATION` 横幅。
4. 通过 `/api/v1/test/sim/devices/sim-cam-1/attach` 或 UI 按钮模拟插入。
5. `POST /backups/plan` 查看计划，`POST /backups/start` 执行，观察 `./dev/nas`。
6. 需要故障场景时 `POST /test/faults`。
7. `make test test-integration test-recovery` 全绿后 `make build-arm64 package`。

## 12. 测试模式

### 12.1 dry-run

目的：在实机上零风险验证设备识别、挂载、扫描、增量判断和 NAS 连通性。

行为：

- 设备发现、只读挂载、扫描、`StorageProbe`、容量检查全部真实执行。
- Sync Engine 产出 `Plan` 后结束，发布 `plan.generated` 事件，Web UI 显示将上传、跳过、冲突的文件清单和总字节数。
- `runmode.Guard.AllowDestinationWrite()` 返回 false，`storage.Target` 的 `CreatePart / Commit / RemovePart` 先检查该门禁，违反时返回 `ErrModeReadOnly`（不 panic，避免 systemd 反复重启），并有单元测试断言目标目录零写入。
- 不写 `source_files`、任务与断点，只追加 `events`。
- `/backups/start` 等写操作接口返回 `409 MODE_READ_ONLY`。

实机上线顺序：先用 `config.dryrun.yaml` 跑一次，核对计划与预期一致，再切换 `mode: production`。

### 12.2 simulation

目的：在 mac 上不依赖 OEC-Turbo 跑通包括断点、恢复和校验在内的完整流程。

模拟设备：

- 配置中每个 `fixtures.devices[]` 对应一台虚拟相机，每个卷对应一个目录，目录内结构与真相机一致：`DCIM/DJI_003/DJI_YYYYMMDDhhmmss_NNNN_D.MP4`，同名 `.LRF`，卷根另有 `MISC/`、`LOST+FOUND/`。
- attach / detach 通过 API 或 UI 切换。detach 时正在传输的任务会收到源读取错误，用于验证 `INTERRUPTED` 流程。
- `device_key` 由 `serial` 生成，与 linux 后端算法一致，保证增量逻辑同样生效。

fixtures 生成器 `scripts/gen-fixtures.sh`：

```text
--out tests/fixtures/cam1
--internal-files 3 --sd-files 5     # 文件数量
--size 64M                           # 单文件大小，默认小文件
--profile big                        # 预设：含 5 GB 与 16 GB 各一个文件，对应实测最大单文件
--seed 42                            # 可复现
```

文件内容来自 `/dev/urandom`，不可压缩，接近真实视频。生成时同步产出 `manifest.sha256` 和 `manifest.json`（路径、大小、mtime）。`tests/manifests/` 保存小型固定清单纳入版本管理，`tests/fixtures/` 为生成产物不入库。

模拟目标：`storage.type: local` 时目标是本地目录，`.part`、重命名、fsync 的语义与真实文件系统一致，足以验证流程；吞吐数据无意义。

### 12.3 测试分层

| 层级 | 位置 | 运行方式 | 覆盖 |
| --- | --- | --- | --- |
| 单元 | 各包 `*_test.go` | `make test`，mac | 指纹、分类、比较器、状态机转移、配置校验、路径白名单 |
| 集成 | `tests/integration` | `make test-integration`，simulation + local | 首次备份、重复接入跳过、文件变化产生新版本、SHA-256 与 manifest 一致 |
| 恢复 | `tests/recovery` | `make test-recovery`；多数场景进程内启动 `app`，崩溃场景用子进程 | 11 个场景见 12.5；全部在 race detector 下运行 |
| 实机 dry-run | OEC-Turbo | 手工 | 真设备识别、双卷、NAS probe |
| 实机真备份 | OEC-Turbo | 手工 + `scripts/test-performance.sh` | 81.1 GB 耗时、CPU、内存、断网拔线 |

恢复测试框架（`tests/recovery/harness_test.go`）：每个用例生成随机内容的源文件与独立临时数据库、目标目录，启动 `app`，通过 HTTP 注入规则、触发备份、轮询任务状态，最后比对目标文件 SHA-256 并确认无残留 `.part`。崩溃用例先 `go build -tags faultinject` 出二进制，以子进程启动，等待退出码 137，再用关闭故障的配置重启并验证续传。

### 12.4 故障注入

编译：`internal/faultinject` 通过 build tag 分为两份实现，`-tags faultinject` 时为真实注入器，否则为 `noop`，调用点内联为空函数。production 的 ARM64 二进制永远不带该 tag。

注入点：

| Point | 位置 | 可用动作 |
| --- | --- | --- |
| `source.open` | 打开源文件 | error、delay |
| `source.read` | 每次分块读取 | error（EIO、ENODEV）、delay、throttle、stall |
| `device.detach` | 设备管理器 | disconnect（模拟 USB 拔出事件） |
| `dest.probe` | `StorageProbe` | error（模拟 NAS 掉线） |
| `dest.open` | 创建 `.part` | error（EACCES、ENOSPC） |
| `dest.write` | 每次分块写入 | error（EIO、ENOSPC）、delay、throttle、stall、short_write |
| `dest.sync` | fsync | error |
| `dest.rename` | 临时文件归档 | error |
| `part.corrupt` | 断点落盘后 | corrupt（在 `.part` 指定偏移翻转字节，验证恢复时不信任 `.part` 大小） |
| `verify.hash` | 校验阶段 | mismatch（强制哈希不一致） |
| `db.commit` | SQLite 提交 | error（SQLITE_BUSY） |
| `process.crash` | checkpoint 持久化后 | crash（`os.Exit(137)`，用于恢复测试） |

规则结构：

```yaml
faults:
  enabled: true
  rules:
    - id: "nas-drop-midfile"
      point: "dest.write"
      trigger:
        after_bytes: 1073741824   # 累计写入 1 GiB 后触发
        times: 1                  # 触发次数，0 表示无限
      action:
        kind: "error"
        errno: "EIO"
      match:
        path_glob: "DCIM/**/*.MP4"
    - id: "slow-nas"
      point: "dest.write"
      trigger: { probability: 1.0 }
      action: { kind: "throttle", bytes_per_sec: 10485760 }
    - id: "crash-after-first-checkpoint"
      point: "process.crash"
      trigger: { nth_call: 1 }
      action: { kind: "crash" }
```

`trigger` 支持 `once`、`nth_call`、`after_bytes`、`probability`、`times`，以及 `after: <rule-id>`（指定规则命中过之后才生效，用于编排"写失败之后 NAS 才掉线"；探测点会被计划、建 part、重连检查共同消耗，没有 `after` 很难表达序列）。`action.kind` 支持 `error`（`errno`：EIO、ENOSPC、ENODEV、EACCES、ENOTCONN、ETIMEDOUT、EBUSY、EPERM）、`delay`、`stall`、`throttle`、`short_write`、`corrupt`（`offset` 为 -1 表示文件尾）、`disconnect`（回调模拟拔出全部设备并返回 ENODEV）、`mismatch`、`crash`（`os.Exit(137)`）。规则命中时发布 `fault.hit` 事件并写入 `events` 表。

各注入点在代码中的位置：`source.open` 打开源；`device.detach` 与 `source.read` 每次分块读取前；`dest.probe` 每次目标探测；`dest.open` 建 `.part` 前；`dest.write` 每次分块写入前；`dest.sync` 每次 fsync 前；`db.commit` 断点落库前；`part.corrupt` 与 `process.crash` 断点落库后（`part.corrupt` 的 `meta.Path` 为 `.part` 绝对路径）；`verify.hash` 校验前；`dest.rename` 归档重命名前。

运行时接口 `/api/v1/test/faults` 允许在不重启的情况下增删规则；production 构建不注册该路由。

### 12.5 必测场景与模式对应

| 场景 | 验证方式 | 状态 |
| --- | --- | --- |
| 首次插入相机，自动发现并备份 | 集成测试 `tests/integration`；mac 真相机 dry-run | 已验证（simulation）；实机待 V0.6 |
| 再次插入相机，跳过已确认完成的文件 | 集成测试：第二次 start 全部 skip | 已验证 |
| USB 设备路径或枚举顺序变化，仍能识别存储卷 | 单元测试（LUN / 容量 / 卷名分类）；darwin 固件回放 | 已验证（mac） |
| 上传中断网、拔出相机、NAS 服务重启或设备重启 | 恢复测试：NAS 掉线、设备拔出、进程崩溃重启 | 已验证 |
| NAS 容量不足和 SMB 凭据错误 | 恢复测试 `dest.write` ENOSPC → FAILED；凭据错误待实机 | 部分 |
| 临时文件数据损坏或无法验证断点 | 恢复测试 `part.corrupt`；Worker 单元测试不可信断点 | 已验证 |
| 同名文件被修改或存储卡更换 | Worker 单元测试源变化 → FAILED；比较器单元测试指纹变化 | 已验证 |
| NAS 已经断开但挂载目录仍存在，必须拒绝错误写入 | `fsinfo` 单元测试；mac smbfs probe 待 NAS 可达 | 部分 |
| Web 未授权请求、路径遍历及恶意配置值 | layout / storage 越界测试、配置校验测试；认证 V0.5 | 部分 |
| 大文件连续传输、长期运行时内存和 CPU 负载 | 5 GB simulation 实测；实机待 V0.6 | 部分 |
| 81.1 GB 全量传输的端到端真实耗时 | 仅实机 | 待 V0.6 |
| dry-run 模式下目标目录零写入 | storage 单元测试；mac 真相机 dry-run | 已验证 |
| 测试构建不能被安装为正式服务 | install.sh 检查 `--version` 输出 `faultinject=true` | 已实现，待实机 |
| 暂停 / 恢复 / 取消 / 重试上限 / 冲突策略 | 恢复测试 | 已验证 |

## 13. systemd 原生部署

权限分离（V0.6 实现并实机验证）：

- 主服务 `action-backup.service` 以 `actionbackup` 运行，`NoNewPrivileges=true`、`ProtectSystem=strict`，无任何挂载能力。
- 特权挂载由 `action-backup-mounter.service`（同一二进制的 `mount-helper` 子命令，root）执行：unix socket `/run/action-backup-mounter/helper.sock`（root:actionbackup 0660），只按 `/etc/action-backup/config.yaml` 推导路径，客户端只能指定 `/dev/sd[a-z]+`，规则见 §11.3。
- 源卷一律只读挂载到 `/run/action-backup-mounter/source/<sdX>`；NAS 挂到 `storage.mount_point`，只用于目的文件写入。
- 检测器不依赖 udev 规则触发；`99-action-backup.rules` 只负责让 udisks 忽略相机卷（`UDISKS_IGNORE=1`）。

两个单元（`deployments/`）：

```ini
# action-backup-mounter.service
[Service]
Type=simple
User=root
ExecStart=/opt/action-backup/action-backup mount-helper --config /etc/action-backup/config.yaml --socket /run/action-backup-mounter/helper.sock --owner actionbackup
Restart=on-failure
RestartSec=3
# 不能用 RuntimeDirectory=：目录下有只读 exFAT 挂载时，systemd 启动前的递归 chown 报 EROFS（status=233/RUNTIME_DIRECTORY）进入崩溃循环。
# 不能加 PrivateTmp / ProtectHome / ProtectSystem：会得到私有挂载命名空间，挂载对宿主与主服务不可见。
LimitNOFILE=1024

# action-backup.service
[Unit]
After=network-online.target action-backup-mounter.service
Requires=action-backup-mounter.service
[Service]
User=actionbackup
ExecStart=/opt/action-backup/action-backup --config /etc/action-backup/config.yaml
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
ReadWritePaths=/etc/action-backup            # PATCH /config 写回（目录 root:actionbackup 0770）
ReadWritePaths=/var/lib/action-backup
ReadWritePaths=/run/action-backup-mounter    # socket；源卷挂载由守护在宿主命名空间完成，以 slave 传播进本沙箱
ReadWritePaths=/mnt/action-backup-nas
```

守护更新后必须重启（`Requires=` 只管启动顺序）；`deploy.sh RESTART=1` 两者一起重启。**切勿仅通过挂载目录存在判断 NAS 已连接**：`StorageProbe` 用 `statfs` 的 `CIFS_SUPER_MAGIC` 与 mountinfo 来源比对，掉线后 `/mnt/action-backup-nas` 变回 ext4 时探测失败、写入门禁拒绝。

### 13.1 部署方式

设备可 SSH（`cmd_ip.sh`），一条命令完成：

```bash
make deploy               # 打包 → scp → 远端 install.sh（校验 SHA256、拒绝测试构建、建用户与目录、装两个单元与 udev 规则，不自动启动）
make deploy RESTART=1     # 已 enable 时安装后重启守护与主服务
```

首次安装后：`action-backup passwd` 设口令并把 `server.auth.enabled` 置 true、填 `storage.share` 与 `/etc/action-backup/smb.credentials`（0600）、`--check`、`systemctl enable --now action-backup`。实机初始化记录见 `docs/evidence/20261027T0320Z-device-deploy-smb/`。

## 14. 开发阶段与验收

| 阶段 | 开发内容 | 验收标准 | 状态 |
| --- | --- | --- | --- |
| V0.1 | Go 工程、配置与模式校验、SQLite 迁移、日志、HTTP、运行模式框架、平台抽象接口、sim 后端、fixtures 脚本、Makefile、systemd 模板、打包脚本 | mac 上 `make dev` 以 simulation 启动；`make build-arm64 package` 产出安装包 | 完成 2026-10-23 |
| V0.2 | 扫描器、卷分类、Sync Engine、Plan、dry-run 门禁、darwin 后端 | mac 接真相机能列出双卷全部 MP4 并输出计划 | 完成 2026-10-23，证据 `docs/evidence/20261023T0249Z-mac-dryrun-real-camera/` |
| V0.3 | 流式传输、`.part`、基础校验、断点、`local` 与 `premounted` 目标、`destination_layout` | simulation 下 5 GB 文件完成归档；mac 连真 NAS 完成一次真实上传 | 完成 2026-10-23（5 GB 含中断续传，证据 `docs/evidence/20261023T0305Z-sim-5gb-sha256-resume/`）；真 NAS 上传因 NAS 不可达待补 |
| V0.4 | 状态机、恢复、重试、故障注入、恢复测试框架 | 12.4 全部注入点有对应用例，`make test-recovery` 全绿 | 完成 2026-10-23，证据 `docs/evidence/20261023T0330Z-recovery-suite/` |
| V0.5 | Web UI（Vue 3）、用户名密码登录、会话与 CSRF、SSE、SHA-256 可选校验的 UI 开关、模式横幅、测试控制面板 | 未登录请求全部 401；浏览器可以管理任务、查看计划、添加故障规则 | 完成 2026-10-23，证据 `docs/evidence/20261023T0400Z-v05-auth-sse-smoke/`（全路由 401、CSRF、SSE、限速 curl 实测、全部测试输出，以及 Chrome 逐页核对记录 `ui-browser-check.md` 与截图） |
| V0.6 | linux 后端（sysfs + udev 检测、挂载守护、CIFS）、实机 dry-run、实机真备份、`make deploy` | 实机检测 / 只读挂载 / 拔插闭环；production 真实备份 SHA-256 一致 | 完成 2026-10-27，证据 `docs/evidence/20261027T0530Z-v06-linux-backend/`（本地目标）与 `20261027T0620Z-v06-nas-cifs-backup/`（真 NAS，SMB 3.1.1） |
| V0.7 | 性能：读写流水线、检查点默认 30 s / 512 MB、`test-performance.sh`；归档后保留源 mtime | 单文件吞吐 ≥ 45 MB/s（81.1 GB / 30 分钟） | 完成 2026-10-27：66–70 MB/s，证据 `docs/evidence/20261027T0700Z-v07-performance/`；81.1 GB 端到端待相机放回全量素材 |

## 15. 开发规范（可写入 AGENTS.md）

1. Go 实现模块化单体，主应用采用普通用户权限运行。
2. 所有源卷必须只读挂载或只读打开，不得删除或修改相机文件。任何模式都不例外。
3. 默认仅备份 `DCIM/**/*.MP4`，必须保留相对目录，扩展名匹配不区分大小写。
4. 数据流应直接从源文件进入目标，不将视频完整缓存到系统盘。
5. 禁止 SMB1，优先 SMB3。
6. 传输使用 `.part` 临时文件，完成校验后正式归档。
7. 所有任务状态转移受状态机约束并持久化。
8. 数据库保存设备、卷、源文件版本、任务、断点、事件。
9. 断点必须可验证；不可信时重新上传。
10. 禁止用字符串拼接构建任意 shell 命令。调用 `diskutil`、`ioreg`、`mount.cifs` 一律用 `exec.Command` 传参数数组；挂载守护的请求参数一律白名单校验，路径只从配置推导。
11. 设备及存储卷身份不能只依赖 `/dev/sdX` 或 `/dev/diskN`。
12. API 输入、文件路径和特权操作必须做白名单校验。
13. 密码、Token、SMB 凭据不得输出到日志或未经授权的 API。
14. 提供单元、集成、异常恢复及性能测试。
15. 首版使用一个 Worker，基于实测决定后续并发和缓冲区参数。
16. 操作系统相关代码只能出现在 `internal/platform`，其余包不得 import `syscall` 平台常量或调用外部命令。
17. 所有目标写入必须经过 `runmode.Guard` 检查；新增写入路径必须同时新增故障注入点和对应测试。
18. 故障注入代码只在 `-tags faultinject` 下编译，production 构建中 `/test/*` 路由不存在。
19. 配置校验必须拒绝危险组合：production / dry-run + local 目标（除非显式 `storage.allow_local_destination: true`）、simulation + 非 `-TEST` 的真 NAS 目录、测试构建安装为正式服务。
20. 构建与测试必须在 mac 上不依赖设备即可完成，`make test test-integration test-recovery` 是合并前的最低要求。
21. 遵循 Go 标准项目布局与惯例：`cmd/`、`internal/`，`gofmt`、`go vet`、`golangci-lint` 零告警；错误用 `fmt.Errorf("...: %w", err)` 包装；上下文取消贯穿所有阻塞调用。
22. 口令只以 bcrypt 存储；会话 ID 用 `crypto/rand` 生成；禁止在 URL 中传递凭据。
23. 目标路径必须经 `storage/layout` 渲染并通过越界校验，禁止直接用源相对路径拼接。

## 16. 下一步开发任务

**V0.7**（性能与收尾）：

1. ~~实机复测读写流水线~~ 已完成：69.6 MB/s（4 MB 缓冲）、66.2 MB/s（16 MB），缓冲大小无影响，瓶颈为裸 CIFS 写 72 MB/s。
2. 相机放回全量素材后跑一次 81.1 GB 端到端（`scripts/test-performance.sh` 按文件逐个记录），记录总耗时、CPU、RSS、负载。
3. 守护与主服务的资源占用、长时间运行稳定性（24 小时）记录证据。
4. HTTPS：若后续经反向代理暴露到局域网外，给会话 Cookie 加 `Secure`。
5. 设备 apt 源 `deb.debian.org` 不可达，是否永久切 USTC 镜像由用户决定。
6. ~~推送通知（网页可配、MeoW、Telegram）~~ 已完成（§10.4）；实机启用待用户提供 MeoW 昵称与 Telegram 代理。

**V0.9 之后**（软件更新，§19）：

1. ~~第一阶段：检查 + 页面提示~~ 已完成（2026-11-19，mac 验证）；设备上线后核对到 GitHub 的可达性（§18 问题 11）。
2. ~~第二阶段：下载、ed25519 验签、root 定时任务安装、重启两个服务、健康检查与回滚~~ 已实现（2026-11-19，§19.6）；**实机验证未做**，设备上线后按核对清单问题 13 执行，通过前不要依赖网页一键安装。
3. 第三阶段：空闲时自动安装、代理 / 镜像源（§19.7）；是否需要镜像取决于问题 11 的实测结果。

## 17. 需在实机验证的事项

- ~~固件架构、Linux 内核版本及 exFAT 可用性~~ 已确认：Armbian 25.05 bookworm、6.1.99 aarch64、内核 `exfat.ko`（挂载时自动加载）。
- ~~相机在 Linux 上的双存储卷只读挂载；LUN 0 / LUN 1；sysfs `product` 的 `SN:`~~ 已确认（2026-10-27）：LUN 0 `sda` 内置 48 GB `OsmoAction`，LUN 1 microSD 116 GB，`product=OsmoAction5pro_SN:0A1B2C3D`；只读挂载正常。
- ~~是否需要在相机上确认 USB 传输模式~~ 实测相机接入即为 usb-storage 双 LUN，无需额外操作。
- ~~CIFS 3.1.1 兼容性、可靠写入~~ 已确认：`//172.20.100.254/home` 协商 3.1.1，备份文件 SHA-256 一致。持续吞吐：裸 `dd` CIFS 写 71.7 MB/s、USB 读 102–116 MB/s、USB→CIFS 直拷 44.9 MB/s；应用串行读写 + 64 MB 检查点 24.1 MB/s，检查点 512 MB 为 65.9 MB/s，读写流水线 66–70 MB/s（`docs/evidence/20261027T0620Z-v06-nas-cifs-backup/`、`20261027T0700Z-v07-performance/`）。
- 设备上 SQLite 及 Go 程序的资源占用：空闲时主服务 RSS 约 2.5 MB、守护 2 MB；传输中主服务 RSS 6.2 MB，4 核负载约 0.6。
- NAS 端是否可以提供可信的服务端 SHA-256 计算接口。
- 81.1 GB 全量端到端备份是否满足 30 分钟目标：按 66–70 MB/s 折算约 19–20 分钟，待相机放回全量素材实测。
- ~~systemd 版本是否支持模板中的沙箱选项~~ systemd 252 支持；但 `ProtectSystem=strict` 需显式 `ReadWritePaths=/etc/action-backup` 才能写回配置，守护单元不能用 `RuntimeDirectory` 与命名空间类沙箱（§13）。
- ~~exFAT 时间戳时区~~ 已确认一致：Linux 读出 `2026-10-25 12:30:28.26 +0800`（10 ms 精度），归一化到秒后与 mac 相同。

## 18. 待确认问题

完整记录见 `docs/03-decisions.md`。

| # | 问题 | 状态 | 结论 |
| --- | --- | --- | --- |
| 1 | 相机双卷在 mac 上的卷名、LUN 对应、文件命名 | 已确认 | 见 §1.4：LUN 0 `OsmoAction` 内置，LUN 1 `Untitled` microSD；序列号取自产品名字符串；V0.2 darwin 后端已按此实现并实测 |
| 2 | NAS 型号与 SMB 版本 | 已确认 | `//172.20.100.254/home`，设备 `mount.cifs` 协商到 SMB 3.1.1（2026-10-27）；`smb_version: auto` 保持 |
| 3 | 工具链版本 | 已确认 | Go 1.25.5、Node 25.5.0、pnpm 10.33.0、golangci-lint；按 Go 惯例 |
| 4 | 固件状态与 SSH | 已确认 | 172.20.31.6 可 SSH（`cmd_ip.sh`），Armbian 25.05 / 6.1.99；`make deploy` 直接部署 |
| 5 | mac 只读重挂载 | 已确认 | 不需要，只依赖程序不写源卷 |
| 6 | 目标目录布局 | 已确认 | 默认 `{volume_type}/{relative_path}`，通过 `destination_layout` 模板自定义 |
| 7 | Web 认证 | 已确认 | 用户名密码 |
| 8 | 目标文件是否保留源 mtime | 已决定 | 保留：归档后 `Target.SetModTime`（V0.7 实现），失败只告警 |
| 9 | 检查点频率与吞吐 | 已确认 | 每个检查点 fsync 是瓶颈：5 s / 64 MB → 24.1 MB/s，512 MB → 65.9 MB/s；默认改 30 s / 512 MB，最多丢 512 MB 进度（约 8 秒） |
| 11 | 设备到 `api.github.com` 与 Release 资产下载域名的可达性与速度（编号与 `02-verification-checklist.md` 一致；该清单的问题 10 是 V0.8 的 USB 协议版本实机核对） | 待确认 | 设备 2026-11-19 不在线。已知 `deb.debian.org`、`api.telegram.org` 在该网络不可达；结果决定第三阶段是否要做代理 / 镜像源（§19.7） |
| 12 | 发布签名密钥 | 已完成（公钥） | 用户 2026-11-19 生成 ed25519 密钥对，公钥已嵌进 `internal/update/release-signing.pub.pem`；私钥需由用户存入 GitHub Secrets `RELEASE_SIGNING_KEY`（未存则发布流水线在签名步骤失败）（§19.6.3） |
| 13 | 第二阶段实机验证 | 待确认 | 设备不在线。真实 systemd 下的 timer 触发、`install.sh`、重启、健康检查、回滚都没有跑过，步骤见 `02-verification-checklist.md` 问题 13 |

---

**当前基线结论：** 使用 Go + SQLite + Linux CIFS + systemd 作为 OEC-Turbo 自动备份软件技术方案。开发在 macOS 上进行，通过平台抽象层和 simulation 模式先把全流程、断点恢复和故障场景在本地跑通，再用 dry-run 在实机零风险预演，最后切换 production 并进行性能验收。

## 19. 软件更新

### 19.1 需求与范围

用户 2026-11-19 提出三点要求：

1. 设备上的 action-backup 能从 GitHub Releases 更新到新版本。
2. 网页上要有对应的提示。
3. 更新后自动重启相关服务。

最初要求基于 `creativeprojects/go-selfupdate`，调研后改为自己实现（§19.2），并确认分三个阶段做、先做第一阶段（§19.4）。本章既是第一阶段的实现说明，也是第二、三阶段的设计依据；后两个阶段尚未实现，实现时如有偏差按惯例另出架构版本回写。

约束来自现有部署（§13）：

- 主服务以 `actionbackup` 运行，`ProtectSystem=strict`、`NoNewPrivileges=true`，写不了 `/opt/action-backup/`，也不能调 `systemctl`。
- 主服务与挂载守护是同一个二进制，更新后两个单元都要重启。
- 安装包里除二进制外还有两个 systemd 单元、udev 规则与配置样例，版本之间会变。
- 设备所在网络对外访问不稳定：`deb.debian.org`、`api.telegram.org` 已实测不可达。

### 19.2 为什么不用 go-selfupdate

2026-11-19 把 `github.com/creativeprojects/go-selfupdate` v1.7.1 的源码拉到本地逐项核对，结论如下。

能对上的部分：

| 项目 | 现状 | 库的行为 |
| --- | --- | --- |
| 资产命名 | `action-backup-<ver>-linux-arm64.tar.gz` | 按后缀 `linux-arm64.tar.gz` 匹配（`detect.go` `getSuffixes`） |
| 包内二进制 | 子目录下的 `action-backup` | 取 basename 与命令名比对（`decompress.go` `unarchiveTar`） |
| 校验 | Release 附带 `SHA256SUMS` | `ChecksumValidator{UniqueFilename: "SHA256SUMS"}` |
| 预发布 | workflow 已把带 `-` 的版本标为 prerelease | `Config.Prerelease` 开关 |

不合适的部分：

| 问题 | 说明 |
| --- | --- |
| 下载、校验、替换绑在一个调用里 | `UpdateTo` 一次完成三步，只能整体放在 root 侧，等于 root 进程要联网并链接一大堆依赖 |
| 只替换一个二进制 | 单元文件、udev 规则不会更新，需要另想办法（例如 `go:embed` 后由新二进制自己同步） |
| 下载不可控 | `io.ReadAll` 一次读完，没有进度、断点续传、重试；设备网络不稳时是硬伤 |
| 依赖重 | 带进 go-github、gitea、gitlab 的 SDK 与 ProtonMail crypto |
| Go 版本 | v1.7.x 要求 Go 1.26，v1.6.0 要求 1.25.12；本项目 `go.mod` 固定 1.25.0 |
| 不负责重启 | 库的职责到替换二进制为止 |

库真正省下的是「同目录临时文件 + rename + 保留旧文件 + 失败回滚」这一段原子替换逻辑。而本项目第二阶段复用包内 `install.sh` 安装整包（§19.6.4），并不需要单文件原子替换。综合下来自己实现的代码量约几百行标准库代码，限制更少。

自己实现需要自己负责的部分：语义化版本比较（引 `golang.org/x/mod/semver`，不手写）、GitHub API 的限流与错误处理、签名校验、以及全部测试用例。

### 19.3 总体结构

更新拆成两个角色，中间用签名衔接：

| 角色 | 身份 | 负责 | 不做 |
| --- | --- | --- | --- |
| 检查与下载 | 主服务 `actionbackup`，现有沙箱不变 | 查新版本、下载整包、初步校验、对外提供 API 与页面状态 | 不碰 `/opt`，不调 `systemctl` |
| 安装与重启 | root oneshot 单元，由 systemd timer 拉起 | 验签、安装、重启两个服务、健康检查、回滚 | 不联网，不接受外部传入的 URL 或版本号 |

挂载守护 `action-backup-mounter` 不参与更新：它保持最小、不联网。曾考虑给它加一个「执行更新」操作，否决原因是 root 守护要联网、还得重启自己。也否决了把 `/opt/action-backup` 放开给 `actionbackup` 写的做法：那等于无特权服务能替换 root 执行的二进制。

**信任链**：安装包由无特权服务下载，root 不能直接信它。如果只靠同目录的 `SHA256SUMS`，主服务一旦被攻破就能伪造包和校验文件，成为提权到 root 的通道。因此发布时对 `SHA256SUMS` 做 ed25519 签名，公钥嵌在二进制里，root 侧只信签名（§19.6.3）。这也让以后走镜像或 NAS 源变得安全：来源不可信不要紧，签名对得上才装。

### 19.4 阶段划分

| 阶段 | 内容 | 状态 |
| --- | --- | --- |
| 一 | 检查新版本 + 页面提示 + 事件 / 推送 | 已实现（V0.9，2026-11-19） |
| 二 | 下载、验签、root 安装、重启、健康检查、回滚 | 已实现（2026-11-19），mac 上验证；root 侧安装未实机验证 |
| 三 | 空闲时自动安装；代理 / 镜像源 | 已设计，未实现；镜像是否要做取决于设备网络实测 |

用户已确认的决定（2026-11-19）：

- 不用 go-selfupdate，自己实现。
- 第二阶段起发布包做 ed25519 签名。
- 默认只提示，自动安装是开关且默认关闭。
- 安装由系统定时任务（systemd timer）触发，执行包内 `install.sh`。
- 仓库是公开的，检查与下载都不需要 token。
- 先做第一阶段；第一阶段完成后同日继续做第二阶段，并生成了签名密钥。

### 19.5 第一阶段：检查与提示（已实现）

整个阶段只发 HTTPS GET、不写任何文件，所以直接放在无特权的主服务里，没有新增单元，也不涉及权限。

**检查逻辑**（`internal/update/update.go`）：

- 请求 `GET {api_base_url}/repos/{owner}/{repo}/releases?per_page=30`，头 `Accept: application/vnd.github+json`、`X-GitHub-Api-Version: 2022-11-28`、`User-Agent: action-backup/<版本>`。客户端 20 秒超时，响应体上限 4 MB。
- 在返回的列表里按语义化版本取最高的一个：draft 一律跳过；`stable` 通道跳过预发布（Release 标了 prerelease，或版本号自身带预发布段，两者任一即算）；tag 不是合法语义化版本的跳过。
- 在该 Release 的资产里找本机平台的安装包 `action-backup-<ver>-<GOOS>-<GOARCH>.tar.gz`，找到则输出文件名、大小、下载地址；没找到只是不给下载链接，不影响「有新版本」的判定。
- 发布说明输出上限 16 KB，超出截断并标记 `notes_truncated`。
- 错误翻译：404 → 仓库或发布不存在；403 / 429 且 `X-RateLimit-Remaining: 0` → 限流并给出恢复时间；其它非 200 原样报状态码。匿名访问限额是每 IP 每小时 60 次，12 小时一次的检查远低于此。

**版本判定**（`internal/update/version.go`）：

- 比较用 `golang.org/x/mod/semver`，`0.9.0-rc.1 < 0.9.0`、`rc.10 > rc.2` 这类规则由库保证。
- **开发构建不参与判定**。`git describe` 产出的 `v0.8.0-3-g76121de`、`v0.8.0-dirty` 在语义化版本里是 0.8.0 的预发布，直接比较会把同一份代码提示成「有新版本 0.8.0」。判定规则：版本串无法解析、带 `-dirty` / `-dev`、或匹配「`<提交数>-g<哈希>`」后缀，即为开发构建。开发构建不做定期检查；手动检查仍能取回最新版本信息，但 `update_available` 恒为 false，也不发事件。`make dev` 与全部测试的默认版本串 `0.1.0-dev` 属于此类，所以测试不会访问 GitHub。

**定期循环**（`Checker.Run`）：

- 启动 30 秒后查第一次（等网络就绪），之后每 `check_interval_hours` 一次。
- 检查失败后的下一次间隔取「30 分钟」与检查间隔的较小值。
- 运行中改配置会唤醒循环重新排期；切换通道或 API 地址会丢弃旧结果。
- 定期检查与手动检查用同一把锁串行化。

**事件**：

| 类型 | 级别 | 时机 | data |
| --- | --- | --- | --- |
| `update.available` | info | 首次发现某个新版本；同一版本在本次进程生命周期内只发一次 | `current`、`version`、`tag`、`url`、`prerelease`、`published_at` |
| `update.check_failed` | warn | 检查从成功转为失败时发一次；连续失败不重复发 | `error` |

去重状态只在内存里，服务重启后再次检查到同一新版本会再发一次事件（以及一条推送）。服务很少重启，没有为此加持久化。

**推送通知**：`internal/notify` 把 `update.available` 翻译成「发现新版本 X，当前 Y」加发布页链接，受 `notifications.on.update_available` 控制（默认开，前提是通知总开关与渠道已启用）。

**接口**：`GET /api/v1/system/update` 返回下列字段，`POST /api/v1/system/update/check` 立即检查后返回同样的结构；检查失败时 HTTP 502、错误码 `UPDATE_CHECK_FAILED`，响应体的 `data` 里仍是当前状态。

| 字段 | 含义 |
| --- | --- |
| `current_version` | 当前版本，不带前导 v |
| `dev_build` | 当前是否开发构建 |
| `platform` | 本机平台，如 `linux-arm64` |
| `repository` | 发布仓库 |
| `check_enabled` / `check_interval_hours` / `channel` | 当前生效的配置 |
| `checking` | 是否正在检查 |
| `checked_at` | 最近一次成功检查的时间 |
| `next_check_at` | 下一次定期检查的计划时间；未启用或开发构建时没有 |
| `latest` | 当前通道的最新版本：`version`、`tag`、`name`、`url`、`notes`、`notes_truncated`、`prerelease`、`published_at`、`asset{name,size,url}` |
| `update_available` | 最新版本是否严格高于当前版本 |
| `last_error` / `last_error_at` | 最近一次失败的原因与时间；之后成功即清空。失败不清掉上一次成功的 `latest` |
| `install_supported` | 是否支持网页一键安装（§19.6.6） |
| `install` | 下载 / 安装请求的阶段与上次安装结果（§19.6.1） |
| `latest.signed` | 该版本是否带 `SHA256SUMS` 与 `SHA256SUMS.sig`；没有签名的版本只能手动安装 |

两条路由都需要登录，已加入 `internal/app/auth_test.go` 的 `protectedRoutes`。

**配置**见 §10.5。发布仓库是代码常量 `update.Repository`，`update.api_base_url` 只用于测试且不在 PATCH 白名单，网页无法把更新源指到别处。

**页面**：

- 顶栏：版本号旁出现「有新版本 vX.Y.Z」徽标，点击跳到「设置」页。
- 「设置 → 软件更新」卡片：结论行（有新版本 / 已是最新 / 开发构建 / 尚未检查）、当前与最新版本、发布时间、本平台安装包链接与大小、上次与下次检查时间、失败原因、可折叠的发布说明（按纯文本显示，不渲染 HTML）、「立即检查」「保存」「打开发布页」，以及定期检查开关、间隔、通道三个配置项。
- 有新版本且支持安装时显示「立即更新到 vX」按钮（§19.6）；不支持时说明原因并提示手动安装（下载包后执行 `install.sh`，或开发机 `make deploy RESTART=1`）。
- 前端收到 `update.*` 或 `config.changed` 事件即刷新更新状态；`web/src/eventText.js` 登记了新事件类型的中文文案。
- `web/src/sse.js` 按名字逐个监听事件类型（EventSource 的限制），新类型必须在其 `TYPES` 列表登记。第一阶段漏了这一步，导致徽标只有刷新页面才出现；第二阶段补上，同时补了此前同样漏掉的 `backup.finished`、`storage.*`、`notify.failed`。

**验证**：`internal/update`、`internal/config`、`internal/notify`、`internal/app` 的单元测试用 httptest 假 GitHub 覆盖版本选择、通道切换、开发构建、限流 / 404 / 坏 JSON / 无发布、事件去重、定期循环与白名单拒绝。2026-11-19 在 mac 上用标成 0.7.0 的构建对真实仓库实测：查到 v0.8.0 与对应安装包，启动 30 秒后定期检查执行、下一次排在 12 小时后，同一版本只发一条事件，页面徽标与卡片显示正常。证据 `docs/evidence/20261119T0411Z-v09-update-check/`。设备实机未验证（不在线）。

### 19.6 第二阶段：下载、验签、安装、重启（已实现，未实机验证）

2026-11-19 实现并在 mac 上用单元测试、假 GitHub 与测试密钥验证；设备当日不在线，**root 侧的安装流程没有在真实 systemd 环境里跑过**，首次启用前必须按核对清单问题 13 实机走一遍。

#### 19.6.1 流程

1. **发起**：用户在「设置 → 软件更新」点「立即更新到 vX」，前端调 `POST /system/update/apply`。
2. **下载**（主服务，`internal/update/install.go`）：把 `SHA256SUMS`、`SHA256SUMS.sig`、本平台安装包下载到更新目录（与数据库同级的 `update/`，设备上是 `/var/lib/action-backup/update/`）。
   - 先下两个小文件并用内嵌公钥验签，签名不对就不下载安装包。
   - 安装包写 `.part` 后 rename，支持 HTTP Range 续传；单个文件最多试 4 次；下载阶段总时限 30 分钟。
   - 上限：安装包 256 MB、清单 1 MB、签名 4 KB。
   - 下载完核对 SHA-256，不符即删除。上一次下载好且哈希对得上的包直接复用。
3. **等空闲**：调度器有任务正在传输时停在 `waiting_idle`，每 5 秒看一次，任务结束后继续。
4. **提交请求**：在更新目录写标记文件 `request`（临时文件 + rename）。内容只有版本号与时间，供排查，root 侧不读。
5. **安装**（root）：`action-backup-update.timer` 每分钟拉起 `action-backup-update.service`，执行 `action-backup self-update --apply`（`internal/update/installer.go`，§19.6.3 起）。
6. **回报**：root 把结果写进 `/opt/action-backup/update-result.json`；主服务启动时读取，同一结果只发一次 `update.installed` 或 `update.failed` 事件，之后一直显示在页面的「上次安装」一行。

页面在 `requested` 阶段显示全屏遮罩并每 1.5 秒轮询 `/system/status`，下面任一成立即刷新页面：请求失败过之后又通了；返回 401（会话在内存里，重启即失效）；`started_at` 变了。等待超过 6 分钟改为提示手动刷新。

安装阶段与 `GET /system/update` 的 `install` 段：

| 字段 | 含义 |
| --- | --- |
| `supported` | 本进程能否提交安装请求（与顶层 `install_supported` 相同） |
| `stage` | `idle` / `downloading` / `verifying` / `waiting_idle` / `requested` / `failed` |
| `version` | 正在处理的目标版本 |
| `downloaded_bytes` / `total_bytes` | 下载进度 |
| `error` | 主服务侧失败的原因 |
| `requested_at` | 写请求标记的时间 |
| `last_result` | root 侧上一次安装的结果：`ok`、`from`、`to`、`step`、`error`、`rolled_back`、`rollback_error`、`started_at`、`finished_at`、`log`（`install.sh` 输出的末尾 4 KB） |

`POST /system/update/apply` 成功返回 202 与当前状态；拒绝时的错误码：

| HTTP | 错误码 | 情形 |
| --- | --- | --- |
| 501 | `INSTALL_NOT_SUPPORTED` | 本进程不支持安装（见 §19.6.6） |
| 409 | `NO_UPDATE` | 没有检查过，或最新版本不高于当前版本，或当前是开发构建 |
| 409 | `NOT_INSTALLABLE` | 该版本没有本平台的包，或没有 `SHA256SUMS` / `SHA256SUMS.sig` |
| 409 | `INSTALL_IN_PROGRESS` | 已经在下载、等待或已提交请求 |

新增事件：`update.progress`（不落库，只走 SSE，至多每 500 ms 一条）、`update.requested`、`update.installed`、`update.failed`（error 级；`data.step` 指明失败在哪一步，`data.rolled_back` / `data.rollback_error` 说明回滚情况）。

#### 19.6.2 触发方式：systemd timer

`deployments/` 新增两个单元，随安装包分发，由 `install.sh` 安装并 `systemctl enable --now action-backup-update.timer`：

```ini
# action-backup-update.timer
[Timer]
OnBootSec=2min
OnUnitActiveSec=1min
AccuracySec=10s

# action-backup-update.service
[Unit]
ConditionPathExists=|/var/lib/action-backup/update/request
ConditionPathExists=|/opt/action-backup/prev/installing
[Service]
Type=oneshot
User=root
ExecStart=/opt/action-backup/action-backup self-update --apply --config /etc/action-backup/config.yaml
TimeoutStartSec=600
```

- 用 systemd timer 而不是 crontab：日志统一进 journal（`journalctl -u action-backup-update`），不依赖设备上是否装了 cron。
- 两个条件带 `|` 前缀，任一成立才运行：有新的安装请求，或上次安装被打断留下了 `installing` 标记需要回滚。都不成立时 systemd 直接跳过，不启动进程。
- 条件里的路径是按默认 `database.path` 写死的；改了数据库路径要同步改单元。
- 代价：点击「立即更新」后最多等一分钟才开始安装。
- 曾考虑 `.path` 单元即时触发，放弃原因是标记没被及时删掉时容易反复触发。
- 本单元不能与主服务、守护有 `Requires` / `PartOf` / `BindsTo` 关系：安装过程要重启它们，绑定会把自己一起带走。
- **定时任务只调用 `self-update --apply`，由它验签通过后再执行包内 `install.sh`；不直接执行下载目录里的任何东西。**

#### 19.6.3 签名与校验

- 算法 ed25519。公钥 `internal/update/release-signing.pub.pem`（PKIX PEM）用 `go:embed` 嵌进二进制；私钥只在 GitHub Secrets `RELEASE_SIGNING_KEY`。密钥由用户 2026-11-19 生成。
- 发布流水线在生成 `SHA256SUMS` 之后执行 `openssl pkeyutl -sign -rawin`，产出 64 字节原始签名 `SHA256SUMS.sig`，并立即用仓库里的公钥验一遍；Secret 缺失时发布失败，不会发出没有签名的版本。
- 2026-11-19 用临时密钥确认过：OpenSSL 3 签出的签名能被 Go `crypto/ed25519` 验过，篡改后验签失败。

root 侧的检查顺序（每一步失败都不会动系统文件）：

1. 把 `SHA256SUMS`、`SHA256SUMS.sig` 从更新目录复制进 root 专属的 `/opt/action-backup/update-work/`。复制用 `O_NOFOLLOW | O_NONBLOCK` 打开并 `fstat` 确认是普通文件、不超过上限（`internal/platform/linux/update_ops_linux.go`）。更新目录归无特权用户所有，是不可信输入。
2. 用内嵌公钥验签。
3. **安装包的文件名与版本号只取自验过签的清单**：在清单里找唯一一个 `action-backup-<ver>-<os>-<arch>.tar.gz`，不看请求标记，也不看目录里有什么文件。
4. 版本必须严格高于当前版本（防降级、防重放旧包）。
5. 把该文件复制进工作目录并核对 SHA-256。
6. 解包：只接受目录与普通文件，拒绝符号链接、硬链接、设备等类型，拒绝越出目标目录的路径；展开上限 512 MB、2000 个条目。
7. 运行包内二进制的 `--version`：版本要与清单一致，且不是 `faultinject=true` 的构建。
8. 包内 `SHA256SUMS`（覆盖包内各文件）由 `install.sh` 自己的 `sha256sum -c` 核对。

与最初设计的差别：结果文件写在 `/opt/action-backup/update-result.json` 而不是更新目录。更新目录归无特权用户所有，root 往里写文件可能被符号链接利用；`/opt/action-backup` 只有 root 可写，主服务可读。

#### 19.6.4 安装：复用 `install.sh`

验签通过后在解包目录里执行 `bash install.sh`，即新包自带的安装脚本：

- 手动安装（`make deploy`）与自动更新走同一份脚本，只有一处需要维护。
- 单元文件、udev 规则随包更新。
- 执行的是新版本的脚本，新版本的安装变化能直接生效。
- 因此 `install.sh` 必须可重复执行、不依赖交互。

第一个带更新功能的版本要用 `make deploy` 手动装一次（装上定时任务），之后才能自更新。

#### 19.6.5 重启、健康检查与回滚

安装前：

1. `systemctl stop action-backup.service`。
2. 把会被改动的文件备份到 `/opt/action-backup/prev/`：二进制、四个单元文件、udev 规则、数据库及其 `-wal` / `-shm`。备份时不存在的文件也记进清单（回滚时要删掉，例如新版本新增的单元）。
3. 备份全部完成后才写 `prev/installing` 标记：标记存在即表示备份完整、安装尚未确认成功。

安装后：

1. 再跑一次已安装二进制的 `--version`，确认装上的是目标版本。
2. `systemctl daemon-reload`，`systemctl restart action-backup-mounter.service action-backup.service`。
3. 90 秒内每 3 秒检查一次：两个单元都 active，且健康接口有应答、`database.ok` 为 true。**不看健康接口的 HTTP 状态码**：NAS 不在线时接口返回 503（degraded），那不是更新造成的，不能因此回滚。
4. 通过 → 删除 `installing` 标记，清掉下载目录里的包；`prev/` 留着，下次安装时覆盖。
5. 不通过 → 回滚。

回滚：停主服务 → 按清单恢复文件（已存在的原地覆盖以保留属主，运行中的二进制写不了时改用临时文件 + rename；备份时不存在的删掉）→ `daemon-reload` → 重启两个单元 → 再做一次健康检查。回滚的每一步出错都记进 `rollback_error` 但不中断后续步骤。回滚确认健康才删除 `installing` 标记；否则保留现场，下一次定时触发会再回滚一遍。

迁移是内嵌 SQL、只能向前，旧二进制不认识新表结构，所以回滚连数据库一起恢复。

#### 19.6.6 何时允许安装

`install_supported` 为 true 需要同时满足（`internal/app/app.go` `updateInstallOptions`）：

- 运行在 Linux；
- 不是 simulation 模式；
- 不是 `faultinject` 构建，也不是开发构建；
- 设备上存在 `/etc/systemd/system/action-backup-update.timer`（即已经用带更新功能的包装过一次）。

此外该版本必须有本平台的包和签名文件，页面才显示「立即更新」按钮。

有备份在传输时不打断：等它结束后再提交请求。没有做「强制立即安装」和「取消」。

#### 19.6.7 失败情形

| 情形 | 处理 | 结果里的 `step` |
| --- | --- | --- |
| 下载失败 | 续传、重试 4 次；仍失败停在 `failed`，可点「重试」 | `download`（主服务侧） |
| 签名或哈希不符（主服务侧） | 不提交请求，删除坏包 | `verify` |
| 更新目录里的文件是符号链接、过大或缺失 | 不安装 | `stage` |
| 签名不符、包被替换、版本不高于当前 | 不安装 | `verify` |
| 包里有链接或越界路径、缺 `install.sh` | 不安装 | `extract` |
| 包内二进制版本不符或是测试构建 | 不安装 | `inspect` |
| 停主服务失败 | 不安装，未动任何文件 | `stop` |
| 备份失败 | 重新启动主服务，不安装 | `backup` |
| `install.sh` 失败，或装上的不是目标版本 | 回滚 | `install` |
| 重启失败 | 回滚 | `restart` |
| 重启后不健康 | 回滚 | `health` |
| 回滚后仍不健康 | 保留 `installing` 标记，下次触发再回滚；`rollback_error` 非空，需人工处理 | 同上 |
| 安装过程中断电或进程被杀 | 下次触发发现 `installing` 标记，先回滚 | `recover` |

请求标记在开始处理时就删除：无论成败每个请求只处理一次，不会每分钟重试刷屏。

#### 19.6.8 代码落点

| 位置 | 内容 |
| --- | --- |
| `internal/update/sign.go` | 内嵌公钥、验签、清单解析、从清单取本平台的包 |
| `internal/update/download.go` | 续传下载 |
| `internal/update/install.go` | 主服务侧：`Apply`、阶段、进度事件、读取并报告安装结果 |
| `internal/update/installer.go` | root 侧：`Installer`（验签、解包、备份、安装、健康检查、回滚），系统操作经 `InstallOps` 接口 |
| `internal/platform/linux/update_ops_linux.go` | `InstallOps` 的真实实现：`O_NOFOLLOW` 复制、`systemctl`、执行 `install.sh`、运行 `--version` |
| `cmd/action-backup/selfupdate.go` | `self-update --apply` 子命令，装配设备上的固定路径 |
| `deployments/` | 两个新单元；`install.sh` 安装并启用定时任务；`scripts/package.sh` 把它们打进包 |
| `.github/workflows/release.yml` | 签名步骤，Release 多一个资产 `SHA256SUMS.sig` |
| `web/src/` | 「立即更新」按钮、下载进度、安装遮罩、三个新事件的中文文案；`sse.js` 登记新事件类型 |

#### 19.6.9 验证情况

已验证（mac，2026-11-19，证据 `docs/evidence/20261119T0530Z-v09-update-install/`）：

- 单元测试：验签与清单解析；续传（服务器中途断开后用 Range 接上）；等空闲；坏签名在下载安装包之前失败；包与清单不符时删除坏包；安装结果只报告一次。
- root 侧用假的系统操作在临时目录里跑：成功路径的调用顺序；11 种「不动系统就拒绝」的情形；5 种回滚情形后文件恢复原样、新增文件被删；回滚后仍不健康时保留标记并在下次触发时恢复。
- 用 `make package` 产出的真实安装包（mac 上打的，带 AppleDouble 文件）验证解包逻辑。
- 浏览器：假 GitHub + 测试密钥，「立即更新」→ 实时下载进度 → 遮罩 → 服务重启后页面自动刷新 → 「更新完成」事件与「上次安装」一行。
- Linux 目标交叉编译与 lint 通过。

没有验证：

- 真实 systemd 下 timer 触发、`ConditionPathExists`、以 root 执行 `install.sh`、重启两个单元、健康检查与回滚。
- `update_ops_linux.go`（`O_NOFOLLOW` 复制等）只做了交叉编译与 lint，没有在 Linux 上执行过。
- 用真实私钥签名的发布：需要先打一个 tag 让流水线跑一次。
- 主服务沙箱（`ProtectSystem=strict`）下读取 `/opt/action-backup/update-result.json` 与 `/etc/systemd/system/action-backup-update.timer`。

### 19.7 第三阶段：自动安装与镜像（设计，未实现）

- **空闲自动安装**：`update.auto_install`（默认 false）打开后，检查到新版本即自动调用与「立即更新」相同的流程。可加「只在某个时间窗内安装」的限制。
- **安装结果推送**：`update.installed` / `update.failed` 目前只是事件，没有接进推送通知；自动安装时用户不在页面前，届时要一并接上。
- **强制安装与取消**：等待空闲期间的「强制立即安装」「取消」没有做。
- **代理**：`update.proxy_url`，写法与 `notifications.telegram.proxy_url` 一致（http / socks5）。
- **镜像源**：可配置一个 HTTP 基地址（例如 NAS 上的目录），放版本清单与安装包。因为 root 侧只信签名，镜像本身不需要可信。
- 代理与镜像是否要做、做哪个，取决于设备到 GitHub 的实测结果（§18 问题 11）。

### 19.8 尚未解决的问题

- 设备到 GitHub 的可达性没有测过（设备不在线）。第一阶段在设备上是否可用取决于此；不通时的临时办法是在 `action-backup.service` 里用 `Environment=HTTPS_PROXY=...`。
- 签名密钥的保管与轮换流程没有定。公钥嵌在二进制里，换密钥需要先发一个同时信任新旧公钥的过渡版本。
- 回滚时恢复数据库，会丢掉「新版本启动后到回滚前」这段时间写入的任务记录。这段时间最长约 90 秒且刚重启，影响有限。
- root 侧安装没有实机验证（§19.6.9）。
- 更新单元里的 `ConditionPathExists` 路径按默认 `database.path` 写死，改了数据库路径需要手改单元。
- 更新目录本身（而不只是里面的文件）可以被无特权用户换成符号链接。root 侧对目录里的文件用 `O_NOFOLLOW`，但没有防目录级替换；后果限于 root 读到别处的同名文件（验签会失败）以及删除别处名为 `request` 的文件，没有进一步处理。
- `update.available` 的去重不持久化，重启后会重发一次。
