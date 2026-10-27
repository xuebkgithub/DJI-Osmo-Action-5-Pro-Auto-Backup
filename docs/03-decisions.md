# 决策记录

状态：`已确认` / `待确认` / `已推断待确认`。依据一律指向 `docs/evidence/` 中的原始输出或外部来源。

| # | 问题 | 状态 | 结论 | 依据 | 影响章节 |
| --- | --- | --- | --- | --- | --- |
| 1 | 相机双卷在 mac 上的卷名 | 已确认 | LUN 0 = `OsmoAction` 51.5 GB 内置；LUN 1 = `Untitled` 124.7 GB microSD。分类以 LUN 为主、容量为辅、卷名为旁证 | `docs/evidence/20261023T020156Z-mac-probe/` 410、420、24 | V1.2 §1.4、§11.3 |
| 1a | 双卷与 USB LUN 对应关系 | 已确认 | LUN 0 → disk4 → OsmoAction；LUN 1 → disk5 → Untitled。USB `iSerialNumber` 为占位值 `123456789ABCDEF`，真实序列号在产品名 `OsmoAction5pro_SN:0A1B2C3D` | 同上 24、25 | V1.2 §1.4、§6 device_key |
| 1b | 真实文件命名与 mtime 精度 | 已确认 | `DCIM/DJI_003/DJI_20261023080221_0019_D.MP4` + 同名 `.LRF`；单文件最大 15.84 GB；mtime 秒级（有奇数秒），fingerprint 截断到整秒 | 同上 424、428、429 | V1.2 §6.2、§12.2 |
| 1c | mac 元数据污染 | 已确认 | 两卷根目录有 `.fseventsd`，`MISC/._AC004.db`；Spotlight 已禁用。扫描器忽略规则已覆盖 | 同上 417、427、412、422 | V1.2 §1.4、§10 ignore_patterns |
| 2 | NAS 型号与 SMB 版本 | 待探测 | 用户不清楚。型号推断为 Synology DSM 7.1，IP 192.168.1.100。2026-10-23 探测时 ping 与 445 端口均超时，mac 与 NAS 不在同一可达网络或 NAS 未开机；需在 Finder 挂载后重跑。设计上 `smb_version: auto` 不依赖该答案 | `../DSM_api/README.md`；`docs/evidence/20261023T020156Z-mac-probe/` 50 至 54 | V1.2 §10 `smb_version`、§17 |
| 3 | 工具链版本 | 已确认 | Go 1.25.5 darwin/arm64、Node 25.5.0、pnpm 10.33.0；golangci-lint 2.15.0 已于 2026-10-23 通过 brew 安装。`go.mod` 写 `go 1.25`。整体遵循 Go 标准项目布局与惯例 | 2026-10-22 会话内 `go version` 等输出；用户 2026-10-23 确认 | V1.2 §11.1、§15 第 21 条 |
| 4 | OEC-Turbo 固件状态、SSH 时间 | 已确认 | 用户答复"没问题"：固件无问题，SSH 稍后可用。部署先用安装包，`make deploy` 待 SSH 可用后实现 | 用户 2026-10-23 答复 | V1.2 §13.1、§14 V0.6 |
| 5 | mac 上是否允许 diskutil 只读重挂载 | 已确认 | 不需要。只读保护完全依赖程序自身不写源卷，并用单元测试验证 | 用户 2026-10-23 答复 | V1.2 §11.3；删除 `remount_readonly` 与 `mac-mount-ro.sh` |
| 6 | NAS 目标目录分层 | 已确认 | 默认 `{volume_type}/{relative_path}`，用户要求可自定义，故引入 `storage.destination_layout` 模板，占位符含 serial、volume_type、mtime 等 | 用户 2026-10-23 答复 | V1.2 §10 占位符表、§6 数据库规则、§15 第 23 条 |
| 7 | Web 认证方式 | 已确认 | 用户名密码：bcrypt 口令、HttpOnly 会话 Cookie、CSRF Token、登录限速、`action-backup passwd` 子命令 | 用户 2026-10-23 答复 | V1.2 §9 认证与安全、§10 `server.auth`、§14 V0.5 |

