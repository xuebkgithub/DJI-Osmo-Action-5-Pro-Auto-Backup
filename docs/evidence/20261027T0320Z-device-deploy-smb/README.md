# OEC-Turbo 环境初始化与首次部署（2026-10-27 03:04–03:21 UTC）

设备 `172.20.31.6`（Armbian OS 25.05 bookworm，内核 6.1.99 aarch64）。本次做三件事：
装 SMB 支持、部署当前可用版本、建立 systemd 服务。全部命令经 `cmd_ip.sh` 执行。

构建版本：`v0.6.0-1-g6ff44ad-dirty`（HEAD 之后改了 `deployments/` 与 `scripts/deploy.sh`，
故带 `-dirty`；提交后重新 `make package` 可得干净版本号）。production 构建，`faultinject=false`。

| 文件 | 内容 |
| --- | --- |
| `01-cifs-utils.txt` | `mount.cifs` 版本、dpkg 状态、`cifs` 模块、apt 源现状 |
| `02-install.txt` | 安装产物、属主权限、`--version`、`--check`、systemd 单元与 `systemd-analyze verify` |
| `03-systemd-status.txt` | `systemctl status` 与启动日志 |
| `04-http-smoke.txt` | mac 上 curl 实测：Web UI、未登录 401、免认证 health、登录、状态、`PATCH /config` 写回、退出 |

## 1. SMB 支持

`deb.debian.org` 在本网络不可达（IPv6 无路由，IPv4 超时），`apt-get install cifs-utils` 失败：

```text
W: Failed to fetch http://deb.debian.org/debian/dists/bookworm/InRelease  Cannot initiate the connection to debian.map.fastlydns.net:80 (2a04:4e42:8c::644). - connect (101: Network is unreachable) Could not connect to debian.map.fastlydns.net:80 (146.75.114.132), connection timed out
E: Package 'cifs-utils' has no installation candidate
```

镜像可达性实测（`curl` 取 `dists/bookworm/Release`）：

```text
mirrors.aliyun.com                  200 1.276578s
mirrors.tuna.tsinghua.edu.cn        200 1.190245s
mirrors.ustc.edu.cn                 200 0.132739s
deb.debian.org                      000 8.001376s
```

改用 USTC 镜像，但**没有改 `/etc/apt/sources.list.d/debian.sources`**，只在 `/tmp/aptsrc/ustc.sources`
放了临时源，用 `apt-get -o Dir::Etc::sourcelist=/dev/null -o Dir::Etc::sourceparts=/tmp/aptsrc` 安装：

```text
Fetched 58.7 MB in 9s (6329 kB/s)
Unpacking libwbclient0:arm64 (2:4.17.12+dfsg-0+deb12u4) ...
Unpacking cifs-utils (2:7.0-2) ...
Setting up libwbclient0:arm64 (2:4.17.12+dfsg-0+deb12u4) ...
Setting up libtalloc2:arm64 (2.4.0-f2) ...
Setting up cifs-utils (2:7.0-2) ...
update-alternatives: using /usr/lib/aarch64-linux-gnu/cifs-utils/idmapwb.so to provide /etc/cifs-utils/idmap-plugin (idmap-plugin) in auto mode
```

结果：`/usr/sbin/mount.cifs` 版本 7.0，`cifs` 模块可 `modprobe` 并已加载。

## 2. 部署

mac 上 `make lint`（0 issues）、`make test`（全绿）后 `make package` 出 linux-arm64 production 包，
`scripts/deploy.sh` 上传并在设备上执行 `install.sh`。SHA256 全部 OK，安装不自动启动。

## 3. systemd

`systemctl enable --now action-backup` 后 active (running)。启动日志两条 WARN 均为预期：

- `storage target not ready` —— `storage.share` 还是占位 `//192.168.1.100/VideoBackup`，NAS 未配置；
- `device detector not available on this platform yet` —— Linux 设备检测属 V0.6，尚未实现。

## 4. 本次发现并修掉的两个部署缺陷

1. `/run/action-backup` 由 `install.sh` 创建，但 `/run` 是 tmpfs，重启即丢，单元里没有重建逻辑。
   单元补 `RuntimeDirectory=action-backup` / `RuntimeDirectoryMode=0750` 与
   `ExecStartPre=/bin/mkdir -p /run/action-backup/source`。
2. `PATCH /api/v1/config`（Web UI 设置页写回）在设备上必然失败：

   ```text
   {"error":{"code":"CONFIG_WRITE_FAILED","message":"write config: open /etc/action-backup/.config-101801972.yaml: read-only file system","retryable":true}}
   ```

   `ProtectSystem=strict` 让 `/etc` 对服务只读，且 `/etc/action-backup` 目录 `actionbackup` 无写权限
   （写回是临时文件 + rename，需要目录写权限）。单元补 `ReadWritePaths=/etc/action-backup`，
   `install.sh` 把配置目录改为 `root:actionbackup 0770`。重新部署后写回成功（见 `04-http-smoke.txt`）。

## 5. 当前配置

`/etc/action-backup/config.yaml` 为 dry-run 模板，已开启认证（`action-backup passwd --stdin`，
用户名 `admin`，口令在设备上用 `/dev/urandom` 生成后一次性交给用户，不入库、不进本目录）。
`storage.share` 仍是占位值，`smb.credentials` 未创建。
