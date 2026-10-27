# CLAUDE.md

DJI Osmo Action 5 Pro → OEC-Turbo → Synology NAS 自动备份。Go 单体 + Vue 3 前端，mac 上开发，交叉编译到 ARM64 Linux，systemd 部署。

仓库根是 `dji_backup/`；产品代码在 `action-backup/`，任务层面的脚本在 `scripts/`，两者不互相依赖。

## 远程设备 SSH

设备可 SSH，**一律走封装脚本，不要手写 `ssh` / `sshpass`、不要在命令行里出现密码**：

```bash
~/MyMac/scripts/cmd_ip.sh <IP> '<命令>'
```

- 当前设备：`172.20.31.6`（OEC-Turbo，Armbian OS 25.05 bookworm，内核 6.1.99 aarch64，root 登录）。
- 脚本与配置在仓库之外：`~/MyMac/scripts/cmd_ip.sh`、`cmd_ip.conf`（格式 `IP;密码;端口`）。
- 不带命令参数即进入交互式登录——**在本会话中不要这样用**，会话无法交互；始终把命令作为第二个参数传入，脚本执行完即返回。
- 多条命令串在同一个参数里：`cmd_ip.sh 172.20.31.6 'cd /opt && ls -la'`。单引号包裹，避免本机 shell 抢先展开。
- `cmd_ip.sh list` 列出已配置主机，`cmd_ip.sh help` 看帮助。依赖 `sshpass`（`brew install sshpass`）。
- 默认用户 `root`，用 `SSH_USER=` 覆盖；配置路径用 `CONFIG_FILE=` 覆盖。
- **禁止**把密码写进命令、文档、提交信息或 `docs/evidence/`；需要引用口令时从 `cmd_ip.conf` 读取。
- 在设备上执行**写入或破坏性操作**（安装包、`systemctl restart/stop`、删除、改 `/etc`）前先向用户确认；只读探查（`df`、`ls`、`journalctl`、`systemctl status`、`lsusb`、`udevadm info`）可直接做。
- 设备现状：`systemctl` / `udevadm` 可用，`cifs` 与 `exfat` 内核模块可加载，`cifs-utils 2:7.0-2` 已装（2026-10-27）。
- 设备上 `deb.debian.org` 不可达，装包要用临时镜像源：`apt-get -o Dir::Etc::sourcelist=/dev/null -o Dir::Etc::sourceparts=/tmp/aptsrc ...`（`/etc/apt/` 未改动）。
- 部署用 `make deploy`（`action-backup/scripts/deploy.sh`）：打包 + scp + 远端 `install.sh`；`RESTART=1` 安装后重启（守护与主服务一起）。
- 设备上跑两个单元：`action-backup-mounter.service`（root 挂载守护，socket `/run/action-backup-mounter/helper.sock`，相机卷挂在 `/run/action-backup-mounter/source/sdX`）与 `action-backup.service`。守护单元**不能**加会创建私有挂载命名空间的沙箱选项。
- 设备当前 `mode: production`、`storage.type: cifs`（`//172.20.100.254/home`，凭据在 `/etc/action-backup/smb.credentials`，用户自己填的，不要读出来写进任何地方）、`auto_backup: true`。`/proc/mounts` 里 cifs 行含 SMB 用户名，放进 evidence 前必须脱敏。
- 相机当前接在设备上；模拟拔插用 `echo 0/1 > /sys/bus/usb/devices/6-1/authorized`（可逆）。

## 常用命令

在 `action-backup/` 下执行：

```bash
make fixtures          # 生成模拟相机卷
make dev               # simulation 模式启动，http://127.0.0.1:8080
make test              # 单元测试
make test-integration  # simulation 端到端
make test-recovery     # 故障注入 + 进程重启
make lint              # gofmt + vet + golangci-lint
make web-build         # 构建前端到 web/dist
make package           # linux-arm64 安装包
make tag V=0.6.0       # 打 tag 触发 GitHub Release
```

交付前至少跑 `make test` 与 `make lint`。更细的开发约定在 `action-backup/docs/development.md`，先读它再动代码。

## 代码约束

- 操作系统相关代码只放 `internal/platform`；其它包不得调用外部命令或使用 `syscall` 常量。
- 任何目标写入路径必须经 `storage.Target` 的方法，并在 `internal/storage/target_test.go` 补 dry-run 拒绝用例。
- 新增 API 路由必须加进 `internal/app/auth_test.go` 的 `protectedRoutes`（保证未登录 401 全覆盖）；免认证路径只能加到 `auth.DefaultPolicy()`。
- `PATCH /config` 只接受 `internal/config/patch.go` 白名单；新增项要同时实现 `Apply`、运行时应用（`internal/app/configstore.go`）与测试。
- `-tags faultinject` 只能出现在 `make dev`、`make test-integration`、`make build-arm64-test`；`make build-arm64` 与 `make package` 永远不带。
- 改前端后必须 `make web-build` 并把 `web/dist` 一起提交，CI 会校验产物与 `web/src` 一致，否则失败。

## 文档约定

- 架构文档按版本号另存（`docs/Action_Backup_软件架构设计_V1.x.md`），**不覆盖旧版**；当前基线 V1.4。
- 每次实机 / mac 验证，把命令原始输出放进 `docs/evidence/<UTC时间>-<主题>/`，结论写进 `docs/03-decisions.md`，架构文档里只引用不复制。
- 待确认问题记在 `docs/02-verification-checklist.md`，答案确认后搬进 `03-decisions.md`。
- 阶段进展更新仓库根 `README.md` 的「当前阶段」。
- 文档与提交信息用中文。

## 提交

- 未经用户明确要求不要 commit / push，也不要主动打 tag。
- `dist/`、`dev/`、`tests/fixtures/`、`web/node_modules/`、`*.db` 不入库；`web/dist/` 必须入库。
