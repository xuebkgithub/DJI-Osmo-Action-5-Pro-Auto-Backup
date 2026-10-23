# action-backup

DJI Osmo Action 5 Pro → NAS 自动备份服务。Go 单体，macOS 开发，交叉编译到 OEC-Turbo（ARM64 Linux），systemd 部署。
架构文档见上级目录 `docs/Action_Backup_软件架构设计_V1.4.md`（与 V0.5 代码一致）。

## 当前版本：V0.5 Web UI、认证与 SSE

V0.5 新增：

- `action-backup passwd --config <file> [--username <name>] [--stdin]`：交互式读两次口令（非终端必须显式 `--stdin`），至少 8 字符，bcrypt cost 12 写入 `server.auth.password_hash`，保留文件注释。不接受命令行明文口令。
- `internal/api/auth`：用户名密码登录、内存会话表（`crypto/rand` 64 hex 会话 ID，默认 24 小时）、`ab_session` Cookie（`HttpOnly; SameSite=Strict; Path=/`，局域网 HTTP 不设 Secure）、每会话 CSRF Token（POST / PATCH / DELETE 必须带 `X-CSRF-Token`，否则 403 `CSRF_INVALID`）、按来源 IP 的登录限速（默认每分钟 5 次，超出 429）、未知用户名也做一次 bcrypt 比较。`auth.login / logout / login_failed / login_rate_limited` 事件入库。
- 门禁：`server.auth.enabled: true` 时 `/api/` 下除 `/auth/login`、`/system/health` 外全部 401 `UNAUTHENTICATED`；静态页放行；production 模式配置校验强制启用。
- SSE `GET /api/v1/events/stream`：连接即发 `hello`，随后转发事件总线全部事件（`event:` 为类型，`id:` 为持久化 ID，`data:` 为完整事件记录）；同一任务多条 `backup.progress` 在一次刷出中只保留最新；15 秒心跳；该路由绕过 30 秒请求超时。
- `GET /config`（口令哈希脱敏为 `***`，附白名单说明）、`PATCH /config`（白名单：`device.auto_backup`、`backup.verify_mode`、`backup.on_conflict`、`backup.retry_count`、`backup.reconcile_interval_sec`、`logging.level`；先校验、再原子写回配置文件并保留注释、最后在运行中生效；发 `config.changed` 事件）、`POST /storage/test`（只读探测：挂载类型、来源、剩余空间、根目录是否存在，不写任何文件）、`GET /devices/{id|key}/volumes`。
- Web UI（Vue 3 + Vite，无路由库，`web/src`）：登录页；非 production 顶部模式横幅；总览（健康、任务计数、当前传输进度、开始 / 暂停 / 恢复 / 生成计划）、设备与卷、计划预览（可过滤 upload / skip / conflict）、任务列表（分页、状态过滤、实时进度、取消 / 重试、展开详情）、事件日志（SSE 实时追加、过滤）、设置（在线修改白名单项、查看脱敏配置、测试目标存储）、测试面板（仅测试构建且非 production：模拟插拔、故障规则表单 / 预设 / JSON，命中计数）。构建产物 `web/dist` 已入库并由 `go:embed` 嵌入，文件名固定，纯 Go 构建无需 Node。
- 运行时可调：`backup.Service.SetVerifyMode / SetConflictPolicy` 与 `scheduler.Configure` 加锁，重连周期每轮重新读取。
- 测试：`internal/api/auth` 单元测试；`internal/app/auth_test.go` 遍历全部路由验证未登录 401、CSRF、注销、限速、`PATCH /config` 写回文件与运行时生效、SSE 真实流；`cmd/action-backup` 的 passwd 测试。

2026-10-23 实测：测试构建 + 认证开启的 simulation 服务，curl 覆盖 401 全路由、登录 Cookie 属性、CSRF 403、`PATCH /config` 写回、SSE 实时插拔事件、第 4 次登录 429、注销后 401；随后用 Chrome（chrome-devtools-mcp）逐页核对 Web UI：登录 / 错误口令、SSE 实时进度、开始 / 暂停 / 恢复 / 取消、设置写回与存储测试、测试面板添加规则、设备与事件页、退出，记录在 `ui-browser-check.md` 并附截图。核对后修正：设备名标题不再被 CSS 转为大写，登录表单 label 关联 input，设置表单控件加 name。证据在 `../docs/evidence/20261023T0400Z-v05-auth-sse-smoke/`。

V0.4 新增：

