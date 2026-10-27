# 验证清单：第 18 章待确认问题

2026-10-23 状态：1、3 至 7 已确认（见 `03-decisions.md`）。仅剩 2：NAS 本次不可达，需在 Finder 中连接 `smb://192.168.1.100` 挂载共享后执行 `smbutil statshares -a`，或重跑探测脚本。

分工：你在 mac 上执行命令（Claude Code 中输入 `! <命令>` 可直接把输出带进会话），Claude 读取输出并把结论写入 `03-decisions.md`。
没有命令的条目是决策题，直接回答即可。

一键方式：相机接入、NAS 在 Finder 中挂载后运行

```bash
bash scripts/probe-mac-env.sh
```

它覆盖下表所有"有命令"的条目，输出在 `docs/evidence/<时间>-mac-probe/`。下表给出单条命令，便于单独重跑。

| # | 问题 | 命令（mac，只读） | Claude 核对什么 |
| --- | --- | --- | --- |
| 1 | 相机两个卷在 `/Volumes` 下的名称 | `ls -la /Volumes` 和 `diskutil list external physical` | 两个 exFAT 卷的卷名是否相同；是否能用卷名区分 internal / sdcard |
| 1a | 两卷与 USB LUN 的对应（Linux 已确认，2026-10-27：`1:0:0:0`→`sda` 内置 48 GB，`1:0:0:1`→`sdb` microSD 116 GB，与 mac 一致） | `ioreg -w0 -r -c IOSCSILogicalUnitNub -l \| grep -E 'Logical Unit Number\|Product Identification\|BSD Name'` | LUN 0 / LUN 1 分别对应哪个 BSD 设备，再对照 `diskutil info /Volumes/<卷>` 的容量 |
| 1b | 真实文件命名与时间戳 | `ls -laR /Volumes/<卷>/DCIM \| head -100` 和 `stat -f '%z %Sm %N' -t '%Y-%m-%dT%H:%M:%S%z' /Volumes/<卷>/DCIM/*/*.MP4 \| head` | 目录层级、文件名模式、是否有 LRF；mtime 秒级是否为偶数（exFAT 2 秒精度的旁证） |
| 1c | mac 是否已在相机卷上写入元数据 | `find /Volumes/<卷> -name '._*' -o -name '.DS_Store' \| head` | 是否存在污染文件，决定扫描器忽略规则是否已必要 |
| 2 | NAS 型号与 SMB 版本（已确认，2026-10-27：设备 `mount.cifs` 对 `//172.20.100.254/home` 协商到 SMB 3.1.1，证据 `docs/evidence/20261027T0620Z-v06-nas-cifs-backup/`） | `smbutil statshares -a`（需先在 Finder 挂载共享） | `SMB_VERSION` 协商结果、`SERVER_NAME`；型号已从同级目录 `DSM_api` 推断为 Synology DSM 7.1 @ 192.168.1.100，请确认 |
| 2a | NAS 445 端口可达 | `nc -z -v -w 3 192.168.1.100 445` | 连通性 |
| 3 | 工具链版本 | 已确认 | Go 1.25.5、Node 25.5.0、pnpm 10.33.0、golangci-lint 2.15.0 |
| 4 | OEC-Turbo 固件状态与 SSH 时间（已确认，2026-10-27：Armbian 25.05 bookworm、内核 6.1.99 aarch64、systemd 252、exfat 内核模块可加载（exfat.ko，未默认加载）、cifs 模块就绪、`cifs-utils 7.0` 已装） | 设备有 IP 后：`OEC_HOST=<ip> bash scripts/probe-mac-env.sh`；设备上（若有串口或本地终端）：`uname -a; cat /proc/filesystems \| grep -i exfat; modinfo cifs \| head -3; systemctl --version \| head -1` | 内核版本、exfat 是否内核原生、cifs 模块、systemd 版本 |
| 5 | mac 只读重挂载 | 已确认 | 不需要 |
| 6 | NAS 目标目录分层 | 已确认 | 默认布局 + `destination_layout` 模板自定义 |
| 7 | Web 认证方式 | 已确认 | 用户名密码 |
| 8 | 目标文件是否要保留源 mtime | 实机：目标 mtime 为写入时间（2026-10-27 实测，`docs/evidence/20261027T0530Z-v06-linux-backend/04-copy-verify.txt`） | 若要保留，在 `backup` 包落盘后 `Chtimes`；exFAT 2 秒精度与 NAS 端时区需一并确认 |
| 9 | NAS 可达后 `mount-target` 实机验证（已确认，2026-10-27：挂载成功、2 文件 SHA-256 一致、24.1 MB/s） | 填好 `storage.share` 与 `smb.credentials` 后 `systemctl restart action-backup`，看 `storage.mounted` 事件与 `journalctl -u action-backup-mounter` | 守护代码已就绪但未实测；SMB 版本结论同问题 2 |

## 核对方法

Claude 收到输出后：

1. 把原始输出目录路径记入 `03-decisions.md` 对应条目的"依据"。
2. 给出结论，并指出影响 V1.1 哪一章节。
3. 全部 7 条确认后发布 V1.2。
