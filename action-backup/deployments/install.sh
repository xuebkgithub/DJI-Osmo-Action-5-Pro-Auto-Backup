#!/bin/bash
# OEC-Turbo 安装脚本。在解压后的安装包目录内以 root 执行。
# 只安装 production 二进制；校验 SHA256；创建用户与目录；注册 systemd；不自动启动主服务（更新定时任务会启用）。
# 自动更新（action-backup self-update --apply）验签通过后执行的也是这份脚本，所以它必须可重复执行、不依赖交互。
set -euo pipefail
cd "$(dirname "$0")"

[ "$(id -u)" -eq 0 ] || { echo "请以 root 执行"; exit 1; }
[ -f action-backup ] || { echo "缺少 action-backup 二进制"; exit 1; }
if ./action-backup --version 2>/dev/null | grep -q 'faultinject=true'; then
  echo "拒绝安装：这是测试构建（faultinject=true），请使用 make build-arm64 产物"; exit 1
fi
sha256sum -c SHA256SUMS

id -u actionbackup >/dev/null 2>&1 || useradd --system --home /var/lib/action-backup --shell /usr/sbin/nologin actionbackup
install -d -o actionbackup -g actionbackup -m 0750 /var/lib/action-backup
install -d -m 0755 /opt/action-backup /mnt/action-backup-nas
# 配置目录给 actionbackup 组写权限：PATCH /config 以临时文件 + rename 方式写回 config.yaml
install -d -o root -g actionbackup -m 0770 /etc/action-backup
install -m 0755 action-backup /opt/action-backup/action-backup
[ -f /etc/action-backup/config.yaml ] || install -m 0640 -o root -g actionbackup config.dryrun.yaml /etc/action-backup/config.yaml
install -m 0644 config.example.yaml /etc/action-backup/config.example.yaml
install -m 0644 action-backup.service /etc/systemd/system/action-backup.service
install -m 0644 action-backup-mounter.service /etc/systemd/system/action-backup-mounter.service
install -m 0644 action-backup-update.service /etc/systemd/system/action-backup-update.service
install -m 0644 action-backup-update.timer /etc/systemd/system/action-backup-update.timer
install -m 0644 99-action-backup.rules /etc/udev/rules.d/99-action-backup.rules
udevadm control --reload-rules || true
systemctl daemon-reload
# 更新定时任务：每分钟看一眼有没有安装请求，没有就什么都不做（单元带 ConditionPathExists）。
# 它是网页「立即更新」能生效的前提，所以安装时直接启用；主服务本身仍然不自动启动。
systemctl enable --now action-backup-update.timer

cat <<MSG
安装完成，尚未启动。
1. 编辑 /etc/action-backup/config.yaml（当前为 dry-run 模板），填写 SMB 共享与认证。
2. 创建 /etc/action-backup/smb.credentials（username=/password=），chmod 0600。
3. 校验：/opt/action-backup/action-backup --config /etc/action-backup/config.yaml --check
4. 首轮以 dry-run 启动：systemctl enable --now action-backup && journalctl -fu action-backup
   （action-backup.service Requires= 挂载守护 action-backup-mounter.service，会一并拉起）
MSG
