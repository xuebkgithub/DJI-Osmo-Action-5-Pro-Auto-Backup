# DJI Osmo Action 5 Pro 自动备份系统

**软件架构设计方案 V1.1**

| 项目 | 内容 |
| --- | --- |
| 目标硬件 | 网心云 OEC-Turbo |
| 操作系统 | 已选定的 ARM64 Linux 固件 |
| 开发机 | macOS（Apple Silicon 或 Intel），交叉编译 ARM64 |
| 开发语言 | Go |
| 运行模式 | `production`、`dry-run`、`simulation` |
| SMB 传输 | Linux CIFS 挂载，优先 SMB3 |
| 数据库 | SQLite（`modernc.org/sqlite`，纯 Go） |
| 校验策略 | 默认基础校验，可选 SHA-256 |
| 部署方式 | systemd 原生部署，当前阶段手工拷贝安装包 |
| 管理方式 | Web UI + REST API + SSE |
| 备份来源 | DJI Osmo Action 5 Pro 内部存储及 microSD |
| 备份目标 | SMB NAS |
| 性能目标 | 81.1 GB 全量备份在 30 分钟内完成，需实机验证 |

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

## 2. 技术栈

| 功能 | 技术 | 说明 |
| --- | --- | --- |
| 核心应用 | Go 1.23+ | 单体模块化，`CGO_ENABLED=0` 交叉编译 ARM64 |
| USB 检测（Linux） | udev、sysfs | 监听事件并支持主动枚举 |
| USB 检测（macOS） | `diskutil`、`/Volumes` 轮询 | 仅开发用，见第 11 章 |
| 存储卷访问 | Linux exFAT / macOS exFAT | 源存储卷必须只读挂载或只读打开 |
| 文件扫描 | `os`、`filepath` | 递归扫描 DCIM |
| 增量备份 | Go Sync Engine | 任务规划、变化识别、冲突处理 |
| 数据传输 | Go 流式 I/O | 固定缓冲区，直接写入目标挂载目录 |
| SMB（Linux） | Linux CIFS 客户端 | SMB2/SMB3，优先 SMB3 |
| SMB（macOS 开发） | Finder / `mount_smbfs` 预挂载 | 程序只验证挂载类型，不负责挂载 |
| 元数据 | SQLite | `modernc.org/sqlite`，纯 Go，无 CGO |
| REST API | Go `net/http` | 轻量 HTTP 服务 |
| Web UI | Vue 3 + Vite，Node 20 LTS | 构建后用 `embed` 嵌入 Go 程序 |
| 实时状态 | SSE | 推送聚合后的进度与事件 |
| 后台服务 | systemd | 开机自启、自动重启 |
| 日志 | slog / journald | 结构化日志，mac 上输出到 stdout |
| 故障注入 | `internal/faultinject` | 仅 `-tags faultinject` 构建时编译 |
| 测试 | `go test`、`testing/fstest`、子进程恢复测试 | 见第 12 章 |

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

