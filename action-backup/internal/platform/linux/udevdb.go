// Package linux 提供 Linux 平台实现：sysfs / udev 数据库枚举相机、经 root 挂载守护挂载源卷与 CIFS、statfs 探测目标。
package linux

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DefaultUdevData udev 数据库目录，文件名 b<major>:<minor>，世界可读，由 60-persistent-storage.rules 的 blkid 填充。
const DefaultUdevData = "/run/udev/data"

// udevProps 读取一个块设备的 udev 属性（E: 行）。文件不存在时返回 os.ErrNotExist，表示 udev 尚未处理完。
func udevProps(dataDir, devNumber string) (map[string]string, error) {
	f, err := os.Open(filepath.Join(dataDir, "b"+devNumber))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	props := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "E:") {
			continue
		}
		k, v, ok := strings.Cut(line[2:], "=")
		if !ok {
			continue
		}
		props[k] = v
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read udev data b%s: %w", devNumber, err)
	}
	return props, nil
}
