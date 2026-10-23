#!/bin/bash
# probe-mac-env.sh — 只读探测 mac 开发环境、相机 exFAT 卷、NAS SMB 挂载。
# 输出到 docs/evidence/<UTC时间>-mac-probe/，不修改任何设备或文件。
#
# 用法：
#   bash scripts/probe-mac-env.sh
#   NAS_HOST=192.168.1.100 OEC_HOST=192.168.1.50 bash scripts/probe-mac-env.sh
#
# 建议运行前：相机已通过 USB 接入 mac；NAS 共享已在 Finder 中挂载。耗时约 1 到 3 分钟。
set -u
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
TS="$(date -u +%Y%m%dT%H%M%SZ)"
OUT="$ROOT/docs/evidence/${TS}-mac-probe"
mkdir -p "$OUT"
NAS_HOST="${NAS_HOST:-192.168.1.100}"
OEC_HOST="${OEC_HOST:-}"
SUMMARY="$OUT/summary.txt"

say() { printf '%s\n' "$*" | tee -a "$SUMMARY"; }
run() {   # run <name> <cmd...>   -> 输出写入 $OUT/<name>.txt
  local name="$1"; shift
  { printf '$ %s\n' "$*"; "$@"; printf '\n[exit=%s]\n' "$?"; } > "$OUT/$name.txt" 2>&1
  say "  - $name.txt  <= $*"
}
runsh() { # runsh <name> '<shell pipeline>'
  local name="$1"; shift
  { printf '$ %s\n' "$*"; bash -c "$*"; printf '\n[exit=%s]\n' "$?"; } > "$OUT/$name.txt" 2>&1
  say "  - $name.txt  <= $*"
}

say "# mac 环境探测  $TS"
say "输出目录: $OUT"
say ""

say "## 1. 工具链"
run 10-sw_vers sw_vers
run 11-uname uname -a
run 12-go-version go version
run 13-go-env go env GOOS GOARCH GOPATH GOMODCACHE GOFLAGS CGO_ENABLED
run 14-node-version node -v
run 15-pnpm-version pnpm -v
run 16-golangci-lint golangci-lint --version
runsh 17-brew-list 'brew list --versions go node pnpm golangci-lint 2>&1'

say "## 2. USB 设备（寻找 VID 0x0506 / PID 0x8002）"
run 20-ioreg-iousb ioreg -p IOUSB -w0 -l
runsh 21-usb-dji "ioreg -p IOUSB -w0 -l | grep -E '\\+-o |idVendor|idProduct|bcdDevice|USB Serial Number|USB Product Name|USB Vendor Name|Device Speed' | grep -B6 -A3 '\"idVendor\" = 1286'"
runsh 22-usb-sysprofiler "system_profiler SPUSBDataType 2>&1 | head -200"   # macOS 26 上曾返回空，仅作参考
runsh 23-ioreg-lun "ioreg -w0 -r -c IOSCSILogicalUnitNub -l"
runsh 24-ioreg-lun-brief "ioreg -w0 -r -c IOSCSILogicalUnitNub -l | grep -E 'IOSCSILogicalUnitNub|SCSI Logical Unit Number|Vendor Identification|Product Identification|Product Revision Level|BSD Name|Physical Interconnect'"

say "## 3. 磁盘与卷"
run 30-diskutil-list diskutil list
run 31-diskutil-external-plist diskutil list -plist external physical
run 32-mount mount
run 33-df df -h
runsh 34-ls-volumes 'ls -la /Volumes'