```text
action-backup/
├── cmd/
│   └── action-backup/
│       └── main.go
├── internal/
│   ├── config/
│   │   ├── config.go
│   │   ├── validate.go
│   │   └── modes.go              # 运行模式与配置组合的合法性校验
│   ├── runmode/
│   │   ├── mode.go               # production / dry-run / simulation
│   │   └── guard.go              # 写入门禁：dry-run 禁止任何目标写入
│   ├── platform/
│   │   ├── platform.go           # DeviceDetector / VolumeMounter / StorageProbe 接口
│   │   ├── select.go             # 按 GOOS 与模式选择后端
│   │   ├── linux/
│   │   │   ├── udev.go
│   │   │   ├── sysfs.go
│   │   │   ├── mount.go          # 调用受限 root helper
│   │   │   └── probe.go          # statfs + /proc/self/mountinfo
│   │   ├── darwin/
│   │   │   ├── diskutil.go       # diskutil list/info -plist
│   │   │   ├── usb.go            # system_profiler SPUSBDataType -json，带缓存
│   │   │   ├── volumes.go        # /Volumes 轮询
│   │   │   └── probe.go          # statfs f_fstypename == "smbfs"
│   │   └── sim/
│   │       ├── devices.go        # 配置定义的目录型设备与卷
│   │       ├── control.go        # attach / detach 控制
│   │       └── probe.go          # 允许本地目录作为目标
│   ├── device/
│   │   ├── identity.go
│   │   └── manager.go
│   ├── volume/
│   │   ├── classify.go           # internal / sdcard 判定
│   │   └── manager.go
│   ├── scanner/
│   │   ├── scanner.go
│   │   ├── filters.go            # 扩展名、系统元数据文件忽略
│   │   └── metadata.go
│   ├── syncengine/
│   │   ├── comparator.go
│   │   ├── planner.go
│   │   ├── plan.go               # Plan 结构，dry-run 与 API 预览共用
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
│   │   ├── target.go             # 目标后端：cifs / premounted / local
│   │   ├── health.go
│   │   └── filesystem.go
│   ├── verify/
│   │   ├── basic.go
│   │   └── sha256.go
│   ├── faultinject/
│   │   ├── injector.go           # //go:build faultinject
│   │   ├── points.go             # 注入点枚举
│   │   ├── rules.go              # 规则、触发条件、动作
│   │   ├── api.go                # /api/v1/test/faults
│   │   └── noop.go               # //go:build !faultinject
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
│       ├── plan.go
│       └── errors.go
├── web/
│   ├── src/
│   ├── public/
│   ├── vite.config.ts            # dev 时代理 /api 到 Go 服务
│   └── package.json
├── configs/
│   ├── config.example.yaml       # 实机 production
│   ├── config.dryrun.yaml        # 实机 dry-run
│   └── config.dev.yaml           # mac simulation
├── deployments/
│   ├── action-backup.service
│   ├── 99-action-backup.rules
│   ├── mount-helper/             # 受限 root helper
│   └── install.sh
├── scripts/
│   ├── build-arm64.sh
│   ├── package.sh                # 产出 tar.gz 安装包
│   ├── gen-fixtures.sh           # 生成模拟相机卷
│   ├── mac-mount-ro.sh           # mac 上把相机卷重新只读挂载
│   └── test-performance.sh
├── tests/
│   ├── integration/              # simulation 模式端到端
│   ├── recovery/                 # 故障注入 + 进程重启
│   ├── fixtures/                 # 生成产物，.gitignore
│   └── manifests/                # 小型固定 fixture 清单，纳入版本管理
├── docs/
│   ├── architecture.md
│   ├── api.md
│   ├── database.md
│   ├── development.md            # mac 开发指南
│   └── testing.md                # 测试模式与故障注入
├── go.mod
├── go.sum
├── Makefile
└── README.md
```

## 5. 模块职责与接口

| 模块 | 主要职责 | 建议接口 |
| --- | --- | --- |
| Platform Backend | 操作系统相关的设备发现、挂载、挂载点校验 | `DeviceDetector`、`VolumeMounter`、`StorageProbe` |
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

核心接口草案：

