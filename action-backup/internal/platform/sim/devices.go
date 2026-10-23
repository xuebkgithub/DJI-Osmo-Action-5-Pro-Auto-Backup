// Package sim 提供目录型模拟相机，用于 mac 开发与自动化测试。
package sim

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"action-backup/internal/config"
	"action-backup/internal/domain"
	"action-backup/internal/platform/fsinfo"
	"action-backup/internal/platform/raw"
)

type simDevice struct {
	raw      raw.Device
	attached bool
}

// Backend 模拟后端，同时实现设备发现与挂载。
type Backend struct {
	mu      sync.Mutex
	devices map[string]*simDevice
	order   []string
	subs    map[int]chan raw.Event
	nextSub int
}

// New 根据 fixtures 配置创建后端。
func New(fx config.SimFixturesConfig, dev config.DeviceConfig) (*Backend, error) {
	b := &Backend{devices: map[string]*simDevice{}, subs: map[int]chan raw.Event{}}
	for _, d := range fx.Devices {
		serial, weak := domain.ResolveSerial("SN:"+d.Serial, "")
		dev := raw.Device{
			Key:          domain.DeviceKey(dev.VendorID, dev.ProductID, serial),
			VendorID:     dev.VendorID,
			ProductID:    dev.ProductID,
			SerialNumber: serial,
			Name:         d.Name,
			WeakIdentity: weak,
		}
		for lun, v := range d.Volumes {
			label := v.Label
			if label == "" {
				if v.Type == string(domain.VolumeInternal) {
					label = "OsmoAction"
				} else {
					label = "Untitled"
				}
			}
			dev.Volumes = append(dev.Volumes, raw.Volume{
				Key:        fmt.Sprintf("sim:%s:%s", serial, v.Type),
				LUN:        lun,
				Label:      label,
				Filesystem: "exfat",
				SizeBytes:  dirSize(v.Path),
				DevicePath: v.Path,
				MountPath:  v.Path,
			})
		}
		if _, dup := b.devices[d.Key]; dup {
			return nil, fmt.Errorf("duplicate simulated device key %q", d.Key)
		}
		b.devices[d.Key] = &simDevice{raw: dev, attached: fx.AttachedOnStart}
		b.order = append(b.order, d.Key)
	}
	return b, nil
}

func dirSize(path string) int64 {
	// 模拟卷不报告容量，让分类规则走 LUN 顺序；目录不存在时同样返回 0，由挂载阶段报错。
	if st, err := os.Stat(path); err != nil || !st.IsDir() {
		return 0
	}
	return 0
}

// Keys 返回配置顺序的设备键。
func (b *Backend) Keys() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.order...)
}

// Discover 返回当前已 attach 的设备。
func (b *Backend) Discover(context.Context) ([]raw.Device, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []raw.Device
	for _, k := range b.order {
		if d := b.devices[k]; d.attached {
			out = append(out, d.raw)
		}
	}
	return out, nil
}

// Watch 订阅事件；ctx 取消时自动退订。
func (b *Backend) Watch(ctx context.Context) (<-chan raw.Event, error) {
	ch := make(chan raw.Event, 16)
	b.mu.Lock()
	id := b.nextSub
	b.nextSub++
	b.subs[id] = ch
	b.mu.Unlock()
	go func() {
		<-ctx.Done()
		b.mu.Lock()
		delete(b.subs, id)
		b.mu.Unlock()
	}()
	return ch, nil
}

// Attach 模拟插入。
func (b *Backend) Attach(key string) error { return b.set(key, true) }

// Detach 模拟拔出。
func (b *Backend) Detach(key string) error { return b.set(key, false) }

func (b *Backend) set(key string, attached bool) error {
	b.mu.Lock()
	d, ok := b.devices[key]
	if !ok {
		b.mu.Unlock()
		return fmt.Errorf("simulated device %q: %w", key, domain.ErrDeviceNotFound)
	}
	if d.attached == attached {
		b.mu.Unlock()
		return nil
	}
	d.attached = attached
	kind := raw.Attached
	if !attached {
		kind = raw.Detached
	}
	ev := raw.Event{Kind: kind, Device: d.raw, At: time.Now()}
	subs := make([]chan raw.Event, 0, len(b.subs))
	for _, ch := range b.subs {
		subs = append(subs, ch)
	}
	b.mu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- ev:
		default:
		}
	}
	return nil
}

// Attached 返回设备是否已插入。
func (b *Backend) Attached(key string) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	d, ok := b.devices[key]
	if !ok {
		return false, fmt.Errorf("simulated device %q: %w", key, domain.ErrDeviceNotFound)
	}
	return d.attached, nil
}

// MountReadOnly 模拟卷无需挂载，只确认目录存在。
func (b *Backend) MountReadOnly(_ context.Context, v raw.Volume) (string, error) {
	st, err := os.Stat(v.MountPath)
	if err != nil {
		return "", fmt.Errorf("simulated volume %s: %w", v.Key, err)
	}
	if !st.IsDir() {
		return "", fmt.Errorf("simulated volume %s: %s is not a directory", v.Key, v.MountPath)
	}
	return v.MountPath, nil
}

// Unmount 无操作。
func (b *Backend) Unmount(context.Context, raw.Volume) error { return nil }

// LocalProbe 接受普通本地目录作为目标，仅 simulation 模式使用。
type LocalProbe struct{}

// Probe 实现 fsinfo 探测。
func (LocalProbe) Probe(_ context.Context, path string, expect fsinfo.Expectation) (fsinfo.Info, error) {
	st, err := os.Stat(path)
	if err != nil {
		return fsinfo.Info{Path: path}, fmt.Errorf("local destination %s: %w", path, err)
	}
	if !st.IsDir() {
		return fsinfo.Info{Path: path}, fmt.Errorf("local destination %s is not a directory", path)
	}
	free, _ := freeBytes(path)
	info := fsinfo.Info{Path: path, FSType: "local", Source: path, FreeBytes: free}
	info.Matched = fsinfo.Matches(fsinfo.Expectation{Kind: "local"}, "local", path, true) && expect.Kind == "local"
	if !info.Matched {
		return info, fmt.Errorf("local destination %s cannot satisfy expectation %q", path, expect.Kind)
	}
	return info, nil
}

// MountPassthrough 把已带 MountPath 的卷原样返回，供单元测试使用。
type MountPassthrough struct{}

// MountReadOnly 返回 MountPath。
func (MountPassthrough) MountReadOnly(_ context.Context, v raw.Volume) (string, error) {
	if v.MountPath == "" {
		return "", fmt.Errorf("volume %s has no mount path", v.Key)
	}
	return v.MountPath, nil
}

// Unmount 无操作。
func (MountPassthrough) Unmount(context.Context, raw.Volume) error { return nil }
