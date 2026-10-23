# DJI Osmo Action 5 Pro 自动备份系统

**软件架构设计方案 V1.0**

| 项目 | 内容 |
| --- | --- |
| 目标硬件 | 网心云 OEC-Turbo |
| 操作系统 | 已选定的 ARM64 Linux 固件 |
| 开发语言 | Go |
| SMB 传输 | Linux CIFS 挂载，优先 SMB3 |
| 数据库 | SQLite |
| 校验策略 | 默认基础校验，可选 SHA-256 |
| 部署方式 | systemd 原生部署 |
| 管理方式 | Web UI + REST API + SSE |
| 备份来源 | DJI Osmo Action 5 Pro 内部存储及 microSD |
| 备份目标 | SMB NAS |
| 性能目标 | 81.1 GB 全量备份在 30 分钟内完成，需实机验证 |

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

以上相机读取测试不能直接代表 OEC-Turbo 到 NAS 的实际吞吐。30 分钟目标应统计扫描、上传及选定校验流程的总耗时。

## 2. 技术栈

| 功能 | 技术 | 说明 |
| --- | --- | --- |
| 核心应用 | Go | 单体模块化，交叉编译 ARM64 |
| USB 检测 | udev、sysfs | 监听事件并支持主动枚举 |
| 存储卷访问 | Linux exFAT | 源存储卷必须只读挂载 |
| 文件扫描 | `os`、`filepath` | 递归扫描 DCIM |
| 增量备份 | Go Sync Engine | 任务规划、变化识别、冲突处理 |
| 数据传输 | Go 流式 I/O | 固定缓冲区，直接写入 CIFS 挂载目录 |
| SMB | Linux CIFS 客户端 | SMB2/SMB3，优先 SMB3 |
| 元数据 | SQLite | 推荐评估 `modernc.org/sqlite` |
| REST API | Go `net/http` | 轻量 HTTP 服务 |
| Web UI | Vue 3 构建的静态资源 | 可嵌入 Go 程序，浏览器访问 |
| 实时状态 | SSE | 推送聚合后的进度与事件 |
| 后台服务 | systemd | 开机自启、自动重启 |
| 日志 | slog / journald | 结构化日志 |

首版只运行一个传输 Worker，默认缓冲区为 4 MiB，后续通过性能测试调整。

## 3. 总体架构与数据流

