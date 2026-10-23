package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"
)

// SourceFile 相机卷上的一个源文件。
type SourceFile struct {
	ID           int64
	VolumeID     int64
	VolumeKey    string
	DeviceKey    string
	RelativePath string
	Size         int64
	ModTime      time.Time
	Fingerprint  string
}

// NormalizeModTime 将修改时间截断到整秒并统一为 UTC。
// exFAT 时间戳为 2 秒 DOS 时间加 10 ms 增量，mac 实测为秒级，不同主机对时区字段解释可能不同。
func NormalizeModTime(t time.Time) time.Time {
	return t.UTC().Truncate(time.Second)
}

// ComputeFingerprint 基于设备、卷、相对路径、大小和归一化修改时间生成指纹。
func ComputeFingerprint(deviceKey, volumeKey, relativePath string, size int64, modTime time.Time) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%s\n%s\n%s\n%d\n%d\n", deviceKey, volumeKey, relativePath, size, NormalizeModTime(modTime).Unix())
	return hex.EncodeToString(h.Sum(nil))
}
