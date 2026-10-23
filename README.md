# DJI Osmo Action 5 Pro 自动备份（OEC-Turbo）

相机通过 USB 接入网心云 OEC-Turbo，自动把 `DCIM` 下的视频增量备份到 Synology NAS。
软件为 Go 单体应用，macOS 上开发，交叉编译到 ARM64 Linux，systemd 部署。

## 目录

```text
dji_backup/
├── README.md                      # 本文件：入口、目录说明、当前阶段
├── docs/
│   ├── Action_Backup_软件架构设计_V1.0.md   # 初版架构
│   ├── Action_Backup_软件架构设计_V1.1.md   # mac 开发环境 + 测试模式
│   ├── Action_Backup_软件架构设计_V1.2.md   # 落实决策 3 至 7
│   ├── Action_Backup_软件架构设计_V1.3.md   # 当前基线：回写 V0.1 至 V0.4 实现偏差
│   ├── 02-verification-checklist.md         # 待确认问题 → 对应命令 → 核对方法
│   ├── 03-decisions.md                      # 已确认的决策与依据（随问题回答更新）
│   └── evidence/                            # 命令原始输出，按 <UTC时间>-<主题>/ 分目录
├── scripts/
│   └── probe-mac-env.sh           # 只读探测 mac 环境、相机卷、NAS；结果写入 docs/evidence/
├── action-backup/                 # Go 工程（V0.1 已交付，见其 README.md；尚未 git init）
└── dist/                          # 交叉编译产物与安装包，不入库
```

约定：

- 架构文档按版本号另存，不覆盖旧版。
- 每次实机或 mac 上的验证都把命令输出放进 `docs/evidence/`，结论写入 `03-decisions.md`，文档里只引用不复制。
- `action-backup/` 内是产品代码，`scripts/` 内是任务层面的辅助脚本，两者不互相依赖。

## 当前阶段

- 架构基线：V1.3（`docs/Action_Backup_软件架构设计_V1.3.md`），已与 V0.4 代码一致。
- V0.1 基座已于 2026-10-23 完成：mac 上 `make dev` 以 simulation 启动，单元 / 集成测试、lint 全绿，ARM64 安装包可产出。
- V0.2 已于 2026-10-23 完成：扫描器、Sync Engine、计划 API、darwin 真相机后端；mac 接真相机 dry-run 实测通过，证据在 `docs/evidence/20261023T0249Z-mac-dryrun-real-camera/`。
- V0.3 已于 2026-10-23 完成：流式传输、`.part`、断点、basic / sha256 校验、单 Worker 调度、任务 API；5 GB 模拟备份含中断续传实测通过，证据在 `docs/evidence/20261023T0305Z-sim-5gb-sha256-resume/`。真 NAS 上传因 NAS 不可达未做。
- V0.4 已于 2026-10-23 完成：故障注入规则引擎、自动重试与重连、冲突策略、恢复测试框架（11 个场景含进程崩溃重启）全绿，证据在 `docs/evidence/20261023T0330Z-recovery-suite/`。
- 待办：NAS 可达后在 Finder 挂载共享并执行 `smbutil statshares -a`，确认 SMB 版本（问题 2）。
- 下一步：V0.5 Web UI、用户名密码认证、SSE（任务清单见 V1.3 §16）。NAS 可达后补做 premounted 真实上传。

## 环境探测

```bash
# 相机接入 mac、NAS 在 Finder 中挂载后执行：
bash scripts/probe-mac-env.sh
# 设备有 IP 后：
OEC_HOST=192.168.1.x bash scripts/probe-mac-env.sh
```

脚本只执行读取类命令，输出目录路径会打印在最后一行。