```text
DJI Osmo Action 5 Pro
  │ USB 3.x
  ▼
Device Manager (udev/sysfs)
  │
  ▼
Volume Manager (识别内部存储及 microSD)
  │ exFAT 只读挂载
  ▼
File Scanner (递归扫描 DCIM)
  │
  ▼
Sync Engine (SQLite 历史记录对比)
  │
  ▼
Task Scheduler (单 Worker 队列)
  │
  ▼
Backup Worker (分块读取、断点恢复)
  │
  ▼
Linux CIFS 挂载目录
  │ SMB3 / 千兆以太网
  ▼
NAS 临时文件 *.part
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

采用模块化单体应用，划分四个逻辑层：

- **Interface**：HTTP API、SSE、Web 页面。
- **Application**：调度、恢复、备份业务编排。
- **Domain**：设备、卷、源文件、任务、状态转换。
- **Infrastructure**：SQLite、udev、系统挂载、CIFS、文件 I/O。

## 4. 工程目录

```text
action-backup/
├── cmd/
│   └── action-backup/
│       └── main.go
├── internal/
│   ├── config/
│   │   ├── config.go
│   │   └── validate.go
│   ├── device/
│   │   ├── detector.go
│   │   ├── udev.go
│   │   ├── identity.go
│   │   └── manager.go
│   ├── volume/
│   │   ├── discover.go
│   │   ├── mount.go
│   │   └── manager.go
│   ├── scanner/
│   │   ├── scanner.go
│   │   ├── filters.go
│   │   └── metadata.go
│   ├── syncengine/
│   │   ├── comparator.go
│   │   ├── planner.go
│   │   └── conflicts.go
│   ├── scheduler/
│   │   ├── scheduler.go
│   │   ├── queue.go
│   │   └── recovery.go
│   ├── backup/
│   │   ├── worker.go
│   │   ├── transfer.go
│   │   ├── checkpoint.go
│   │   └── retry.go
│   ├── storage/
│   │   ├── cifs.go
│   │   ├── health.go
│   │   └── filesystem.go
│   ├── verify/
│   │   ├── basic.go
│   │   └── sha256.go
│   ├── database/
│   │   ├── sqlite.go
│   │   ├── migrations/
│   │   └── repository/
│   ├── api/
│   │   ├── router.go
│   │   ├── middleware/
│   │   ├── handlers/
│   │   └── dto/
│   ├── events/
│   │   └── broadcaster.go
│   └── domain/
│       ├── device.go
│       ├── volume.go
│       ├── file.go
│       ├── task.go
│       └── errors.go
├── web/
│   ├── src/
│   ├── public/
│   └── package.json
├── configs/
│   └── config.example.yaml
├── deployments/
│   ├── action-backup.service
│   ├── 99-action-backup.rules
│   └── install.sh
├── scripts/
│   ├── build-arm64.sh
│   └── test-performance.sh
├── tests/
│   ├── integration/
│   ├── fixtures/
│   └── recovery/
├── docs/
│   ├── architecture.md
│   ├── api.md
│   ├── database.md
│   └── development.md
├── go.mod
├── go.sum
├── Makefile
└── README.md
```

## 5. 模块职责与接口

| 模块 | 主要职责 | 建议接口 |
| --- | --- | --- |
| Device Manager | 设备发现、热插拔、USB 身份 | `Discover`、`Watch` |
| Volume Manager | 双卷枚举、只读挂载和卸载 | `Discover`、`MountReadOnly`、`Unmount` |
| File Scanner | DCIM 递归扫描、文件元数据 | `Scan` |
| Sync Engine | 增量判断、任务计划、冲突 | `Plan` |
| Task Scheduler | 排队、暂停、恢复、重试 | `Enqueue`、`Pause`、`Resume` |
| Backup Worker | 流式传输、进度、断点 | `Transfer` |
| Storage Manager | CIFS 健康检查、目标路径和容量 | `CheckHealth` |
| Verify Service | 基础校验或 SHA-256 | `Verify` |
| Database | 存储和查询持久化数据 | Repository |
| Web API | 查询、设置、任务控制 | HTTP Handlers |
| Event Manager | 广播进度和错误 | `Publish`、`Subscribe` |

核心接口草案：

```go
type DeviceManager interface {
    Discover(ctx context.Context) ([]Device, error)
    Watch(ctx context.Context) (<-chan DeviceEvent, error)
}

type VolumeManager interface {
    Discover(ctx context.Context, device Device) ([]Volume, error)
    MountReadOnly(ctx context.Context, volume Volume) (string, error)
    Unmount(ctx context.Context, volume Volume) error
}

type Scanner interface {
    Scan(ctx context.Context, volume Volume) ([]SourceFile, error)
}

type SyncEngine interface {
    Plan(ctx context.Context, files []SourceFile) ([]BackupTask, error)
}

type TransferEngine interface {
    Transfer(ctx context.Context, task BackupTask) error
}

