package platform

import (
	"context"
	"errors"
	"sync"

	"action-backup/internal/domain"
)

// MultiDetector 合并多个探测器（如 sim + darwin）。
type MultiDetector []DeviceDetector

// Discover 合并各探测器结果；单个探测器未实现时忽略。
func (m MultiDetector) Discover(ctx context.Context) ([]RawDevice, error) {
	var out []RawDevice
	var errs []error
	for _, d := range m {
		devs, err := d.Discover(ctx)
		if err != nil {
			if errors.Is(err, domain.ErrNotImplemented) {
				continue
			}
			errs = append(errs, err)
			continue
		}
		out = append(out, devs...)
	}
	return out, errors.Join(errs...)
}

// Watch 合并事件流。
func (m MultiDetector) Watch(ctx context.Context) (<-chan DeviceEvent, error) {
	out := make(chan DeviceEvent, 32)
	var wg sync.WaitGroup
	started := 0
	for _, d := range m {
		ch, err := d.Watch(ctx)
		if err != nil {
			if errors.Is(err, domain.ErrNotImplemented) {
				continue
			}
			return nil, err
		}
		started++
		wg.Add(1)
		go func(ch <-chan DeviceEvent) {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case ev, ok := <-ch:
					if !ok {
						return
					}
					select {
					case out <- ev:
					case <-ctx.Done():
						return
					}
				}
			}
		}(ch)
	}
	go func() {
		wg.Wait()
		close(out)
	}()
	if started == 0 {
		return out, nil
	}
	return out, nil
}

// MultiMounter 按卷的挂载来源选择挂载器：已带 MountPath 的卷直接返回，否则交给 fallback。
type MultiMounter struct {
	Fallback VolumeMounter
}

// MountReadOnly 已挂载的卷直接返回路径。
func (m MultiMounter) MountReadOnly(ctx context.Context, v RawVolume) (string, error) {
	if v.MountPath != "" {
		return v.MountPath, nil
	}
	if m.Fallback == nil {
		return "", domain.ErrNotImplemented
	}
	return m.Fallback.MountReadOnly(ctx, v)
}

// Unmount 已挂载的卷不处理。
func (m MultiMounter) Unmount(ctx context.Context, v RawVolume) error {
	if v.MountPath != "" || m.Fallback == nil {
		return nil
	}
	return m.Fallback.Unmount(ctx, v)
}
