//go:build linux

// Package linux 提供 Linux 平台实现。
package linux

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/unix"

	"action-backup/internal/platform/fsinfo"
)

const (
	cifsSuperMagic = 0xFF534D42
	smb2SuperMagic = 0xFE534D42
)

// Probe 用 statfs 和 /proc/self/mountinfo 确认路径是期望的 CIFS 挂载。
type Probe struct{}

// Probe 实现 platform.StorageProbe。
func (Probe) Probe(_ context.Context, path string, expect fsinfo.Expectation) (fsinfo.Info, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return fsinfo.Info{Path: path}, fmt.Errorf("statfs %s: %w", path, err)
	}
	info := fsinfo.Info{Path: path, FreeBytes: st.Bavail * uint64(st.Bsize)}
	switch uint32(st.Type) {
	case cifsSuperMagic, smb2SuperMagic:
		info.FSType = "cifs"
	default:
		info.FSType = fmt.Sprintf("0x%x", uint32(st.Type))
	}
	src, mountedHere := mountSource(path)
	info.Source = src
	if info.FSType != "cifs" {
		// 非 CIFS 时用 mountinfo 的类型名方便排查
		if t := mountType(path); t != "" {
			info.FSType = t
		}
	}
	info.Matched = fsinfo.Matches(expect, info.FSType, info.Source, mountedHere)
	if !info.Matched {
		return info, fmt.Errorf("%s is %s mounted from %q, expected %s %q", path, info.FSType, info.Source, expect.Kind, expect.Share)
	}
	return info, nil
}

func mountSource(path string) (string, bool) {
	src, _, here := mountEntry(path)
	return src, here
}

func mountType(path string) string {
	_, t, _ := mountEntry(path)
	return t
}

// mountEntry 在 /proc/self/mountinfo 中找精确等于 path 的挂载点。
func mountEntry(path string) (source, fstype string, found bool) {
	f, err := os.Open("/proc/self/mountinfo")
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
			return unescape(fields[sep+2]), fields[sep+1], true
		}
	}
	return "", "", false
}

func unescape(s string) string {
	r := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	return r.Replace(s)
}