type Verifier interface {
    Verify(ctx context.Context, task BackupTask) (VerifyResult, error)
}
```

类型 `Device`、`Volume`、`SourceFile`、`BackupTask` 等在 `internal/domain` 中定义。

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

CREATE TABLE IF NOT EXISTS devices (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    device_key TEXT NOT NULL UNIQUE,
    vendor_id TEXT NOT NULL,
    product_id TEXT NOT NULL,
    serial_number TEXT,
    name TEXT NOT NULL,
    last_seen_at TEXT,
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS volumes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    device_id INTEGER NOT NULL REFERENCES devices(id),
    volume_key TEXT NOT NULL,
    volume_type TEXT NOT NULL
        CHECK (volume_type IN ('internal','sdcard','unknown')),
    filesystem TEXT NOT NULL,
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
    prefix_sha256 TEXT,
    updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id INTEGER REFERENCES backup_tasks(id),
    level TEXT NOT NULL,
    event_type TEXT NOT NULL,
    message TEXT NOT NULL,
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
- `COMPLETED` 代表正式目标文件已成功确认。增量跳过前仍需确认 NAS 目标存在且必要元数据匹配。
- SQLite 保存在 OEC-Turbo 本地持久化文件系统，不放在 NAS 或 `/run`。
- 数据库迁移使用明确的版本管理。
- 断点记录只能前进至经过确认的可靠写入位置。
- WAL 配合合理的 checkpoint 策略，避免异常磁盘占用。

## 7. 任务状态机

```text
PENDING → QUEUED → TRANSFERRING → VERIFYING → COMPLETED
               │           │           │
               │           ├───────────┴──→ FAILED
               │           └──────────────→ INTERRUPTED
               └──────────────────────────→ CANCELLED

已存在可信备份：PENDING → SKIPPED
恢复或重试：INTERRUPTED / FAILED → QUEUED
```

| 当前状态 | 条件 | 下一状态 |
| --- | --- | --- |
| PENDING | 生成任务并入队 | QUEUED |
| QUEUED | Worker 开始 | TRANSFERRING |
| TRANSFERRING | 文件传输完成 | VERIFYING |
| VERIFYING | 校验及归档完成 | COMPLETED |
| TRANSFERRING | USB/网络断开 | INTERRUPTED |
| TRANSFERRING、VERIFYING | 校验或写入失败 | FAILED |
| INTERRUPTED、FAILED | 可恢复或允许重试 | QUEUED |
| PENDING | 已有可信备份 | SKIPPED |
| 非终态 | 用户取消 | CANCELLED |

用户暂停由调度器管理，保留可靠断点。避免将手动暂停与故障状态混淆。

### 7.1 文件传输及归档

1. 验证相机卷和 CIFS 目标已经就绪。
2. 检查已完成文件及相关任务记录。
3. 创建目标 `.part` 文件或验证历史临时文件。
4. 从源文件指定偏移位置分块读取，顺序写入 NAS。
5. 定期确认持久化并更新 SQLite 断点。
6. 数据写完后按配置执行基础或 SHA-256 校验。
7. 在同一目标文件系统内完成临时文件正式重命名。
8. 更新数据库任务状态，发送完成事件。

### 7.2 中断恢复

服务重启后：

1. 把上次未完成的 `TRANSFERRING`、`VERIFYING` 任务标记为 `INTERRUPTED`。
2. 重新发现设备及卷，确认源文件身份没有变化。
3. 检查 NAS 挂载和 `.part` 文件。
4. 对照数据库 checkpoint 与临时文件实际大小及有效数据前缀。
5. 对未确认的后续数据截断或重新校验。
6. 对可信断点继续，否则重新上传当前文件。
7. 重新执行文件完成校验，正式归档。

不得只根据 `.part` 大小认定数据正确。

## 8. 完整性校验

### 8.1 默认 basic

- 源文件身份、大小及修改时间复核。
- 累计成功写入的字节数校验。
- NAS 临时文件大小校验。
- 检查写入、同步、关闭和重命名的错误。
- 成功后记录完整的备份元数据。

基础校验不提供完整内容逐字节一致性保证。

### 8.2 可选 sha256

- 传输中计算完整源文件 SHA-256。
- 从 NAS 目标文件重新计算完整 SHA-256 并比较。
- 断点恢复时必须正确重建哈希状态。
- CIFS 目标重新读取会额外消耗网络时间和 NAS I/O。

## 9. REST API

统一前缀：`/api/v1`。

| HTTP | 路径 | 作用 |
| --- | --- | --- |
| GET | `/system/status` | 系统状态与统计 |
| GET | `/system/health` | USB、NAS、数据库健康情况 |
| GET | `/devices` | 设备列表 |
| GET | `/devices/{id}/volumes` | 存储卷列表 |
| POST | `/devices/rescan` | 重新扫描设备 |
| GET | `/tasks` | 任务列表、分页、过滤 |
| GET | `/tasks/{id}` | 任务详情 |
| POST | `/backups/start` | 开始备份 |
| POST | `/backups/pause` | 暂停备份 |
| POST | `/backups/resume` | 恢复备份 |
| POST | `/tasks/{id}/retry` | 重试失败任务 |
| POST | `/tasks/{id}/cancel` | 取消任务 |
| GET | `/backups/history` | 历史备份 |
| GET | `/config` | 脱敏配置 |
| PATCH | `/config` | 修改白名单配置项 |
| POST | `/storage/test` | 测试 SMB 配置 |
| GET | `/events` | 历史事件 |
| GET | `/events/stream` | SSE 实时进度 |

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
```

