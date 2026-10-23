//go:build darwin

package darwin

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"action-backup/internal/domain"
	"action-backup/internal/platform/raw"
)

// Runner 执行外部命令，便于测试注入。
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// ExecRunner 真实执行，带超时。
func ExecRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return out, nil
}

// Detector 通过轮询 ioreg 与 diskutil 发现 macOS 自动挂载的相机卷。
type Detector struct {
	vendorID  string
	productID string
	vid, pid  int64
	interval  time.Duration
	run       Runner
	log       *slog.Logger

	mu    sync.Mutex
	known map[string]raw.Device
	// pendingLogged 记录已提示过"未就绪"的设备，避免刷屏。
	pendingLogged map[string]bool
}

// NewDetector 创建探测器。vendorID / productID 为 4 位十六进制。
func NewDetector(vendorID, productID string, interval time.Duration, run Runner, log *slog.Logger) (*Detector, error) {
	vid, err := strconv.ParseInt(vendorID, 16, 64)
	if err != nil {
		return nil, fmt.Errorf("vendor_id %q: %w", vendorID, err)
	}
	pid, err := strconv.ParseInt(productID, 16, 64)
	if err != nil {
		return nil, fmt.Errorf("product_id %q: %w", productID, err)
	}
	if interval <= 0 {
		interval = 2 * time.Second
	}
	if run == nil {
		run = ExecRunner
	}
	if log == nil {
		log = slog.Default()
	}
	return &Detector{
		vendorID: strings.ToLower(vendorID), productID: strings.ToLower(productID), vid: vid, pid: pid,
		interval: interval, run: run, log: log, known: map[string]raw.Device{}, pendingLogged: map[string]bool{},
	}, nil
}

// Discover 返回当前就绪（所有卷已挂载）的相机。
func (d *Detector) Discover(ctx context.Context) ([]raw.Device, error) {
	ready, _, err := d.snapshot(ctx)
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
			ready, _, err := d.snapshot(ctx)
			if err != nil {
				d.log.Warn("darwin device poll failed", "err", err)
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

// snapshot 枚举一次，返回就绪设备与未就绪设备数。
func (d *Detector) snapshot(ctx context.Context) (ready []raw.Device, pending int, err error) {
	out, err := d.run(ctx, "ioreg", "-p", "IOUSB", "-a", "-l", "-w0")
	if err != nil {
		return nil, 0, err
	}
	nodes, err := findUSBDevices(out, d.vid, d.pid)
	if err != nil {
		return nil, 0, err
	}
	for _, n := range nodes {
		dev, ok, err := d.buildDevice(ctx, n)
		if err != nil {
			d.log.Warn("enumerate camera failed", "device", n.EntryName, "err", err)
			continue
		}
		if !ok {
			pending++
			if !d.pendingLogged[n.EntryName] {
				d.log.Info("camera present but volumes not mounted yet", "device", n.EntryName)
				d.pendingLogged[n.EntryName] = true
			}
			continue
		}
		delete(d.pendingLogged, n.EntryName)
		ready = append(ready, dev)
	}
	return ready, pending, nil
}

func (d *Detector) buildDevice(ctx context.Context, n usbNode) (raw.Device, bool, error) {
	sub, err := d.run(ctx, "ioreg", "-p", "IOService", "-a", "-l", "-w0", "-r", "-n", n.EntryName)
	if err != nil {
		return raw.Device{}, false, err
	}
	luns, err := findLUNs(sub)
	if err != nil {
		return raw.Device{}, false, err
	}
	sort.Slice(luns, func(i, j int) bool { return luns[i].LUN < luns[j].LUN })
	serial, weak := domain.ResolveSerial(n.ProductName, n.Serial)
	dev := raw.Device{
		Key: domain.DeviceKey(d.vendorID, d.productID, serial), VendorID: d.vendorID, ProductID: d.productID,
		SerialNumber: serial, Name: displayName(n), WeakIdentity: weak,
	}
	if len(luns) == 0 {
		return dev, false, nil
	}
	for _, l := range luns {
		if l.BSDName == "" {
			return dev, false, nil
		}
		out, err := d.run(ctx, "diskutil", "info", "-plist", l.BSDName)
		if err != nil {
			return dev, false, err
		}
		info, err := parseDiskInfo(out)
		if err != nil {
			return dev, false, err
		}
		if info.MountPoint == "" {
			return dev, false, nil
		}
		if st, err := os.Stat(info.MountPoint); err != nil || !st.IsDir() {
			return dev, false, nil
		}
		key := "uuid:" + strings.ToLower(info.VolumeUUID)
		if info.VolumeUUID == "" {
			key = fmt.Sprintf("bsd:%s:%d:%d", serial, l.LUN, info.Size)
		}
		size := info.Size
		if size == 0 {
			size = l.Size
		}
		dev.Volumes = append(dev.Volumes, raw.Volume{
			Key: key, LUN: l.LUN, Label: info.VolumeName, Filesystem: info.FilesystemType,
			SizeBytes: size, DevicePath: "/dev/" + l.BSDName, MountPath: info.MountPoint,
		})
	}
	return dev, true, nil
}

func displayName(n usbNode) string {
	name := n.ProductName
	if i := strings.Index(name, "_SN:"); i > 0 {
		name = name[:i]
	}
	if name == "" {
		name = n.EntryName
	}
	if n.VendorName != "" && !strings.HasPrefix(strings.ToLower(name), strings.ToLower(n.VendorName)) {
		name = n.VendorName + " " + name
	}
	return name
}

// Mounter macOS 不做重新挂载：卷由系统自动挂载，程序只确认挂载点存在。
type Mounter struct{}

// MountReadOnly 返回系统挂载点。
func (Mounter) MountReadOnly(_ context.Context, v raw.Volume) (string, error) {
	if v.MountPath == "" {
		return "", fmt.Errorf("volume %s is not mounted by macOS", v.Key)
	}
	st, err := os.Stat(v.MountPath)
	if err != nil {
		return "", fmt.Errorf("volume %s: %w", v.Key, err)
	}
	if !st.IsDir() {
		return "", fmt.Errorf("volume %s: %s is not a directory", v.Key, v.MountPath)
	}
	return v.MountPath, nil
}

// Unmount 无操作。
func (Mounter) Unmount(context.Context, raw.Volume) error { return nil }
