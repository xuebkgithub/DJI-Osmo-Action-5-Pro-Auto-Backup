# DJI Osmo Action 5 Pro 自动备份（OEC-Turbo）

相机通过 USB 接入网心云 OEC-Turbo，自动把 `DCIM` 下的视频增量备份到 Synology NAS。
软件为 Go 单体应用，macOS 上开发，交叉编译到 ARM64 Linux，systemd 部署。

## 目录

```text
dji_backup/
├── README.md                      # 本文件：入口、目录说明、当前阶段
├── .github/workflows/             # CI（构建与测试）与 Release（打 tag 自动发布安装包）
├── docs/
│   ├── Action_Backup_软件架构设计_V1.0.md   # 初版架构
│   ├── Action_Backup_软件架构设计_V1.1.md   # mac 开发环境 + 测试模式
│   ├── Action_Backup_软件架构设计_V1.2.md   # 落实决策 3 至 7
│   ├── Action_Backup_软件架构设计_V1.3.md   # 回写 V0.1 至 V0.4 实现偏差
│   ├── Action_Backup_软件架构设计_V1.4.md   # 当前基线：回写 V0.5 Web UI / 认证 / SSE
│   ├── 02-verification-checklist.md         # 待确认问题 → 对应命令 → 核对方法
│   ├── 03-decisions.md                      # 已确认的决策与依据（随问题回答更新）
│   └── evidence/                            # 命令原始输出，按 <UTC时间>-<主题>/ 分目录
├── scripts/
│   └── probe-mac-env.sh           # 只读探测 mac 环境、相机卷、NAS；结果写入 docs/evidence/
├── action-backup/                 # Go 工程 + Vue 前端（V0.5 已交付，见其 README.md）
└── dist/                          # 交叉编译产物与安装包，不入库
```

约定：

- 架构文档按版本号另存，不覆盖旧版。
- 每次实机或 mac 上的验证都把命令输出放进 `docs/evidence/`，结论写入 `03-decisions.md`，文档里只引用不复制。
- `action-backup/` 内是产品代码，`scripts/` 内是任务层面的辅助脚本，两者不互相依赖。

## 当前阶段

- 架构基线：V1.4（`docs/Action_Backup_软件架构设计_V1.4.md`），已与 V0.5 代码一致。
- V0.1 基座已于 2026-10-23 完成：mac 上 `make dev` 以 simulation 启动，单元 / 集成测试、lint 全绿，ARM64 安装包可产出。
- V0.2 已于 2026-10-23 完成：扫描器、Sync Engine、计划 API、darwin 真相机后端；mac 接真相机 dry-run 实测通过，证据在 `docs/evidence/20261023T0249Z-mac-dryrun-real-camera/`。
- V0.3 已于 2026-10-23 完成：流式传输、`.part`、断点、basic / sha256 校验、单 Worker 调度、任务 API；5 GB 模拟备份含中断续传实测通过，证据在 `docs/evidence/20261023T0305Z-sim-5gb-sha256-resume/`。真 NAS 上传因 NAS 不可达未做。
- V0.4 已于 2026-10-23 完成：故障注入规则引擎、自动重试与重连、冲突策略、恢复测试框架（11 个场景含进程崩溃重启）全绿，证据在 `docs/evidence/20261023T0330Z-recovery-suite/`。
- V0.5 已于 2026-10-23 完成：`action-backup passwd`、用户名密码会话认证与 CSRF、登录限速、SSE、`/config` 在线修改、`/storage/test`、Vue 3 Web UI（含模式横幅与测试面板）；未登录 401 全路由测试、curl 实测与 Chrome 逐页核对（登录、SSE 实时进度、暂停 / 恢复 / 取消、设置写回、存储测试、测试面板、退出）通过，证据在 `docs/evidence/20261023T0400Z-v05-auth-sse-smoke/`。
- 发布流程已于 2026-10-23 接入 GitHub Actions：push 与 PR 跑 CI，打 `v*` tag 自动交叉编译 linux / darwin × arm64 / amd64 四个平台、打包并发布到 Releases（`make tag V=0.6.0`），细节见 `action-backup/README.md`。
- 设备 SSH 已于 2026-10-27 打通：`172.20.31.6`，Armbian OS 25.05 (bookworm)、内核 6.1.99 aarch64、root 登录，systemd / udev / cifs 内核模块就绪；连接方式见下文「远程设备 SSH」。
- 设备已于 2026-10-27 完成环境初始化与首次部署：装 `cifs-utils 2:7.0-2`（`mount.cifs` 7.0，`cifs` 模块可加载），
  `v0.6.0-1-g6ff44ad` production 包经 `scripts/deploy.sh` 安装，systemd `action-backup.service` 已 enable 并运行在
  dry-run + 认证开启状态，`http://172.20.31.6:8080` 可登录。证据 `docs/evidence/20261027T0320Z-device-deploy-smb/`。
  同时修掉两个部署缺陷：`/run` 目录重启丢失（单元补 `RuntimeDirectory`）、`PATCH /config` 在 `ProtectSystem=strict`
  下写回失败（单元补 `ReadWritePaths=/etc/action-backup`，配置目录改 `root:actionbackup 0770`）。
