package linux

import (
	"bufio"
	"os"
	"strings"
)

// DefaultMountInfo 当前进程视角的挂载表。
const DefaultMountInfo = "/proc/self/mountinfo"

// mountEntryIn 在挂载表（通常 /proc/self/mountinfo）中找精确等于 path 的挂载点。
// 同一路径可能叠多层挂载（如 systemd ReadWritePaths 先把本地目录绑定进沙箱，CIFS 随后传播进来叠在上面），
// mountinfo 按挂载先后列出，最后一条才是进程实际看到的栈顶，必须与 statfs 的结果一致。
func mountEntryIn(mountinfo, path string) (source, fstype string, found bool) {
	f, err := os.Open(mountinfo)
	if err != nil {
		return "", "", false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		// 36 35 98:0 /mnt1 /mnt2 rw,noatime master:1 - ext3 /dev/root rw
		sep := -1
		for i, f := range fields {
			if f == "-" {
				sep = i
				break
			}
		}
		if sep < 0 || len(fields) < sep+3 || len(fields) < 5 {
			continue
		}
		if unescape(fields[4]) == path {
			source, fstype, found = unescape(fields[sep+2]), fields[sep+1], true
		}
	}
	return source, fstype, found
}

func unescape(s string) string {
	r := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	return r.Replace(s)
}
