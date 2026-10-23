#!/bin/bash
# 打包安装包：dist/action-backup-<version>-linux-arm64.tar.gz
# 内含 production 二进制、配置样例、systemd 单元、udev 规则、mount helper 说明、install.sh、SHA256SUMS。
set -euo pipefail
cd "$(dirname "$0")/.."
VERSION="${VERSION:-0.1.0-dev}"
BIN="dist/action-backup-linux-arm64"
[ -x "$BIN" ] || { echo "缺少 $BIN，先执行 make build-arm64"; exit 1; }
NAME="action-backup-${VERSION}-linux-arm64"
STAGE="dist/${NAME}"
rm -rf "$STAGE"
mkdir -p "$STAGE"
cp "$BIN" "$STAGE/action-backup"
cp configs/config.example.yaml configs/config.dryrun.yaml "$STAGE/"
cp deployments/action-backup.service deployments/99-action-backup.rules deployments/install.sh "$STAGE/"
cp -R deployments/mount-helper "$STAGE/mount-helper"
chmod +x "$STAGE/install.sh" "$STAGE/action-backup"
( cd "$STAGE" && shasum -a 256 action-backup config.example.yaml config.dryrun.yaml action-backup.service 99-action-backup.rules install.sh > SHA256SUMS )
tar -C dist -czf "dist/${NAME}.tar.gz" "$NAME"
rm -rf "$STAGE"
echo "打包完成: dist/${NAME}.tar.gz"
tar -tzf "dist/${NAME}.tar.gz"
