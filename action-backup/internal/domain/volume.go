package domain

import "time"

// VolumeType 卷类型。
type VolumeType string

const (
	VolumeInternal VolumeType = "internal"
	VolumeSDCard   VolumeType = "sdcard"
	VolumeUnknown  VolumeType = "unknown"
)

// Valid 判断卷类型是否合法。
func (t VolumeType) Valid() bool {
	switch t {
	case VolumeInternal, VolumeSDCard, VolumeUnknown:
		return true
	}
	return false
}

// Volume 一个存储卷。
type Volume struct {
	ID         int64
	DeviceID   int64
	DeviceKey  string
	Key        string
	Type       VolumeType
	Filesystem string
	Label      string
	LUN        int
	SizeBytes  int64
	MountPath  string
	LastSeenAt time.Time
}

// ClassifyVolume 按实测规则判断卷类型：LUN 顺序为主，容量为辅，卷名只作旁证。
// LUN 0 为内置存储，LUN 1 为 microSD；LUN 未知（-1）时按容量：64 GB 以下视为内置。
func ClassifyVolume(lun int, sizeBytes int64, label string) VolumeType {
	switch lun {
	case 0:
		return VolumeInternal
	case 1:
		return VolumeSDCard
	}
	const internalMax = 64 << 30
	if sizeBytes > 0 {
		if sizeBytes <= internalMax {
			return VolumeInternal
		}
		return VolumeSDCard
	}
	if label == "OsmoAction" {
		return VolumeInternal
	}
	return VolumeUnknown
}