接口需有认证、输入校验、请求超时、分页限制、跨站请求防护和日志脱敏。进度推送建议按约 1 秒聚合。

## 10. 配置文件

`/etc/action-backup/config.yaml`：

```yaml
server:
  listen: "0.0.0.0:8080"
  auth_enabled: true

device:
  vendor_id: "0506"
  product_id: "8002"
  auto_detect: true
  auto_backup: true
  mount_root: "/run/action-backup/source"
  readonly: true

backup:
  include_internal: true
  include_sdcard: true
  source_directory: "DCIM"
  file_extensions:
    - ".mp4"
  worker_count: 1
  buffer_size_mb: 4
  retry_count: 3
  checkpoint_interval_sec: 5
  checkpoint_bytes_mb: 64
  verify_mode: "basic"
  delete_source: false

storage:
  type: "cifs"
  share: "//192.168.1.100/VideoBackup"
  mount_point: "/mnt/action-backup-nas"
  smb_version: "3.1.1"
  credentials_file: "/etc/action-backup/smb.credentials"
  destination_root: "DJI-Action5Pro"

database:
  path: "/var/lib/action-backup/backup.db"

logging:
  level: "info"
  format: "json"
```

SMB 用户名与密码存放在独立凭据文件中，权限限制为 `0600`，不能在 Web API 和日志中直接暴露。

## 11. systemd 原生部署

建议权限分离：

- 主业务应用使用 `actionbackup` 普通用户运行。
- 特权挂载操作由受限 root helper 执行，仅接受经过验证的固定设备及目标挂载路径。
- USB 源卷一律只读挂载，NAS 挂载目录只用于目的文件写入。
- udev 负责事件触发，但服务启动时也要主动枚举设备。

`/etc/systemd/system/action-backup.service` 初始模板：

```ini
[Unit]
Description=DJI Action Backup Service
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=actionbackup
Group=actionbackup
ExecStart=/opt/action-backup/action-backup --config /etc/action-backup/config.yaml
WorkingDirectory=/var/lib/action-backup
Restart=on-failure
RestartSec=5
TimeoutStopSec=45
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
ReadWritePaths=/var/lib/action-backup
ReadWritePaths=/run/action-backup
ReadWritePaths=/mnt/action-backup-nas
LimitNOFILE=8192

[Install]
WantedBy=multi-user.target
```

该模板需要在固件实机环境中验证目录权限、源卷读取权限、CIFS 挂载方式以及 systemd 沙箱兼容性。**切勿仅通过挂载目录存在判断 NAS 已连接**，必须确认目标是期望的 CIFS 挂载，并避免掉线后误写本地底层目录。

## 12. 开发阶段与验收

