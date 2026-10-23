// Package faultinject 提供故障注入点。只有 -tags faultinject 构建时才包含真实注入器。
package faultinject

import "context"

// Point 注入点。
type Point string

const (
	SourceOpen   Point = "source.open"
	SourceRead   Point = "source.read"
	DeviceDetach Point = "device.detach"
	DestProbe    Point = "dest.probe"
	DestOpen     Point = "dest.open"
	DestWrite    Point = "dest.write"
	DestSync     Point = "dest.sync"
	DestRename   Point = "dest.rename"
	PartCorrupt  Point = "part.corrupt"
	VerifyHash   Point = "verify.hash"
	DBCommit     Point = "db.commit"
	ProcessCrash Point = "process.crash"
)

// HitMeta 注入点上下文信息。
type HitMeta struct {
	TaskID       int64
	RelativePath string
	Bytes        int64
	// Path 注入点相关的绝对文件路径（part.corrupt 需要）。
	Path string
}

// Injector 故障注入器接口。
type Injector interface {
	Hit(ctx context.Context, point Point, meta HitMeta) error
}

// Noop 永不注入。
type Noop struct{}

// Hit 实现 Injector。
func (Noop) Hit(context.Context, Point, HitMeta) error { return nil }
