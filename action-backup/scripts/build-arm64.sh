#!/bin/bash
# 交叉编译 ARM64 Linux production 二进制。等价于 make build-arm64。
set -euo pipefail
cd "$(dirname "$0")/.."
exec make build-arm64