say "## 4. exFAT 卷（相机）"
EXFAT_VOLS=()
for v in /Volumes/*; do
  [ -d "$v" ] || continue
  info="$(diskutil info "$v" 2>/dev/null)" || continue
  if printf '%s' "$info" | grep -q -i 'File System Personality: *ExFAT'; then
    EXFAT_VOLS+=("$v")
  fi
done
if [ "${#EXFAT_VOLS[@]}" -eq 0 ]; then
  say "  (没有发现 exFAT 卷。相机未接入，或 macOS 尚未挂载。)"
fi
i=0
for v in ${EXFAT_VOLS[@]+"${EXFAT_VOLS[@]}"}; do
  i=$((i+1)); n="4${i}"
  base="$(basename "$v")"
  say "  卷 $i: $v"
  run "${n}0-vol-${i}-diskutil-info" diskutil info "$v"
  run "${n}1-vol-${i}-diskutil-info-plist" diskutil info -plist "$v"
  run "${n}2-vol-${i}-mdutil" mdutil -s "$v"
  runsh "${n}3-vol-${i}-ls-root" "ls -la '$v'"
  runsh "${n}4-vol-${i}-ls-dcim" "ls -laR '$v/DCIM' 2>&1 | head -300"
  runsh "${n}5-vol-${i}-ext-count" "find '$v/DCIM' -type f 2>/dev/null | sed -E 's/.*\.//' | tr 'a-z' 'A-Z' | sort | uniq -c | sort -rn"
  runsh "${n}6-vol-${i}-du" "du -sh '$v/DCIM' 2>&1"
  runsh "${n}7-vol-${i}-mac-pollution" "find '$v' \( -name '._*' -o -name '.DS_Store' -o -name '.Spotlight-V100' -o -name '.fseventsd' -o -name '.Trashes' \) 2>/dev/null | head -50"
  runsh "${n}8-vol-${i}-mp4-stat" "find '$v/DCIM' -type f -iname '*.MP4' 2>/dev/null | head -10 | while IFS= read -r f; do stat -f '%z  %Sm  %N' -t '%Y-%m-%dT%H:%M:%S%z' \"\$f\"; done"
  runsh "${n}9-vol-${i}-largest" "find '$v/DCIM' -type f 2>/dev/null -exec stat -f '%z %N' {} + | sort -rn | head -5"
done

say "## 5. NAS / SMB（NAS_HOST=${NAS_HOST}）"
run 50-smb-statshares smbutil statshares -a
runsh 51-mount-smbfs "mount -t smbfs"
run 52-ping-nas ping -c 3 -t 5 "$NAS_HOST"
run 53-nc-445 nc -z -v -w 3 "$NAS_HOST" 445
run 54-nc-5001 nc -z -v -w 3 "$NAS_HOST" 5001
if ! mount -t smbfs 2>/dev/null | grep -q .; then
  say "  (未发现 smbfs 挂载。请先在 Finder 中「前往 > 连接服务器」smb://${NAS_HOST} 挂载共享，再重跑。)"
fi
for m in $(mount -t smbfs 2>/dev/null | awk '{print $3}'); do
  run "55-smb-diskutil-info-$(basename "$m")" diskutil info "$m"
  runsh "56-smb-df-$(basename "$m")" "df -h '$m'"
done

say "## 6. OEC-Turbo（OEC_HOST=${OEC_HOST:-未设置}）"
if [ -n "$OEC_HOST" ]; then
  run 60-ping-oec ping -c 3 -t 5 "$OEC_HOST"
  run 61-nc-22 nc -z -v -w 3 "$OEC_HOST" 22
else
  say "  (未设置 OEC_HOST，跳过。设备有 IP 后用 OEC_HOST=<ip> 重跑。)"
fi

say ""
say "## 关键摘要"
say "- DJI USB 设备数: $(grep -c '"idVendor" = 1286' "$OUT/20-ioreg-iousb.txt" 2>/dev/null)  产品名: $(grep -o '"USB Product Name" = "[^"]*"' "$OUT/20-ioreg-iousb.txt" 2>/dev/null | grep -i osmo | head -1)"
say "- exFAT 卷: ${EXFAT_VOLS[*]:-无}"
say "- SMB 挂载: $(mount -t smbfs 2>/dev/null | awk '{print $1" -> "$3}' | tr '\n' ';')"
say "- SMB 协商版本: $(grep -E 'SMB_VERSION' "$OUT/50-smb-statshares.txt" 2>/dev/null | awk '{print $2}' | sort -u | tr '\n' ' ')"
say "- Go: $(go version 2>/dev/null)"
say "- Node: $(node -v 2>/dev/null)  pnpm: $(pnpm -v 2>/dev/null)  golangci-lint: $(command -v golangci-lint >/dev/null && echo 已安装 || echo 未安装)"
say ""
say "完成。把以下目录交给 Claude 核对："
say "$OUT"