## 已做出的其他决定

| 日期 | 决定 | 原因 |
| --- | --- | --- |
| 2026-10-22 | 测试模式 = mac simulation + 实机 dry-run，含故障注入 | 用户确认 |
| 2026-10-22 | mac 上同时支持 fixtures 目录和 `/Volumes` 真相机、真 NAS | 用户确认 |
| 2026-10-22 | OEC-Turbo 暂不能 SSH，部署先做安装包，`make deploy` 预留 | 用户确认 |
| 2026-10-22 | SQLite 驱动选定 `modernc.org/sqlite` | 纯 Go，mac 上 `CGO_ENABLED=0` 直接交叉编译 ARM64 |
| 2026-10-23 | `smb_version` 默认 `auto` | NAS SMB 版本未知，由 CIFS 协商 2.1 到 3.1.1，实机确认后可固定 |
| 2026-10-23 | 发布 V1.2 | 落实第 3 至 7 条；1、2 待探测 |
| 2026-10-23 | V1.2 补充 §1.4 实测结论 | 设备身份改为解析产品名中的 SN；卷分类以 LUN 为主；fingerprint mtime 截断到整秒；fixtures 命名与 16 GB 大文件 profile |
| 2026-10-23 | 探测脚本 USB 段改用 `ioreg -p IOUSB` | `system_profiler SPUSBDataType` 在本机返回空输出 |
| 2026-10-23 | V0.1 交付 | `action-backup/` 可编译、测试全绿、ARM64 安装包可产出；详见其 README |
| 2026-10-23 | 依赖固定 `modernc.org/sqlite v1.59.0`、`x/sys v0.47.0` | 更新版本要求 Go 1.26，而本机为 1.25.5 且 goproxy.cn 不可用无法自动下载工具链；用 proxy.golang.org |
| 2026-10-23 | Schema 在文档 §6.2 基础上增加 `devices.weak_identity`、`volumes.label/lun/size_bytes`、`events.data`、`schema_migrations.name` | 承载实测得到的 LUN、卷标与弱身份标记；已回写 V1.3文档 |
| 2026-10-23 | dry-run 写入门禁违规时返回 `ErrModeReadOnly` 错误而不是文档 §12.1 所写的 panic | 服务内 panic 会触发 systemd 反复重启；错误返回同样阻止写入且可被测试断言；已回写 V1.3文档 |
| 2026-10-23 | `config.dryrun.yaml` 的 `auth.enabled` 为 false | 认证到 V0.5 才实现，production 模板保留占位 hash 且校验必拒，防止直接上线 |
| 2026-10-23 | 模块路径 `action-backup`（无域名） | 尚未建远程仓库；推到 GitHub 时统一改名 |
| 2026-10-23 | V0.2 交付 | 扫描、计划、darwin 后端；真相机 dry-run 实测通过 |
| 2026-10-23 | darwin 后端用 `ioreg -a` + 自写 plist 解析器，不用 `system_profiler` | `system_profiler SPUSBDataType` 本机返回空；`plutil -convert json` 遇 `<data>` 报错；plist XML 解析只需标准库 |
| 2026-10-23 | 卷身份 `volume_key` 用 diskutil `VolumeUUID`（mac）；Linux 将用 blkid UUID | 两者都由 exFAT 卷序列号派生；数据库不跨主机，格式不同无妨 |
| 2026-10-23 | 计划阶段目标不可用时，已完成文件按 `upload / target_unverified` 处理，`target_checked: false` | 文档 §6 要求跳过前核对目标；宁可多列也不漏 |
| 2026-10-23 | 目标存在正式文件但数据库无完成记录 → `conflict`，V0.4 定解决策略 | 不静默覆盖；数据库丢失后的首轮会把已存在文件全部列为冲突，需人工确认 |
| 2026-10-23 | `backup.include_internal / include_sdcard` 省略即 true | 用指针布尔区分"省略"与"显式 false" |
| 2026-10-23 | V0.3 交付 | 传输、断点、校验、调度、任务 API；5 GB 续传实测通过；真 NAS 上传待 NAS 可达 |
| 2026-10-23 | 状态机增加 TRANSFERRING → QUEUED，仅用于用户暂停 | 文档 §7 要求暂停保留断点且不与故障混淆，原状态机无此路径；已回写 V1.3 |
| 2026-10-23 | 断点的 `prefix_sha256` 列存放 base64 的 SHA-256 内部状态（`MarshalBinary`） | 续传时无需重读目标前缀即可继续计算完整哈希；校验阶段仍重读目标全文 |
| 2026-10-23 | SQLite DSN 加 `_txlock=immediate` | 调度器与 API 并发时，`BEGIN DEFERRED` 先读后写会直接 SQLITE_BUSY 而不等待 busy_timeout |
| 2026-10-23 | 写入失败后再探测目标：探测失败归 INTERRUPTED，否则 FAILED | 对应文档 §7 表中"USB/网络断开"与"写入失败"的区分 |
| 2026-10-23 | 计划阶段的 skip 项不建 SKIPPED 任务 | 每次插卡都建一行会让任务表膨胀；历史查询以 COMPLETED 任务为准 |
| 2026-10-23 | V0.4 交付 | 故障注入、重试 / 重连、冲突策略、恢复测试框架；`make test-recovery` 11 场景全绿 |
| 2026-10-23 | 故障触发条件增加 `after: <rule-id>` | 文档 §12.4 的条件无法表达"写失败之后 NAS 才掉线"这类序列，探测点会被计划、重连检查等共同消耗 |
| 2026-10-23 | 重试策略：INTERRUPTED 自动重连重试（设备在位、目标可用、次数未用尽）；FAILED 只人工重试；人工重试不限次数 | INTERRUPTED 是环境原因，FAILED 多为数据或配置原因，盲目重试无益 |
| 2026-10-23 | 校验失败时丢弃断点与 `.part` | 可信前缀本身已损坏，续传只会重复失败 |
| 2026-10-23 | 失败分类：源打不开 / 读错误 / 设备拔出 / 目标探测失败 → INTERRUPTED；目标在线时的写、刷盘、重命名、落库失败 → FAILED | 对应文档 §7 状态表；`db.commit` 失败归 FAILED 是保守选择，可按实机情况调整 |
| 2026-10-23 | `backup.on_conflict` 默认 manual | 文档 §6 要求目标存在而记录缺失时不得静默覆盖；overwrite 需显式开启 |
| 2026-10-23 | 崩溃注入使用退出码 137 | 与 SIGKILL 的惯例一致，恢复测试据此区分"注入崩溃"与其它退出 |
| 2026-10-23 | 发布架构文档 V1.3 | 回写 V0.1 至 V0.4 全部实现偏差；之后新偏差继续先记本表，按阶段回写 |
| 2026-10-23 | V0.5 交付 | passwd 子命令、会话认证与 CSRF、登录限速、SSE、`/config` 在线修改、`/storage/test`、Vue 3 Web UI；全路由 401 测试与 curl 实测通过，证据 `docs/evidence/20261023T0400Z-v05-auth-sse-smoke/` |
| 2026-10-23 | SSE 路由 `/api/v1/events/stream` 绕过 30 秒 `http.TimeoutHandler` | TimeoutHandler 缓冲响应且不实现 Flusher，流 30 秒即断；其它接口仍经超时包装；认证中间件在最外层所以流同样要求登录 |
| 2026-10-23 | 会话 Cookie 不设 `Secure` | 设备在局域网以纯 HTTP 提供服务，设 Secure 浏览器会丢弃 Cookie；HttpOnly + SameSite=Strict 保留；上 HTTPS 反代后再加 |
| 2026-10-23 | 登录限速按来源 IP 统计全部尝试（含成功），不信任 `X-Forwarded-For` | 服务直连无反代；成功也计数可防止用正确口令刷会话 |
| 2026-10-23 | 用户名不匹配时仍执行一次 bcrypt 比较 | 避免通过响应时间判断用户名是否存在 |
| 2026-10-23 | `PATCH /config` 白名单仅 6 项，用 yaml.v3 Node 原地改写并原子替换文件 | 文档 §9 只说"白名单字段"；选运行时能安全生效的项；`yaml.Marshal` 整体重写会丢注释，Node 方式保留注释与顺序 |
| 2026-10-23 | `POST /storage/test` 只读，不写探测文件 | 规范第 17 条要求新增写入路径必须配注入点与测试；探测挂载类型、剩余空间与根目录已足够判断可用性 |
| 2026-10-23 | `web/dist` 构建产物入库，Vite 输出固定文件名 | `go build` / `make build-arm64` 不依赖 Node；服务端所有响应 `Cache-Control: no-store`，无需哈希文件名；改前端必须 `make web-build` 一并提交 |
| 2026-10-23 | 前端不用 vue-router / pinia，单页 tab + reactive store | 六个页面、单用户，少两个依赖；状态通过 SSE 推送而非轮询 |
| 2026-10-23 | `passwd` 非终端输入必须显式 `--stdin`，口令至少 8 字符，不自动改 `auth.enabled` | 防止脚本误把其它内容当口令；是否启用认证由配置显式决定 |
| 2026-10-23 | 发布架构文档 V1.4 | 回写 V0.5 实现：§9 认证与 SSE 细节、§10 白名单、§11.2、§14、§16 |
| 2026-10-23 | V0.5 Web UI 以 Chrome（chrome-devtools-mcp）逐页核对，curl 不替代浏览器验收 | 真实 SSE 渲染、按钮状态与表单关联只有浏览器能验证；核对发现 3 处修正（标题 CSS 大写、登录 label 关联、设置控件 name），记录 `docs/evidence/20261023T0400Z-v05-auth-sse-smoke/ui-browser-check.md` |
| 2026-10-23 | 前端验收规则：每个阶段的 UI 改动都在浏览器核对并留记录与截图，放进该阶段证据目录 | 与“验证输出放 evidence、文档只引用”的约定一致；chrome-devtools-mcp 可在隔离上下文复现 |
| 2026-10-27 | 设备 apt 改用 USTC 镜像，但只放 `/tmp/aptsrc/ustc.sources` 临时源，不动 `/etc/apt/sources.list.d/debian.sources` | `deb.debian.org` 在该网络不可达（IPv6 无路由、IPv4 超时），装不上 `cifs-utils`；永久换源属于改设备基础配置，留给用户决定。证据 `docs/evidence/20261027T0320Z-device-deploy-smb/` |
| 2026-10-27 | 设备装 `cifs-utils 2:7.0-2`（`mount.cifs` 7.0），`cifs` 模块可加载 | V0.6 挂载 NAS 的前置依赖，问题「设备缺 mount.cifs」就此关闭 |
| 2026-10-27 | `scripts/deploy.sh` 落地，`make deploy` 接上 | 远程命令走 `cmd_ip.sh`；它不支持文件拷贝，只有 scp 一步在脚本内调 `sshpass`，口令从 `cmd_ip.conf` 读取经 `SSHPASS` 传入，不进命令行 |
| 2026-10-27 | systemd 单元加 `RuntimeDirectory=action-backup` 与 `ExecStartPre=/bin/mkdir -p /run/action-backup/source` | `/run` 是 tmpfs，`install.sh` 建的目录重启即丢，单元原本没有重建逻辑 |
| 2026-10-27 | 单元加 `ReadWritePaths=/etc/action-backup`，`install.sh` 把配置目录改为 `root:actionbackup 0770` | `ProtectSystem=strict` 下 `/etc` 对服务只读，`PATCH /config` 的「临时文件 + rename」写回必然失败；实测报 `read-only file system`，修后写回成功 |
| 2026-10-27 | 设备首轮以 dry-run + 认证开启运行，`storage.share` 保持占位 | NAS 尚未确定；dry-run 不写目标，存储探测告警不影响服务；服务监听 0.0.0.0:8080，局域网可达，故认证先开 |
