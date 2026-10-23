#!/bin/bash
# 生成模拟相机卷，目录结构与 2026-10-23 实测一致：
#   <out>/internal/DCIM/DJI_003/DJI_YYYYMMDDhhmmss_NNNN_D.MP4 (+ .LRF)
#   <out>/sdcard/DCIM/DJI_003/...
#   另有 MISC/、LOST+FOUND/ 空目录
# 内容由 openssl CTR 流按 --seed 生成，可复现且不可压缩。
# 用法：
#   bash scripts/gen-fixtures.sh --out tests/fixtures/cam1 [--internal-files 1] [--sd-files 3] [--size 64M] [--seed 42] [--profile big]
set -euo pipefail

OUT=""
INTERNAL_FILES=1
SD_FILES=3
SIZE="64M"
SEED="42"
PROFILE=""
while [ $# -gt 0 ]; do
  case "$1" in
    --out) OUT="$2"; shift 2 ;;
    --internal-files) INTERNAL_FILES="$2"; shift 2 ;;
    --sd-files) SD_FILES="$2"; shift 2 ;;
    --size) SIZE="$2"; shift 2 ;;
    --seed) SEED="$2"; shift 2 ;;
    --profile) PROFILE="$2"; shift 2 ;;
    -h|--help) sed -n '2,10p' "$0"; exit 0 ;;
    *) echo "未知参数: $1"; exit 2 ;;
  esac
done
[ -n "$OUT" ] || { echo "需要 --out"; exit 2; }

to_bytes() {
  local v="$1" n unit
  n="${v%[KMGkmg]}"; unit="${v#"$n"}"
  case "$unit" in
    K|k) echo $((n * 1024)) ;;
    M|m) echo $((n * 1024 * 1024)) ;;
    G|g) echo $((n * 1024 * 1024 * 1024)) ;;
    "") echo "$n" ;;
    *) echo "无法解析大小: $v" >&2; exit 2 ;;
  esac
}

gen_file() { # path bytes seed-suffix
  local path="$1" bytes="$2" key="$3"
  # head 关闭管道后 openssl 收到 SIGPIPE 退出，这是预期行为，不能让 pipefail 把它当失败
  ( set +e +o pipefail; openssl enc -aes-256-ctr -pass "pass:${SEED}-${key}" -nosalt -pbkdf2 -iter 1000 < /dev/zero 2>/dev/null | head -c "$bytes" > "$path" )
  local got
  got=$(stat -f '%z' "$path" 2>/dev/null || stat -c '%s' "$path")
  [ "$got" -eq "$bytes" ] || { echo "生成 $path 失败：期望 $bytes 字节，得到 $got" >&2; exit 1; }
}

make_volume() { # dir count size_bytes [extra...]
  local dir="$1" count="$2" bytes="$3"; shift 3
  mkdir -p "$dir/DCIM/DJI_003" "$dir/MISC" "$dir/LOST+FOUND"
  local i ts name
  local base=20261023080000
  for ((i = 1; i <= count; i++)); do
    ts=$((base + i * 100))
    name="DJI_${ts}_$(printf '%04d' "$i")_D"
    gen_file "$dir/DCIM/DJI_003/$name.MP4" "$bytes" "$dir-$i-mp4"
    gen_file "$dir/DCIM/DJI_003/$name.LRF" $((bytes / 4 > 0 ? bytes / 4 : 1)) "$dir-$i-lrf"
    # 设置修改时间，秒级奇数，模拟实测
    touch -t "$(printf '%s' "$ts" | sed -E 's/^(..........)(..)(..)$/\1\2.\3/')" "$dir/DCIM/DJI_003/$name.MP4" "$dir/DCIM/DJI_003/$name.LRF"
  done
  while [ $# -gt 0 ]; do # extra: name=bytes
    name="${1%%=*}"; bytes="${1#*=}"
    gen_file "$dir/DCIM/DJI_003/$name.MP4" "$bytes" "$dir-$name"
    shift
  done
}

BYTES="$(to_bytes "$SIZE")"
rm -rf "$OUT"
mkdir -p "$OUT"
make_volume "$OUT/internal" "$INTERNAL_FILES" "$BYTES"
if [ "$PROFILE" = "big" ]; then
  make_volume "$OUT/sdcard" "$SD_FILES" "$BYTES" "DJI_20261023090000_0101_D=$(to_bytes 5G)" "DJI_20261023091000_0102_D=$(to_bytes 16G)"
else
  make_volume "$OUT/sdcard" "$SD_FILES" "$BYTES"
fi

# 清单
( cd "$OUT" && find . -type f -path '*/DCIM/*' | sort | xargs shasum -a 256 > manifest.sha256 )
{
  echo "["
  first=1
  while IFS= read -r f; do
    sz=$(stat -f '%z' "$OUT/$f" 2>/dev/null || stat -c '%s' "$OUT/$f")
    mt=$(stat -f '%m' "$OUT/$f" 2>/dev/null || stat -c '%Y' "$OUT/$f")
    [ $first -eq 1 ] || echo ","
    first=0
    printf '  {"path": "%s", "size": %s, "mtime_unix": %s}' "$f" "$sz" "$mt"
  done < <(cd "$OUT" && find . -type f -path '*/DCIM/*' | sed 's|^\./||' | sort)
  echo
  echo "]"
} > "$OUT/manifest.json"
echo "fixtures 已生成: $OUT"
du -sh "$OUT"