- `internal/faultinject` 规则引擎（仅 `-tags faultinject` 构建）：12 个注入点；触发条件 once / nth_call / after_bytes / probability / times / after（依赖另一规则先命中）；动作 error（errno）/ delay / stall / throttle / short_write / corrupt / disconnect / mismatch / crash。规则来自 `faults.rules` 或运行时 `/api/v1/test/faults`（GET / POST / DELETE / reset）。命中发 `fault.hit` 事件。
- 自动重试策略：INTERRUPTED 任务由调度器每 `reconcile_interval_sec` 检查一次，设备在位、目标可用且 `retry_count < backup.retry_count` 时自动重新入队；FAILED 不自动重试；人工 retry 不受次数限制；`backups/start` 报告 `tasks_retry_exhausted`。
- 校验失败即丢弃断点与 `.part`，下次从头重传，避免在损坏的前缀上反复失败。
- 冲突策略 `backup.on_conflict`：manual（默认，不动）/ overwrite（照常上传）/ skip。
- 恢复测试框架 `tests/recovery`（`make test-recovery`）：11 个场景，其中进程崩溃场景用子进程真实 `os.Exit(137)` 后重启验证续传。证据见 `../docs/evidence/20261023T0330Z-recovery-suite/`。

V0.3 新增：

- `internal/backup`：流式传输 Worker。源只读打开并复核身份；写 `.part`；按字节数或时间刷盘并把可信偏移和 SHA-256 哈希状态写入 `checkpoints`；恢复时只信任数据库偏移，`.part` 超出部分截断，断点不可信则从头重传；完成后 basic 或 sha256 校验（sha256 会重读目标文件）再重命名归档。
- `internal/scheduler`：单 Worker 队列。暂停让当前任务在断点处回到 QUEUED，取消删除 `.part`，重试只接受 FAILED / INTERRUPTED；进度每秒聚合为 `backup.progress` 事件。
- `backup.Service`：计划中的 upload 项落为 `source_files` 与 `backup_tasks` 并入队；已有未完成任务则复用，FAILED / INTERRUPTED 自动重新入队；冲突项不动。
- 启动时把上次遗留的 TRANSFERRING / VERIFYING 标为 INTERRUPTED；`auto_backup` 下设备就绪即重新入队续传。
- API：`POST /backups/start|pause|resume`、`GET /backups/history`、`GET /tasks`、`GET /tasks/{id}`（含实时进度）、`POST /tasks/{id}/cancel|retry`；`/system/status` 增加 `scheduler` 段。dry-run 下写类接口返回 `409 MODE_READ_ONLY`。
- SQLite 写事务改为 `BEGIN IMMEDIATE`（DSN `_txlock=immediate`），避免并发时先读后写直接返回 SQLITE_BUSY。

2026-10-23 实测：5 GB 随机文件在 simulation 下以 sha256 模式备份，中途 SIGTERM 后重启自动续传，最终哈希与清单一致。证据见 `../docs/evidence/20261023T0305Z-sim-5gb-sha256-resume/`。
**未完成**：mac 连真 NAS（premounted）上传一次，因 NAS 当天不可达；代码路径与 local 相同，只差 `StorageProbe` 换成 smbfs 探测。

V0.2 新增：

- `internal/scanner`：只读递归扫描 DCIM，扩展名不区分大小写，忽略 `._*`、`.DS_Store`、`.fseventsd` 等，跳过符号链接。
- `internal/syncengine`：对比历史记录与目标文件生成 `Plan`（upload / skip / conflict 及原因），目标不可用时不做跳过判断并标记 `target_checked: false`。
- `POST /api/v1/backups/plan`、`GET /api/v1/backups/plan`；`device.auto_backup` 为 true 时设备就绪后自动生成计划，dry-run 到此为止。
- darwin 真相机后端：轮询 `ioreg` 与 `diskutil`（自带 plist 解析器），从产品名解析序列号，按 LUN 分类卷；`configs/config.mac-dryrun.yaml` 可在 mac 上对真相机做 dry-run。
- `simulation.device_source: volumes | both` 可同时使用 fixtures 与真相机。

2026-10-23 在 mac 上接真相机 dry-run 实测：识别双卷，列出 microSD 上 3 个 MP4 共 34 GB，内置存储为空，零写入。证据见 `../docs/evidence/20261023T0249Z-mac-dryrun-real-camera/`。

V0.1 已实现：

