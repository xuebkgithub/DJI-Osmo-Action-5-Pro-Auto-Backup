# action-backup

DJI Osmo Action 5 Pro → NAS 自动备份服务。Go 单体，macOS 开发，交叉编译到 OEC-Turbo（ARM64 Linux），systemd 部署。
架构文档见上级目录 `docs/Action_Backup_软件架构设计_V1.3.md`（与 V0.4 代码一致）。

## 当前版本：V0.4 故障注入与恢复

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
- 占位 Web 页面（V0.5 替换为 Vue 3）。
- fixtures 生成脚本、ARM64 构建与安装包、systemd 单元、udev 规则、install.sh。

未实现（按阶段）：Web UI、用户名密码认证、SSE（V0.5）；Linux udev / mount helper / CIFS 挂载与实机验证（V0.6）、Web UI 与认证（V0.5）、Linux udev / mount helper / CIFS 挂载（V0.6）。

## 快速开始（mac）

```bash
make tools            # go 1.25+、golangci-lint；node / pnpm 仅 V0.5 需要
make fixtures         # 生成 tests/fixtures/cam1（8 MB 小文件可加 --size，见脚本）
make dev              # simulation 模式启动，http://127.0.0.1:8080
curl -s localhost:8080/api/v1/system/mode
curl -s -X POST localhost:8080/api/v1/test/sim/devices/sim-cam-1/detach
```

```bash
make test             # 单元测试
make test-integration # simulation 端到端（-tags faultinject,integration）
make lint
make build-arm64      # dist/action-backup-linux-arm64（production，无 faultinject）
make package          # dist/action-backup-<version>-linux-arm64.tar.gz
```

Go 模块代理：本机默认 `goproxy.cn` 不可用时，用 `GOPROXY=https://proxy.golang.org,direct`。
依赖版本固定在 Go 1.25 兼容范围（`modernc.org/sqlite v1.59.0`、`golang.org/x/sys v0.47.0`），升级到需要 Go 1.26 的版本前先升级工具链。

## 配置

| 文件 | 用途 |
| --- | --- |
| `configs/config.example.yaml` | 实机 production 模板，`password_hash` 为占位符，校验会拒绝，必须用 `action-backup passwd`（V0.5）生成 |
| `configs/config.dryrun.yaml` | 实机首轮 dry-run，零写入 |
| `configs/config.dev.yaml` | mac simulation，本地目录模拟相机与 NAS |
| `configs/config.mac-dryrun.yaml` | mac 接真相机的 dry-run，真实 USB 发现，零写入 |

校验配置：`action-backup --config <file> --check`。

## 部署到 OEC-Turbo（当前无 SSH）

1. `make package`。
2. 把 `dist/action-backup-<version>-linux-arm64.tar.gz` 拷到设备并解压。
3. 以 root 执行 `install.sh`：校验 SHA256、拒绝测试构建、创建用户与目录、注册 systemd，不自动启动。
4. 编辑 `/etc/action-backup/config.yaml`，先以 dry-run 启动验证。

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
internal/api             路由与处理器
internal/app             装配与生命周期
internal/faultinject     注入点定义（规则引擎 V0.4）
web/                     嵌入的静态资源
configs/ deployments/ scripts/ tests/
```
