#!/bin/bash
# 实机性能测试：删掉 NAS 上某个已备份文件的副本，触发一次备份，用 journald 的纳秒时间戳算吞吐，
# 同时采样服务进程 RSS / CPU 与系统负载。结果打印到 stdout，调用方重定向进 docs/evidence/。
#
# 用法：
#   LOGIN_JSON=/path/login.json bash scripts/test-performance.sh [--host 172.20.31.6] [--dest sdcard/DCIM/DJI_003/X.MP4]
#     LOGIN_JSON：{"username":"admin","password":"..."}，口令只走文件，不进命令行
#     --dest：目标相对 destination_root 的路径（默认取 NAS 上最大的那个已备份文件）
#     --label：写进输出头部的说明，如 "buffer=4 checkpoint=512/30 pipeline"
# 远程命令走 cmd_ip.sh；API 走 curl。
set -euo pipefail
HOST=172.20.31.6
DEST=""
LABEL=""
while [ $# -gt 0 ]; do
  case "$1" in
    --host) HOST="$2"; shift 2 ;;
    --dest) DEST="$2"; shift 2 ;;
    --label) LABEL="$2"; shift 2 ;;
    *) echo "未知参数 $1"; exit 2 ;;
  esac
done
CMD_IP="${CMD_IP:-$HOME/MyMac/scripts/cmd_ip.sh}"
LOGIN_JSON="${LOGIN_JSON:?需要 LOGIN_JSON=<登录 JSON 文件>}"
B="http://$HOST:8080"
CJ="$(mktemp)"; trap 'rm -f "$CJ"' EXIT
# 设备 sshd 偶发拒绝密码（连续快速连接时），重试 3 次
remote() {
  local out
  for _ in 1 2 3; do
    if out="$("$CMD_IP" "$HOST" "$1" 2>&1)"; then printf '%s\n' "$out" | sed '1,/^-----/d'; return 0; fi
    sleep 2
  done
  echo "远程命令失败: $1" >&2; return 1
}

MP="$(remote 'grep -E "^  mount_point:" /etc/action-backup/config.yaml | sed -E "s/.*: *\"([^\"]*)\".*/\1/"')"
ROOT="$(remote 'grep -E "^  destination_root:" /etc/action-backup/config.yaml | sed -E "s/.*: *\"([^\"]*)\".*/\1/"')"
if [ -z "$DEST" ]; then
  DEST="$(remote "cd '$MP/$ROOT' && find . -type f -name '*.MP4' -printf '%s %p\n' | sort -n | tail -1 | cut -d' ' -f2- | sed 's#^\./##'")"
fi
[ -n "$DEST" ] || { echo "NAS 上没有已备份的 MP4，先完成一次备份"; exit 1; }

echo "### 性能测试 $(date -u +%Y-%m-%dT%H:%M:%SZ)  host=$HOST  dest=$DEST  ${LABEL:+label=$LABEL}"
echo "### 服务配置（backup 段）"
remote 'sed -n "/^backup:/,/^storage:/p" /etc/action-backup/config.yaml | grep -E "worker_count|buffer_size_mb|checkpoint|verify_mode"'
echo "### 版本"; remote '/opt/action-backup/action-backup --version'
echo "### 删除 NAS 副本"; remote "rm -f '$MP/$ROOT/$DEST' && echo removed"

curl -s -c "$CJ" -H 'Content-Type: application/json' --data @"$LOGIN_JSON" "$B/api/v1/auth/login" >/dev/null
CSRF="$(curl -s -b "$CJ" "$B/api/v1/auth/me" | sed -n 's/.*"csrf_token":"\([^"]*\)".*/\1/p')"
START_TS="$(remote 'date +%H:%M:%S')"
echo "### POST /backups/start @ $START_TS"
curl -s -X POST -b "$CJ" -H "X-CSRF-Token: $CSRF" "$B/api/v1/backups/start" | sed -E 's/"items":\[[^]]*\]/"items":[...]/' | head -c 400; echo

echo "### 采样（每 2 秒：时间 状态计数 RSS(KB) %CPU loadavg）"
for _ in $(seq 1 600); do
  st="$(curl -s -b "$CJ" "$B/api/v1/system/status" | python3 -c 'import json,sys; t=json.load(sys.stdin)["data"]["tasks"]; print(t["QUEUED"]+t["TRANSFERRING"]+t["VERIFYING"], t["TRANSFERRING"])')"
  active="${st%% *}"
  remote 'printf "%s rss_kb/cpu=%s load=%s\n" "$(date +%H:%M:%S)" "$(ps -o rss=,pcpu= -C action-backup | head -1 | tr -s " " | sed "s/^ //")" "$(cut -d" " -f1-3 /proc/loadavg)"' | sed "s/^/  /;s/$/  active=$active/"
  [ "$active" = "0" ] && break
  sleep 2
done

echo "### journald 时间戳与吞吐"
remote "journalctl -u action-backup --no-pager -o cat --since '$START_TS' | grep -E 'transferring|completed|FAILED|INTERRUPTED|WARN|ERROR' | sed 's/.*\"time\":\"\([^\"]*\)\".*\"msg\":\"\([^\"]*\)\".*/\1 \2/'" | tee /tmp/perf-journal.$$
python3 - "/tmp/perf-journal.$$" <<'PY'
import re, sys
from datetime import datetime
start = end = size = None
for line in open(sys.argv[1], encoding="utf-8"):
    m = re.match(r'(\S+) transferring .*\((\d+) bytes\)', line)
    if m:
        start, size = m.group(1), int(m.group(2))
    m = re.match(r'(\S+) completed ', line)
    if m:
        end = m.group(1)
def parse(ts):
    ts = re.sub(r'(\.\d{6})\d+', r'\1', ts)
    return datetime.fromisoformat(ts)
if start and end and size:
    secs = (parse(end) - parse(start)).total_seconds()
    print(f"### 结果：{size} B / {secs:.2f} s = {size/secs/1e6:.1f} MB/s")
else:
    print("### 结果：未找到完整的 transferring/completed 记录")
PY
rm -f /tmp/perf-journal.$$
echo "### 校验（源 vs NAS SHA-256）"
remote "SRC=\$(ls /run/action-backup-mounter/source/*/\$(echo '$DEST' | cut -d/ -f2-) 2>/dev/null | head -1); sha256sum \"\$SRC\" '$MP/$ROOT/$DEST' | cut -c1-16,64-"