- 待办：NAS 可达后在 Finder 挂载共享并执行 `smbutil statshares -a`，确认 SMB 版本（问题 2）；
  设备上的 `storage.share` 目前仍是占位 `//192.168.1.100/VideoBackup`，`smb.credentials` 未创建。
- 待办：设备 apt 源 `deb.debian.org` 在本网络不可达，本次用 `/tmp` 临时镜像源装的包；是否永久换成 USTC 镜像待定。
- V0.6 Linux 后端已于 2026-10-27 实机验证通过：sysfs + udev 数据库检测相机（LUN0 内置 / LUN1 microSD、序列号 `0A1B2C3D`）、
  root 挂载守护 `action-backup-mounter` 经 unix socket 只读挂载两卷、拔插闭环、production 模式真实复制一个 409 KB 文件到设备本地目标，SHA-256 一致。
  证据 `docs/evidence/20261027T0530Z-v06-linux-backend/`。新增 `storage.allow_local_destination` 开关（NAS 不可达时本地目标）。
- 真 NAS 已于 2026-10-27 接通：`//172.20.100.254/home` 经守护挂载（SMB 3.1.1），2 个文件备份后 SHA-256 一致，吞吐 24.1 MB/s，证据 `docs/evidence/20261027T0620Z-v06-nas-cifs-backup/`。
- 设备现状：`mode: production`、`storage.type: cifs`、`auto_backup: true`（经 `PATCH /config` 打开），插入相机即自动备份到 NAS。
- 待办：目标 mtime 是否保留（问题 8）；架构文档 V1.5 回写 V0.6。
- 下一步：性能测试——首测 24.1 MB/s 折算 81.1 GB 约 56 分钟，未达 30 分钟目标，需定位瓶颈并调 `buffer_size_mb` / Worker 数；systemd 资源占用记录（V1.4 §17）。

## 环境探测

```bash
# 相机接入 mac、NAS 在 Finder 中挂载后执行：
bash scripts/probe-mac-env.sh
# 设备有 IP 后：
OEC_HOST=192.168.1.x bash scripts/probe-mac-env.sh
```

脚本只执行读取类命令，输出目录路径会打印在最后一行。

## 远程设备 SSH（OEC-Turbo）

设备已可 SSH。统一走封装脚本 `cmd_ip.sh`，它从配置文件读取密码与端口，用 `sshpass` 免交互登录，不需要在命令里写密码。

```bash
# 脚本与配置（在仓库之外，机器本地）
~/MyMac/scripts/cmd_ip.sh        # 脚本
~/MyMac/scripts/cmd_ip.conf      # 配置，格式 IP;密码;端口

# 用法
cmd_ip.sh <IP>              # 交互式登录
cmd_ip.sh <IP> '<命令>'     # 执行远程命令后返回
cmd_ip.sh list              # 列出已配置的 IP 与端口
cmd_ip.sh help              # 帮助
```

示例：

```bash
~/MyMac/scripts/cmd_ip.sh 172.20.31.6 'df -h'
~/MyMac/scripts/cmd_ip.sh 172.20.31.6 'systemctl status action-backup'
~/MyMac/scripts/cmd_ip.sh 172.20.31.6 'journalctl -u action-backup -n 100 --no-pager'
```

说明：

- 依赖 `sshpass`（`brew install sshpass`）；默认用户 `root`，可用 `SSH_USER=xxx` 覆盖，配置文件路径可用 `CONFIG_FILE=` 覆盖。
- 多条命令用 `&&` 或 `;` 串在一个参数里：`cmd_ip.sh 172.20.31.6 'cd /opt && ls -la'`。
- 密码只存在于 `cmd_ip.conf`，不要写进命令行、文档、提交或 `docs/evidence/` 的输出里。
- 当前设备：`172.20.31.6`，Armbian OS 25.05 (bookworm)、内核 6.1.99 aarch64、root 登录、systemd 与 udev 可用。
