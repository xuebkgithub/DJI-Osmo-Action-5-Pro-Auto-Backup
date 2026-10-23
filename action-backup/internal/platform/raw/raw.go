// Package raw 定义平台层设备与事件类型，供 platform 及其各后端共用，避免循环引用。
package raw

import "time"

// Volume 平台层看到的卷。
type Volume struct {
	Key        string
	LUN        int
	Label      string
	Filesystem string
	SizeBytes  int64
	DevicePath string
	// MountPath 非空表示系统已挂载（mac /Volumes、sim 目录）。
	MountPath string
}

// Device 平台层看到的 USB 设备。
type Device struct {
	Key          string
	VendorID     string
	ProductID    string
	SerialNumber string
	Name         string
	WeakIdentity bool
	Volumes      []Volume
}

// EventKind 设备事件类型。
type EventKind string

const (
	Attached EventKind = "attached"
	Detached EventKind = "detached"
)

// Event 设备事件。
type Event struct {
	Kind   EventKind
	Device Device
	At     time.Time
}
