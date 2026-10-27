//go:build linux

package linux

import (
	"context"
	"fmt"

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
	// 沙箱（ProtectSystem=strict）可能把挂载设成只读，mountinfo 与 share 都对但写不进去，这里提前报出来
	if st.Flags&unix.ST_RDONLY != 0 {
		info.Matched = false
		return info, fmt.Errorf("%s (%s %s) is mounted read-only", path, info.FSType, info.Source)
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
	return mountEntryIn(DefaultMountInfo, path)
}
