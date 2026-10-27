// Package device 把平台层设备事件转换为领域设备并持久化。
package device

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"action-backup/internal/database/repository"
	"action-backup/internal/domain"
	"action-backup/internal/events"
	"action-backup/internal/platform"
)

// Manager 设备管理器。
type Manager struct {
	detector platform.DeviceDetector
	repo     repository.Devices
	bus      *events.Broadcaster
	log      *slog.Logger

	mu       sync.RWMutex
	attached map[string]domain.Device

	// OnAttached 新设备就绪后的回调（已持久化，含 ID），在独立 goroutine 中调用。
	OnAttached func(ctx context.Context, d domain.Device)
	// OnDetached 设备拔出后的回调（卸载残留挂载等），在独立 goroutine 中调用。
	OnDetached func(ctx context.Context, d domain.Device)
}

// NewManager 创建管理器。
func NewManager(detector platform.DeviceDetector, repo repository.Devices, bus *events.Broadcaster, log *slog.Logger) *Manager {
	return &Manager{detector: detector, repo: repo, bus: bus, log: log, attached: map[string]domain.Device{}}
}

// Start 主动枚举一次并开始监听事件，直到 ctx 取消。
func (m *Manager) Start(ctx context.Context) error {
	if err := m.Rescan(ctx); err != nil {
		if errors.Is(err, domain.ErrNotImplemented) {
			m.log.Warn("device detector not available on this platform yet", "err", err)
			return nil
		}
		return err
	}
	ch, err := m.detector.Watch(ctx)
	if err != nil {
		if errors.Is(err, domain.ErrNotImplemented) {
			m.log.Warn("device watcher not available on this platform yet", "err", err)
			return nil
		}
		return err
	}
	go m.loop(ctx, ch)
	return nil
}

func (m *Manager) loop(ctx context.Context, ch <-chan platform.DeviceEvent) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			switch ev.Kind {
			case platform.DeviceAttached:
				m.onAttached(ctx, ev.Device)
			case platform.DeviceDetached:
				m.onDetached(ctx, ev.Device)
			}
		}
	}
}

// Rescan 重新枚举设备。
func (m *Manager) Rescan(ctx context.Context) error {
	devs, err := m.detector.Discover(ctx)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, d := range devs {
		seen[d.Key] = true
		m.onAttached(ctx, d)
	}
	m.mu.Lock()
	for k, d := range m.attached {
		if !seen[k] {
			delete(m.attached, k)
			m.mu.Unlock()
			m.bus.Publish(ctx, events.Event{Level: "info", Type: "device.detached", Message: fmt.Sprintf("device %s detached", d.Name), Data: map[string]any{"device_key": k}})
			m.mu.Lock()
		}
	}
	m.mu.Unlock()
	return nil
}

func (m *Manager) onAttached(ctx context.Context, raw platform.RawDevice) {
	d := toDomain(raw)
	id, err := m.repo.Upsert(ctx, d)
	if err != nil {
		m.log.Error("persist device failed", "device", d.Key, "err", err)
	} else {
		d.ID = id
		for i := range d.Volumes {
			d.Volumes[i].DeviceID = id
			if vid, err := m.repo.VolumeID(ctx, d.Key, d.Volumes[i].Key); err == nil {
				d.Volumes[i].ID = vid
			}
		}
	}
	m.mu.Lock()
	_, known := m.attached[d.Key]
	m.attached[d.Key] = d
	m.mu.Unlock()
	if known {
		return
	}
	data := map[string]any{"device_key": d.Key, "serial": d.SerialNumber, "volumes": len(d.Volumes), "weak_identity": d.WeakIdentity}
	level := "info"
	msg := fmt.Sprintf("device %s attached with %d volume(s)", d.Name, len(d.Volumes))
	if d.WeakIdentity {
		level = "warn"
		msg += " (weak identity: serial not resolvable)"
	}
	m.bus.Publish(ctx, events.Event{Level: level, Type: "device.attached", Message: msg, Data: data})
	if m.OnAttached != nil {
		go m.OnAttached(ctx, d)
	}
}

func (m *Manager) onDetached(ctx context.Context, raw platform.RawDevice) {
	m.mu.Lock()
	d, known := m.attached[raw.Key]
	delete(m.attached, raw.Key)
	m.mu.Unlock()
	if !known {
		return
	}
	m.bus.Publish(ctx, events.Event{Level: "info", Type: "device.detached", Message: fmt.Sprintf("device %s detached", d.Name), Data: map[string]any{"device_key": d.Key}})
	if m.OnDetached != nil {
		go m.OnDetached(ctx, d)
	}
}

// Attached 当前已插入的设备，按键排序。
func (m *Manager) Attached() []domain.Device {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]domain.Device, 0, len(m.attached))
	for _, d := range m.attached {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

func toDomain(raw platform.RawDevice) domain.Device {
	d := domain.Device{
		Key: raw.Key, VendorID: raw.VendorID, ProductID: raw.ProductID,
		SerialNumber: raw.SerialNumber, Name: raw.Name, WeakIdentity: raw.WeakIdentity,
		LastSeenAt: time.Now(),
	}
	for _, v := range raw.Volumes {
		d.Volumes = append(d.Volumes, domain.Volume{
			DeviceKey: raw.Key, Key: v.Key, Type: domain.ClassifyVolume(v.LUN, v.SizeBytes, v.Label),
			Filesystem: v.Filesystem, Label: v.Label, LUN: v.LUN, SizeBytes: v.SizeBytes, DevicePath: v.DevicePath, MountPath: v.MountPath,
			LastSeenAt: d.LastSeenAt,
		})
	}
	return d
}