| 阶段 | 开发内容 | 验收标准 |
| --- | --- | --- |
| V0.1 | Go 工程、配置、SQLite、日志、HTTP、systemd | ARM64 可编译并启动，数据库及健康 API 正常 |
| V0.2 | USB 设备枚举、只读挂载、DCIM 扫描 | 识别双卷并列出全部目标文件，源文件不变 |
| V0.3 | CIFS、流式传输、`.part` 文件、基础校验 | 5 GB 以上 MP4 可上传并归档 |
| V0.4 | 增量、状态机、断点、自动重试 | 重复接入不重复上传，异常后安全恢复 |
| V0.5 | Web UI、认证、SSE、SHA-256 可选校验 | 浏览器可以管理任务和查询日志 |
| V0.6 | 性能测试、故障注入、发布 | 以 81.1 GB / 30 分钟为目标，提交真实结果 |

### 12.1 必测场景

- 首次插入相机，自动发现并备份。
- 再次插入相机，跳过已确认完成的文件。
- USB 设备路径或枚举顺序变化，仍能识别存储卷。
- 上传中断网、拔出相机、NAS 服务重启或设备重启。
- NAS 容量不足和 SMB 凭据错误。
- 临时文件数据损坏或无法验证断点。
- 同名文件被修改或存储卡更换。
- NAS 已经断开但挂载目录仍存在，必须拒绝错误写入。
- Web 未授权请求、路径遍历及恶意配置值。
- 大文件连续传输、长期运行时内存和 CPU 负载。
- 81.1 GB 全量传输的端到端真实耗时。

## 13. 开发规范（可写入 AGENTS.md）

1. Go 实现模块化单体，主应用采用普通用户权限运行。
2. 所有源卷必须只读挂载，不得删除或修改相机文件。
3. 默认仅备份 `DCIM/**/*.MP4`，必须保留相对目录。
4. 数据流应直接从源文件进入 CIFS 目标，不将视频完整缓存到系统盘。
5. 禁止 SMB1，优先 SMB3。
6. 传输使用 `.part` 临时文件，完成校验后正式归档。
7. 所有任务状态转移受状态机约束并持久化。
8. 数据库保存设备、卷、源文件版本、任务、断点、事件。
9. 断点必须可验证；不可信时重新上传。
10. 禁止用字符串拼接构建任意 shell 命令。
11. 设备及存储卷身份不能只依赖 `/dev/sdX`。
12. API 输入、文件路径和特权操作必须做白名单校验。
13. 密码、Token、SMB 凭据不得输出到日志或未经授权的 API。
14. 提供单元、集成、异常恢复及性能测试。
15. 首版使用一个 Worker，基于实测决定后续并发和缓冲区参数。

## 14. 下一步开发任务

**立即进入 V0.1**，按以下顺序实施：

1. 初始化 `go.mod` 和工程目录。
2. 实现 YAML 配置解析和配置校验。
3. 建立 SQLite 连接、Migration 和 Repository 骨架。
4. 建立结构化日志及事件模型。
5. 实现 `/api/v1/system/health`、`/api/v1/system/status`。
6. 实现配置和模块依赖注入。
7. 加入 ARM64 编译脚本与 Makefile。
8. 编写 systemd 模板和安装脚本。
9. 增加单元测试及最小集成测试。
10. 在 OEC-Turbo 上进行第一轮启动验证。

V0.1 交付包含：完整可编译工程、数据库初始化迁移、配置样例、systemd 服务、ARM64 编译脚本、基础测试及 README。

## 15. 需在实机验证的事项

- 固件架构、Linux 内核版本及 exFAT 可用性。
- 相机在 Linux 上的 USB 接口类型、双存储卷识别与只读挂载。
- 是否需要在相机上确认 USB 传输模式。
- CIFS 3.1.1 兼容性、可靠写入和 NAS 持续吞吐。
- 设备上 SQLite 及 Go 程序的资源占用。
- NAS 端是否可以提供可信的服务端 SHA-256 计算接口。
- 81.1 GB 全量端到端备份是否满足 30 分钟目标。

---

**当前基线结论：** 使用 Go + SQLite + Linux CIFS + systemd 作为 OEC-Turbo 自动备份软件技术方案，先完成可运行的 V0.1 基座，再按阶段增加 USB 识别、SMB 传输、增量与断点恢复、Web 管理及性能优化。