- 配置解析与模式组合校验（`production` / `dry-run` / `simulation`），危险组合拒绝启动。
- SQLite（`modernc.org/sqlite`，纯 Go）、版本化迁移、设备 / 卷 / 事件 / 任务仓储。
- 运行模式门禁 `runmode.Guard`：dry-run 下所有目标写入入口返回 `ErrModeReadOnly`。
- 平台抽象 `internal/platform`：`sim` 后端（目录型模拟相机，attach / detach）、darwin 与 linux 的挂载点探测（statfs）。
- 目标存储 `internal/storage`：local / premounted / cifs 三类，`destination_layout` 模板，路径越界校验。
- 设备管理器：枚举、事件、持久化。
- HTTP API：`/api/v1/system/{health,status,mode}`、`/devices`、`/devices/rescan`、`/events`，以及仅测试构建可见的 `/api/v1/test/sim/*`。
- 占位 Web 页面（V0.5 已替换为 Vue 3）。
- fixtures 生成脚本、ARM64 构建与安装包、systemd 单元、udev 规则、install.sh。

未实现（按阶段）：Linux udev / mount helper / CIFS 挂载与实机验证、性能测试、发布（V0.6）；真 NAS premounted 上传待 NAS 可达后补做。

## 快速开始（mac）

```bash
make tools            # go 1.25+、golangci-lint；node / pnpm 仅改前端时需要
make fixtures         # 生成 tests/fixtures/cam1（8 MB 小文件可加 --size，见脚本）
make dev              # simulation 模式启动，浏览器打开 http://127.0.0.1:8080（dev 配置免登录）
curl -s localhost:8080/api/v1/system/mode
curl -s -X POST localhost:8080/api/v1/test/sim/devices/sim-cam-1/detach
```

认证开启时的 curl 用法：

```bash
curl -c c.txt -X POST -H 'Content-Type: application/json' -d '{"username":"admin","password":"..."}' localhost:8080/api/v1/auth/login
CSRF=$(curl -s -b c.txt localhost:8080/api/v1/auth/me | python3 -c 'import json,sys;print(json.load(sys.stdin)["data"]["csrf_token"])')
curl -b c.txt -H "X-CSRF-Token: $CSRF" -X POST localhost:8080/api/v1/backups/start
curl -N -b c.txt localhost:8080/api/v1/events/stream
```

```bash
make test             # 单元测试
make test-integration # simulation 端到端（-tags faultinject,integration）
make test-recovery    # 故障注入 + 进程崩溃重启
make lint
make web-dev          # 改前端：Vite 开发服务器，/api 代理到 8080
make web-build        # 改前端后必须执行并提交 web/dist
make build-arm64      # dist/action-backup-linux-arm64（production，无 faultinject）
make package          # dist/action-backup-<version>-linux-arm64.tar.gz
```

Go 模块代理：本机默认 `goproxy.cn` 不可用时，用 `GOPROXY=https://proxy.golang.org,direct`。
依赖版本固定在 Go 1.25 兼容范围（`modernc.org/sqlite v1.59.0`、`golang.org/x/sys v0.47.0`），升级到需要 Go 1.26 的版本前先升级工具链。

## 配置

| 文件 | 用途 |
| --- | --- |
| `configs/config.example.yaml` | 实机 production 模板，`password_hash` 为占位符，校验会拒绝，必须用 `action-backup passwd --config /etc/action-backup/config.yaml` 生成 |
| `configs/config.dryrun.yaml` | 实机首轮 dry-run，零写入 |
| `configs/config.dev.yaml` | mac simulation，本地目录模拟相机与 NAS |
| `configs/config.mac-dryrun.yaml` | mac 接真相机的 dry-run，真实 USB 发现，零写入 |

校验配置：`action-backup --config <file> --check`。

## 部署到 OEC-Turbo（当前无 SSH）

1. `make package`。
2. 把 `dist/action-backup-<version>-linux-arm64.tar.gz` 拷到设备并解压。
3. 以 root 执行 `install.sh`：校验 SHA256、拒绝测试构建、创建用户与目录、注册 systemd，不自动启动。
4. 编辑 `/etc/action-backup/config.yaml`，执行 `action-backup passwd --config /etc/action-backup/config.yaml` 设置登录口令，先以 dry-run 启动验证。

## 目录

```text
cmd/action-backup        入口
internal/config          配置与校验
internal/runmode         模式与写入门禁
internal/platform        平台抽象：sim / darwin / linux / fsinfo / raw / plist
internal/scanner         DCIM 只读扫描
internal/syncengine      增量对比与计划
internal/backup          传输 Worker、备份服务
internal/scheduler       单 Worker 队列
internal/domain          设备、卷、文件、任务状态机、计划
internal/database        SQLite、迁移、repository
internal/storage         目标存储、layout 模板
internal/device          设备管理器
internal/events          事件总线
internal/api             路由与处理器、SSE
internal/api/auth        会话、CSRF、登录限速、门禁中间件
internal/app             装配与生命周期
internal/faultinject     注入点定义（规则引擎 V0.4）
web/                     Vue 3 前端（src/）与构建产物（dist/，已入库，go:embed）
configs/ deployments/ scripts/ tests/
```
