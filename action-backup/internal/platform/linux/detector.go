package linux

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"action-backup/internal/domain"
	"action-backup/internal/platform/raw"
)

// Detector 轮询 sysfs 与 udev 数据库发现相机。不需要特权：sysfs 属性与 /run/udev/data 都是世界可读。
// 不用 netlink uevent：轮询 2 秒一次对本场景足够，且与 darwin 后端行为一致、无需额外权限。
type Detector struct {
	vendorID  string
	productID string
	mountRoot string
	interval  time.Duration
	fs        Sysfs
	mountinfo string
	log       *slog.Logger

	mu            sync.Mutex
	known         map[string]raw.Device
	pendingLogged map[string]bool
}

// NewDetector 创建探测器。mountRoot 用于识别已由挂载守护挂好的卷（服务重启后沿用）。
func NewDetector(vendorID, productID, mountRoot string, interval time.Duration, log *slog.Logger) *Detector {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	if log == nil {
		log = slog.Default()
	}
	return &Detector{
		vendorID: strings.ToLower(vendorID), productID: strings.ToLower(productID), mountRoot: mountRoot,
		interval: interval, fs: DefaultSysfsTree(), mountinfo: DefaultMountInfo, log: log,
		known: map[string]raw.Device{}, pendingLogged: map[string]bool{},
	}
}

// Discover 枚举当前就绪的相机。
func (d *Detector) Discover(ctx context.Context) ([]raw.Device, error) {
	ready, err := d.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	d.known = map[string]raw.Device{}
	for _, dev := range ready {
		d.known[dev.Key] = dev
	}
	d.mu.Unlock()
	return ready, nil
}

// Watch 轮询并发出插入 / 拔出事件。
func (d *Detector) Watch(ctx context.Context) (<-chan raw.Event, error) {
	ch := make(chan raw.Event, 16)
	go func() {
		defer close(ch)
		t := time.NewTicker(d.interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			ready, err := d.snapshot(ctx)
			if err != nil {
				d.log.Warn("linux device poll failed", "err", err)
				continue
			}
			for _, ev := range d.diff(ready) {
				select {
				case ch <- ev:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return ch, nil
}

func (d *Detector) diff(ready []raw.Device) []raw.Event {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := time.Now()
	var evs []raw.Event
	seen := map[string]bool{}
	for _, dev := range ready {
		seen[dev.Key] = true
		if _, ok := d.known[dev.Key]; !ok {
			d.known[dev.Key] = dev
			evs = append(evs, raw.Event{Kind: raw.Attached, Device: dev, At: now})
		}
	}
	for key, dev := range d.known {
		if !seen[key] {
			delete(d.known, key)
			evs = append(evs, raw.Event{Kind: raw.Detached, Device: dev, At: now})
		}
	}
	return evs
}

func (d *Detector) snapshot(ctx context.Context) ([]raw.Device, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	usbs, err := d.fs.findUSBDevices(d.vendorID, d.productID)
	if err != nil {
		return nil, err
	}
	var ready []raw.Device
	for _, u := range usbs {
		dev, err := d.buildDevice(u)
		if err != nil {
			if errors.Is(err, errNotReady) {
				if !d.pendingLogged[u.Name] {
					d.log.Info("camera present but volumes not ready yet", "device", u.Name, "reason", err)
					d.pendingLogged[u.Name] = true
				}
				continue
			}
			d.log.Warn("enumerate camera failed", "device", u.Name, "err", err)
			continue
		}
		delete(d.pendingLogged, u.Name)
		ready = append(ready, dev)
	}
	return ready, nil
}

func (d *Detector) buildDevice(u usbDevice) (raw.Device, error) {
	vols, err := d.fs.volumesOf(u)
	if err != nil {
		return raw.Device{}, err
	}
	serial, weak := domain.ResolveSerial(u.Product, u.Serial)
	dev := raw.Device{
		Key: domain.DeviceKey(d.vendorID, d.productID, serial), VendorID: d.vendorID, ProductID: d.productID,
		SerialNumber: serial, Name: displayName(u), WeakIdentity: weak,
	}
	for _, v := range vols {
		key := "uuid:" + strings.ToLower(v.Props["ID_FS_UUID"])
		if v.Props["ID_FS_UUID"] == "" {
			key = fmt.Sprintf("usb:%s:lun%d:%d", serial, v.LUN, v.SizeBytes)
		}
		rv := raw.Volume{
			Key: key, LUN: v.LUN, Label: v.Props["ID_FS_LABEL"], Filesystem: strings.ToLower(v.Props["ID_FS_TYPE"]),
			SizeBytes: v.SizeBytes, DevicePath: "/dev/" + v.Name,
		}
		// 已由挂载守护挂好（例如服务重启后）则直接复用挂载点，避免重复挂载。
		target := filepath.Join(d.mountRoot, v.Name)
		if src, _, ok := mountEntryIn(d.mountinfo, target); ok && src == rv.DevicePath {
			rv.MountPath = target
		}
		dev.Volumes = append(dev.Volumes, rv)
	}
	return dev, nil
}

func displayName(u usbDevice) string {
	name := u.Product
	if i := strings.Index(name, "_SN:"); i > 0 {
		name = name[:i]
	}
	if name == "" {
		name = u.Name
	}
	if u.Manufacturer != "" && !strings.HasPrefix(strings.ToLower(name), strings.ToLower(u.Manufacturer)) {
		name = u.Manufacturer + " " + name
	}
	return name
}