```go
// internal/platform
type DeviceDetector interface {
    Discover(ctx context.Context) ([]RawDevice, error)
    Watch(ctx context.Context) (<-chan RawDeviceEvent, error)
}

type VolumeMounter interface {
    MountReadOnly(ctx context.Context, volume Volume) (string, error)
    Unmount(ctx context.Context, volume Volume) error
}

// 确认 path 确实是期望的目标文件系统，而不是底层本地目录
type StorageProbe interface {
    Probe(ctx context.Context, path string, expect MountExpectation) (MountInfo, error)
}

// internal/runmode
type Guard interface {
    Mode() Mode
    AllowDestinationWrite() bool   // dry-run 返回 false
    TestEndpointsEnabled() bool    // 仅非 production 且 faultinject 构建
}

// internal/faultinject
type Injector interface {
    Hit(ctx context.Context, point Point, meta HitMeta) error  // 无规则命中返回 nil
}

// 业务接口与 V1.0 相同
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
    Plan(ctx context.Context, files []SourceFile) (Plan, error)
}

type TransferEngine interface {
    Transfer(ctx context.Context, task BackupTask) error
}

type Verifier interface {
    Verify(ctx context.Context, task BackupTask) (VerifyResult, error)
}
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

CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY,
    applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

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
- **修改时间在参与 `fingerprint` 前归一化到 2 秒精度并统一为 UTC**。exFAT 的时间戳精度为 10 ms 并带时区偏移字段，Linux exfat 驱动与 macOS 的解释可能不一致，归一化后可避免同一文件在不同主机上产生不同指纹。
- 一个数据库文件只属于一台主机。mac 开发库与实机库不互相拷贝。
- `COMPLETED` 代表正式目标文件已成功确认。增量跳过前仍需确认 NAS 目标存在且必要元数据匹配。
- SQLite 保存在 OEC-Turbo 本地持久化文件系统，不放在 NAS 或 `/run`。mac 上默认在工程目录 `./dev/backup-dev.db`。
- dry-run 模式只读打开数据库，不插入任务和断点，只允许写 `events`。
- 数据库迁移使用 `schema_migrations` 表做明确的版本管理。
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

不得只根据 `.part` 大小认定数据正确。

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
- 断点恢复时必须正确重建哈希状态。
- CIFS 目标重新读取会额外消耗网络时间和 NAS I/O。

simulation 模式下 fixtures 自带 `manifest.sha256`，集成测试用它核对最终归档文件，与程序自身的校验结果互相印证。

## 9. REST API

统一前缀：`/api/v1`。

| HTTP | 路径 | 作用 | 可用模式 |
| --- | --- | --- | --- |
| GET | `/system/status` | 系统状态与统计 | 全部 |
| GET | `/system/health` | USB、NAS、数据库健康情况 | 全部 |
| GET | `/system/mode` | 当前运行模式、平台后端、是否测试构建 | 全部 |
| GET | `/devices` | 设备列表 | 全部 |
| GET | `/devices/{id}/volumes` | 存储卷列表 | 全部 |
| POST | `/devices/rescan` | 重新扫描设备 | 全部 |
| POST | `/backups/plan` | 扫描并生成备份计划，不写入 | 全部 |
| GET | `/backups/plan` | 最近一次计划 | 全部 |
| GET | `/tasks` | 任务列表、分页、过滤 | 全部 |
| GET | `/tasks/{id}` | 任务详情 | 全部 |
| POST | `/backups/start` | 开始备份 | production、simulation |
| POST | `/backups/pause` | 暂停备份 | production、simulation |
| POST | `/backups/resume` | 恢复备份 | production、simulation |
| POST | `/tasks/{id}/retry` | 重试失败任务 | production、simulation |
| POST | `/tasks/{id}/cancel` | 取消任务 | production、simulation |
| GET | `/backups/history` | 历史备份 | 全部 |
| GET | `/config` | 脱敏配置 | 全部 |
| PATCH | `/config` | 修改白名单配置项 | 全部 |
| POST | `/storage/test` | 测试目标存储连接 | 全部 |
| GET | `/events` | 历史事件 | 全部 |
| GET | `/events/stream` | SSE 实时进度 | 全部 |
| GET | `/test/faults` | 当前故障规则 | 测试构建且非 production |
| POST | `/test/faults` | 新增故障规则 | 同上 |
| DELETE | `/test/faults/{id}` | 删除故障规则 | 同上 |
| POST | `/test/sim/devices/{key}/attach` | 模拟插入设备 | simulation |
| POST | `/test/sim/devices/{key}/detach` | 模拟拔出设备 | simulation |
| POST | `/test/reset` | 清空测试数据库和目标目录 | simulation 且目标为 local |

在 dry-run 模式调用 `/backups/start` 返回 `409`，错误码 `MODE_READ_ONLY`。`/test/*` 在 production 构建中不注册路由，返回 `404`。

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

接口需有认证、输入校验、请求超时、分页限制、跨站请求防护和日志脱敏。进度推送建议按约 1 秒聚合。Web UI 在非 production 模式下顶部显示醒目的模式横幅。

## 10. 配置文件

### 10.1 实机 production：`/etc/action-backup/config.yaml`

```yaml
mode: "production"            # production | dry-run | simulation

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
  ignore_patterns:              # 默认值，通常不需改
    - "._*"
    - ".DS_Store"
    - ".Spotlight-V100"
    - ".fseventsd"
    - ".Trashes"
    - "System Volume Information"
  worker_count: 1
  buffer_size_mb: 4
  retry_count: 3
  checkpoint_interval_sec: 5
  checkpoint_bytes_mb: 64
  verify_mode: "basic"
  delete_source: false

storage:
  type: "cifs"                  # cifs | premounted | local
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

`storage.type` 含义：

| 取值 | 行为 | 允许的模式 |
| --- | --- | --- |
| `cifs` | 程序通过 root helper 挂载 CIFS，并用 `statfs` 确认 `CIFS_SUPER_MAGIC` | production、dry-run（Linux） |
| `premounted` | 操作系统已挂载，程序只用 `StorageProbe` 确认该路径是 `cifs` 或 `smbfs` 且来源匹配 `share` | 全部 |
| `local` | 普通本地目录 | 仅 simulation，且必须显式 `allow_local_destination: true` |

### 10.2 实机 dry-run：`configs/config.dryrun.yaml`

与 production 相同，仅 `mode: "dry-run"`。此模式下 `device.auto_backup` 触发的动作是生成计划而非上传，目标目录不会出现任何文件。

### 10.3 mac simulation：`configs/config.dev.yaml`

```yaml
mode: "simulation"

server:
  listen: "127.0.0.1:8080"
  auth_enabled: false

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
    remount_readonly: false     # true 时调用 scripts/mac-mount-ro.sh，需要确认
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

faults:
  enabled: true
  rules: []                     # 也可运行时通过 /api/v1/test/faults 添加

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
- SMB 用户名与密码存放在独立凭据文件中，权限限制为 `0600`，不能在 Web API 和日志中直接暴露。

## 11. macOS 开发环境

### 11.1 工具链

| 工具 | 版本 | 用途 |
| --- | --- | --- |
| Go | 1.23 或更高 | 主程序，`brew install go` |
| Node.js | 20 LTS | Web UI 构建 |
| pnpm | 9 | Web 依赖管理 |
| golangci-lint | 最新 | 静态检查 |
| diskutil、system_profiler | macOS 自带 | darwin 平台后端 |
| `mount_smbfs` / Finder | macOS 自带 | 预挂载 NAS |

不需要 Docker、虚拟机或 CGO。`modernc.org/sqlite` 是纯 Go，`CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build` 可以直接在 mac 上产出实机二进制。

### 11.2 Makefile 目标

```makefile
tools              # 检查 go / node / pnpm 版本
fixtures           # scripts/gen-fixtures.sh 生成模拟相机卷
dev                # go run -tags faultinject ./cmd/action-backup --config configs/config.dev.yaml
web-dev            # pnpm --dir web dev，Vite 代理 /api 到 127.0.0.1:8080
web-build          # 构建 web/dist，供 go:embed
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
| linux | production / dry-run | udev + sysfs | root helper | statfs CIFS magic + mountinfo |
| linux | simulation | sim | sim（目录即挂载点） | sim（本地目录）或 linux probe |
| darwin | dry-run | diskutil | darwin（只读重新挂载可选） | darwin smbfs probe |
| darwin | simulation | sim、diskutil 或两者 | 同上 | sim 或 darwin probe |
| darwin | production | 拒绝启动 | | |

darwin 后端细节：

- **设备发现**：每 `poll_interval_sec` 执行一次 `diskutil list -plist`，对新出现的外置卷执行 `diskutil info -plist <dev>` 取 `VolumeUUID`、`VolumeName`、`FilesystemType`、`MediaName`、`BusProtocol`。USB VID/PID 通过 `system_profiler SPUSBDataType -json` 获取，该命令耗时 1 到 2 秒，结果缓存直到卷列表变化。
- **卷分类**：优先用 USB 逻辑单元顺序（LUN 0 视为 internal，LUN 1 视为 sdcard），其次按容量区间（50 GB 级为 internal），最后按卷名。规则与 linux 后端共用 `internal/volume/classify.go`。
- **只读保护**：macOS 默认以读写方式自动挂载 exFAT。程序自身只用 `O_RDONLY` 打开源文件且从不创建文件。若 `remount_readonly: true`，调用 `scripts/mac-mount-ro.sh` 执行 `diskutil unmount` 再 `diskutil mount readOnly`，脚本执行前要求用户确认。
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
- `runmode.Guard.AllowDestinationWrite()` 返回 false，`storage` 包在任何创建、写入、重命名、删除入口处先检查该门禁，违反时 panic 并记录，这是最后一道防线。
- 数据库只读打开，只允许追加 `events`。
- `/backups/start` 等写操作接口返回 `409 MODE_READ_ONLY`。

实机上线顺序：先用 `config.dryrun.yaml` 跑一次，核对计划与预期一致，再切换 `mode: production`。

### 12.2 simulation

目的：在 mac 上不依赖 OEC-Turbo 跑通包括断点、恢复和校验在内的完整流程。

模拟设备：

- 配置中每个 `fixtures.devices[]` 对应一台虚拟相机，每个卷对应一个目录，目录内结构与真相机一致：`DCIM/100MEDIA/DJI_YYYYMMDDhhmmss_NNNN_D.MP4`，可选 `.LRF`。
- attach / detach 通过 API 或 UI 切换。detach 时正在传输的任务会收到源读取错误，用于验证 `INTERRUPTED` 流程。
- `device_key` 由 `serial` 生成，与 linux 后端算法一致，保证增量逻辑同样生效。

fixtures 生成器 `scripts/gen-fixtures.sh`：

```text
--out tests/fixtures/cam1
--internal-files 3 --sd-files 5     # 文件数量
--size 64M                           # 单文件大小，默认小文件
--profile big                        # 预设：含 1 个 5 GB 文件，用于大文件测试
--seed 42                            # 可复现
```

文件内容来自 `/dev/urandom`，不可压缩，接近真实视频。生成时同步产出 `manifest.sha256` 和 `manifest.json`（路径、大小、mtime）。`tests/manifests/` 保存小型固定清单纳入版本管理，`tests/fixtures/` 为生成产物不入库。

模拟目标：`storage.type: local` 时目标是本地目录，`.part`、重命名、fsync 的语义与真实文件系统一致，足以验证流程；吞吐数据无意义。

### 12.3 测试分层

| 层级 | 位置 | 运行方式 | 覆盖 |
| --- | --- | --- | --- |
| 单元 | 各包 `*_test.go` | `make test`，mac | 指纹、分类、比较器、状态机转移、配置校验、路径白名单 |
| 集成 | `tests/integration` | `make test-integration`，simulation + local | 首次备份、重复接入跳过、文件变化产生新版本、SHA-256 与 manifest 一致 |
| 恢复 | `tests/recovery` | `make test-recovery`，子进程方式启动二进制 | 故障注入后重启进程，断点续传正确，不重复上传，不产生残留 `.part` |
| 实机 dry-run | OEC-Turbo | 手工 | 真设备识别、双卷、NAS probe |
| 实机真备份 | OEC-Turbo | 手工 + `scripts/test-performance.sh` | 81.1 GB 耗时、CPU、内存、断网拔线 |

恢复测试框架：测试用例以子进程启动 `action-backup`，注入 `process.crash` 规则，等待进程退出后重新启动，通过 API 轮询任务终态并比对目标文件哈希。每个用例独立的临时数据库和目标目录。

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

`trigger` 支持 `once`、`nth_call`、`after_bytes`、`probability`、`times`；`action.kind` 支持 `error`、`delay`、`stall`、`throttle`、`short_write`、`corrupt`、`disconnect`、`mismatch`、`crash`。规则命中时发布 `fault.hit` 事件并写入 `events` 表，便于测试断言和事后排查。

运行时接口 `/api/v1/test/faults` 允许在不重启的情况下增删规则；production 构建不注册该路由。

### 12.5 必测场景与模式对应

| 场景 | 验证方式 |
| --- | --- |
| 首次插入相机，自动发现并备份 | simulation 集成测试；实机 |
| 再次插入相机，跳过已确认完成的文件 | simulation 集成测试；实机 |
| USB 设备路径或枚举顺序变化，仍能识别存储卷 | 单元测试（分类规则）；实机 |
| 上传中断网、拔出相机、NAS 服务重启或设备重启 | 故障注入 `dest.write`、`device.detach`、`dest.probe`、`process.crash` |
| NAS 容量不足和 SMB 凭据错误 | 故障注入 `ENOSPC`；实机错误凭据 |
| 临时文件数据损坏或无法验证断点 | 故障注入 `part.corrupt` |
| 同名文件被修改或存储卡更换 | simulation：修改 fixture 后重新 attach |
| NAS 已经断开但挂载目录仍存在，必须拒绝错误写入 | 单元测试 probe；mac 上 Finder 断开 NAS 后实测；实机 |
| Web 未授权请求、路径遍历及恶意配置值 | 单元测试 + 集成测试 |
| 大文件连续传输、长期运行时内存和 CPU 负载 | fixtures `--profile big`；实机 |
| 81.1 GB 全量传输的端到端真实耗时 | 仅实机 |
| dry-run 模式下目标目录零写入 | 集成测试：目标目录 inotify/fsevents 或前后快照比对 |
| 测试构建不能被安装为正式服务 | install.sh 单元测试 |

## 13. systemd 原生部署

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

### 13.1 当前阶段的部署方式

OEC-Turbo 暂不能从 mac SSH 访问，部署流程分两步：

1. mac 上 `make package`，产出 `dist/action-backup-<version>-linux-arm64.tar.gz`，内含二进制、`config.example.yaml`、`config.dryrun.yaml`、service 文件、udev 规则、mount helper、`install.sh` 和 `SHA256SUMS`。
2. 手工拷贝到设备后执行 `install.sh`，脚本核对 SHA256、创建用户和目录、安装文件、`systemctl daemon-reload`，但**不自动启动**，首次由人工用 dry-run 配置启动。

`make deploy` 在设备可 SSH 后实现，内容为 scp 安装包、远程执行 install.sh、重启服务并拉取健康检查。

## 14. 开发阶段与验收

| 阶段 | 开发内容 | 验收标准 |
| --- | --- | --- |
| V0.1 | Go 工程、配置与模式校验、SQLite 迁移、日志、HTTP、运行模式框架、平台抽象接口、sim 后端、fixtures 脚本、Makefile、systemd 模板、打包脚本 | mac 上 `make dev` 以 simulation 启动，健康 API 和 `/system/mode` 正常；`make build-arm64 package` 产出安装包 |
| V0.2 | 扫描器、卷分类、Sync Engine、Plan、dry-run 门禁、darwin 后端 | mac 接真相机能列出双卷全部 MP4 并输出计划；fixtures 下增量判断单元测试通过 |
| V0.3 | 流式传输、`.part`、基础校验、断点、`local` 与 `premounted` 目标 | simulation 下 5 GB fixture 完成归档；mac 连真 NAS 完成一次真实上传 |
| V0.4 | 状态机、恢复、重试、故障注入、恢复测试框架 | 12.4 全部注入点有对应用例，`make test-recovery` 全绿 |
| V0.5 | Web UI、认证、SSE、SHA-256 可选校验、模式横幅、测试控制面板 | 浏览器可以管理任务、查看计划、添加故障规则 |
| V0.6 | linux 后端（udev、mount helper、CIFS）、实机 dry-run、实机真备份、性能测试、发布 | 实机 dry-run 计划正确；以 81.1 GB / 30 分钟为目标提交真实结果 |

linux 后端代码可以在 V0.2 之后随时编写，但只能在设备可用后验证，因此验收放在 V0.6。设备提前到手则提前。

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
10. 禁止用字符串拼接构建任意 shell 命令。调用 `diskutil`、`system_profiler`、mount helper 一律用 `exec.Command` 传参数数组。
11. 设备及存储卷身份不能只依赖 `/dev/sdX` 或 `/dev/diskN`。
12. API 输入、文件路径和特权操作必须做白名单校验。
13. 密码、Token、SMB 凭据不得输出到日志或未经授权的 API。
14. 提供单元、集成、异常恢复及性能测试。
15. 首版使用一个 Worker，基于实测决定后续并发和缓冲区参数。
16. 操作系统相关代码只能出现在 `internal/platform`，其余包不得 import `syscall` 平台常量或调用外部命令。
17. 所有目标写入必须经过 `runmode.Guard` 检查；新增写入路径必须同时新增故障注入点和对应测试。
18. 故障注入代码只在 `-tags faultinject` 下编译，production 构建中 `/test/*` 路由不存在。
19. 配置校验必须拒绝危险组合：production + local 目标、simulation + 非 `-TEST` 的真 NAS 目录、测试构建安装为正式服务。
20. 构建与测试必须在 mac 上不依赖设备即可完成，`make test test-integration test-recovery` 是合并前的最低要求。

## 16. 下一步开发任务

**立即进入 V0.1**，按以下顺序实施：

1. 初始化 `go.mod` 和工程目录，确认 `modernc.org/sqlite` 在 `CGO_ENABLED=0` 下交叉编译通过。
2. 实现 YAML 配置解析、运行模式解析和配置组合校验。
3. 建立 SQLite 连接、`schema_migrations` 和 Repository 骨架。
4. 建立结构化日志及事件模型。
5. 定义 `internal/platform` 接口与 `sim` 后端，darwin 和 linux 后端先放桩实现。
6. 实现 `runmode.Guard` 并在 `storage` 包写入入口接入。
7. 实现 `/api/v1/system/health`、`/system/status`、`/system/mode`。
8. 实现配置和模块依赖注入。
9. 编写 `scripts/gen-fixtures.sh` 和 `config.dev.yaml`。
10. 加入 Makefile：`dev`、`test`、`build-arm64`、`build-arm64-test`、`package`。
11. 编写 systemd 模板、udev 规则和 install.sh。
12. 增加单元测试及最小集成测试，`make dev` 在 mac 上以 simulation 启动验证。

V0.1 交付包含：完整可编译工程、数据库初始化迁移、三份配置样例、systemd 服务、ARM64 安装包、fixtures 生成脚本、基础测试及 README。

## 17. 需在实机验证的事项

- 固件架构、Linux 内核版本及 exFAT 可用性（内核 exfat 还是 exfat-fuse）。
- 相机在 Linux 上的 USB 接口类型、双存储卷识别与只读挂载；两卷对应的 LUN 编号。
- 是否需要在相机上确认 USB 传输模式。
- CIFS 3.1.1 兼容性、可靠写入和 NAS 持续吞吐。
- 设备上 SQLite 及 Go 程序的资源占用。
- NAS 端是否可以提供可信的服务端 SHA-256 计算接口。
- 81.1 GB 全量端到端备份是否满足 30 分钟目标。
- systemd 版本是否支持模板中的沙箱选项。
- exFAT 时间戳在 Linux 上的时区解释与 mac 是否一致，用于核对 2 秒归一化是否足够。

## 18. 待确认问题

以下问题的答案会直接影响实现细节，请逐条确认：

1. **卷名**：相机接 mac 时，`/Volumes` 下两个卷显示的名称分别是什么？若两卷同名，卷分类将只能依赖 LUN 顺序和容量。
2. **NAS 型号**：NAS 品牌与型号、SMB 服务端版本。影响 `smb_version` 默认值和是否有服务端哈希接口。
3. **工具链版本**：Go、Node、pnpm 是否有版本限制？本文档假定 Go 1.23+、Node 20 LTS、pnpm 9。
4. **固件状态**：OEC-Turbo 固件是否已刷好并确认带 `cifs` 内核模块和 exFAT 支持？预计何时可以 SSH？
5. **mac 只读保护**：开发时是否接受程序调用 `diskutil` 把相机卷重新只读挂载（需要确认提示），还是只依赖程序自身不写源卷？
6. **备份目标目录结构**：`destination_root` 之下是否需要再按设备序列号或日期分层，例如 `DJI-Action5Pro/<serial>/<volume>/DCIM/...`？V1.0 未明确，当前默认 `<destination_root>/<volume_type>/DCIM/...`。
7. **Web 认证方式**：首版用单一静态 Token 还是用户名密码？影响 V0.5 和配置白名单。

---

**当前基线结论：** 使用 Go + SQLite + Linux CIFS + systemd 作为 OEC-Turbo 自动备份软件技术方案。开发在 macOS 上进行，通过平台抽象层和 simulation 模式先把全流程、断点恢复和故障场景在本地跑通，再用 dry-run 在实机零风险预演，最后切换 production 并进行性能验收。
