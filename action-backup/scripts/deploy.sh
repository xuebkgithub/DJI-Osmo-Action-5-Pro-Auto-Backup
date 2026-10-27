#!/bin/bash
# 部署到 OEC-Turbo：打包 → scp → 远端解压 → install.sh → 可选重启服务。
#
# 远程命令一律走封装脚本 cmd_ip.sh；它不提供文件拷贝，所以只有 scp 这一步在本脚本内
# 直接调用 sshpass，密码从 cmd_ip.conf 读取并经 SSHPASS 环境变量传入，不出现在命令行。
#
# 用法：
#   bash scripts/deploy.sh                      # 打包并部署到默认主机，部署后不自动启动
#   HOST=172.20.31.6 bash scripts/deploy.sh     # 指定主机
#   PKG=dist/xxx.tar.gz bash scripts/deploy.sh  # 用已有安装包，跳过 make package
#   RESTART=1 bash scripts/deploy.sh            # 安装完成后 systemctl restart（服务已 enable 时）
set -euo pipefail
cd "$(dirname "$0")/.."

HOST="${HOST:-172.20.31.6}"
SSH_USER="${SSH_USER:-root}"
CMD_IP="${CMD_IP:-$HOME/MyMac/scripts/cmd_ip.sh}"
CONFIG_FILE="${CONFIG_FILE:-$(dirname "$CMD_IP")/cmd_ip.conf}"
REMOTE_DIR="${REMOTE_DIR:-/root/action-backup-install}"
RESTART="${RESTART:-0}"

[ -x "$CMD_IP" ] || { echo "找不到 cmd_ip.sh: $CMD_IP"; exit 1; }
command -v sshpass >/dev/null 2>&1 || { echo "未安装 sshpass：brew install sshpass"; exit 1; }

# 1. 安装包
if [ -z "${PKG:-}" ]; then
  VERSION="$(git describe --tags --always --dirty 2>/dev/null || echo 0.1.0-dev)"
  PKG="dist/action-backup-${VERSION}-linux-arm64.tar.gz"
  [ -f "$PKG" ] || make package
fi
[ -f "$PKG" ] || { echo "安装包不存在: $PKG"; exit 1; }
NAME="$(basename "$PKG")"
STEM="${NAME%.tar.gz}"
echo "安装包: $PKG"

# 2. 从 cmd_ip.conf 取端口与口令（口令只进环境变量）
PORT=22
PASS=""
while IFS=';' read -r ip password port extra || [ -n "$ip" ]; do
  ip="${ip%$'\r'}"; password="${password%$'\r'}"; port="${port%$'\r'}"
  [[ -z "$ip" || "$ip" == \#* ]] && continue
  if [ "$ip" = "$HOST" ]; then PASS="$password"; PORT="${port:-22}"; break; fi
done < "$CONFIG_FILE"
[ -n "$PASS" ] || { echo "$HOST 未在 $CONFIG_FILE 中配置"; exit 1; }

# 3. 上传
echo "上传到 $SSH_USER@$HOST:$REMOTE_DIR/"
"$CMD_IP" "$HOST" "mkdir -p '$REMOTE_DIR'" >/dev/null
SSHPASS="$PASS" sshpass -e scp -P "$PORT" \
  -o ConnectTimeout=10 -o StrictHostKeyChecking=accept-new \
  "$PKG" "$SSH_USER@$HOST:$REMOTE_DIR/$NAME"

# 4. 解压并安装
"$CMD_IP" "$HOST" "cd '$REMOTE_DIR' && rm -rf '$STEM' && tar -xzf '$NAME' && cd '$STEM' && bash install.sh"

# 5. 可选重启
if [ "$RESTART" = "1" ]; then
  "$CMD_IP" "$HOST" "systemctl restart action-backup && sleep 2 && systemctl --no-pager -l status action-backup | head -20"
else
  echo "未自动重启。服务已 enable 时用 RESTART=1 重新执行，或手动："
  echo "  $CMD_IP $HOST 'systemctl restart action-backup'"
fi
