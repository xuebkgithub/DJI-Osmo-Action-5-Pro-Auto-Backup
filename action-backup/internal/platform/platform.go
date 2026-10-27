// Package platform 封装操作系统相关的设备发现、挂载与挂载点校验。
package platform

import (
	"context"

	"action-backup/internal/platform/fsinfo"
	"action-backup/internal/platform/raw"
)

// RawVolume 见 raw.Volume。
type RawVolume = raw.Volume

// RawDevice 见 raw.Device。
type RawDevice = raw.Device

// DeviceEventKind 见 raw.EventKind。
type DeviceEventKind = raw.EventKind

const (
	DeviceAttached = raw.Attached
	DeviceDetached = raw.Detached
)

// DeviceEvent 见 raw.Event。
type DeviceEvent = raw.Event

// DeviceDetector 设备发现。
type DeviceDetector interface {
	Discover(ctx context.Context) ([]RawDevice, error)
	Watch(ctx context.Context) (<-chan DeviceEvent, error)
}

// VolumeMounter 只读挂载。
type VolumeMounter interface {
	MountReadOnly(ctx context.Context, volume RawVolume) (string, error)
	Unmount(ctx context.Context, volume RawVolume) error
}

// MountExpectation 期望的目标挂载，见 fsinfo.Expectation。
type MountExpectation = fsinfo.Expectation

// MountInfo 探测结果，见 fsinfo.Info。
type MountInfo = fsinfo.Info

// StorageProbe 确认 path 是期望的挂载而不是底层本地目录。
type StorageProbe interface {
	Probe(ctx context.Context, path string, expect MountExpectation) (MountInfo, error)
}

// StorageMounter 挂载 / 卸载目标存储（Linux 上经 root 挂载守护挂 CIFS）。没有实现的平台为 nil。
type StorageMounter interface {
	MountTarget(ctx context.Context) error
	UnmountTarget(ctx context.Context) error
}

// Backend 一组平台实现。
type Backend struct {
	Name     string
	Detector DeviceDetector
	Mounter  VolumeMounter
	Probe    StorageProbe
	// Storage 目标挂载器；仅 Linux 的 cifs 类型非空。
	Storage StorageMounter
}
