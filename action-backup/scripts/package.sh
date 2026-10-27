#!/bin/bash
# 打包：dist/action-backup-<version>-<target>.tar.gz
# TARGET=linux-arm64（默认） | linux-amd64 | darwin-arm64 | darwin-amd64
#   linux  包：production 二进制、配置样例、systemd 单元、udev 规则、mount helper 说明、install.sh、SHA256SUMS
#   darwin 包：production 二进制、配置样例、README-macos.md、SHA256SUMS（mac 上供开发与 dry-run，无 systemd）
set -euo pipefail
cd "$(dirname "$0")/.."
VERSION="${VERSION:-0.1.0-dev}"
TARGET="${TARGET:-linux-arm64}"
OS="${TARGET%%-*}"
BIN="dist/action-backup-${TARGET}"
[ -x "$BIN" ] || { echo "缺少 $BIN，先执行 make build-${TARGET}"; exit 1; }
NAME="action-backup-${VERSION}-${TARGET}"
STAGE="dist/${NAME}"
rm -rf "$STAGE"
mkdir -p "$STAGE"
cp "$BIN" "$STAGE/action-backup"
chmod +x "$STAGE/action-backup"

case "$OS" in
  linux)
    cp configs/config.example.yaml configs/config.dryrun.yaml "$STAGE/"
    cp deployments/action-backup.service deployments/action-backup-mounter.service deployments/99-action-backup.rules deployments/install.sh "$STAGE/"
    cp -R deployments/mount-helper "$STAGE/mount-helper"
    chmod +x "$STAGE/install.sh"
    FILES=(action-backup config.example.yaml config.dryrun.yaml action-backup.service action-backup-mounter.service 99-action-backup.rules install.sh)
    ;;
  darwin)
    cp configs/config.example.yaml configs/config.mac-dryrun.yaml "$STAGE/"
    cp deployments/README-macos.md "$STAGE/"
    FILES=(action-backup config.example.yaml config.mac-dryrun.yaml README-macos.md)
    ;;
  *)
    echo "未知 TARGET: $TARGET（可用 linux-arm64 linux-amd64 darwin-arm64 darwin-amd64）"; exit 2 ;;
esac

# macOS 用 shasum，Linux(CI) 用 sha256sum；两者输出格式与 install.sh 的 sha256sum -c 兼容。
if command -v shasum >/dev/null 2>&1; then SHA256=(shasum -a 256); else SHA256=(sha256sum); fi
( cd "$STAGE" && "${SHA256[@]}" "${FILES[@]}" > SHA256SUMS )
tar -C dist -czf "dist/${NAME}.tar.gz" "$NAME"
rm -rf "$STAGE"
echo "打包完成: dist/${NAME}.tar.gz"
tar -tzf "dist/${NAME}.tar.gz"
