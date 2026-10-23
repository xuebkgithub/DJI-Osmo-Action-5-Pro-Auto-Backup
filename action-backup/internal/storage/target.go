// Package storage 管理目标存储：挂载校验、路径解析与受门禁保护的写入入口。
package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"action-backup/internal/config"
	"action-backup/internal/domain"
	"action-backup/internal/faultinject"
	"action-backup/internal/platform"
	"action-backup/internal/runmode"
	"action-backup/internal/storage/layout"
)

// PartSuffix 临时文件后缀。
const PartSuffix = ".part"

// Target 目标存储。
type Target struct {
	kind       string
	mountPoint string
	root       string
	expect     platform.MountExpectation
	probe      platform.StorageProbe
	guard      *runmode.Guard
	layout     *layout.Template
	inject     faultinject.Injector
}

// SetInjector 设置故障注入器（测试构建）。
func (t *Target) SetInjector(i faultinject.Injector) {
	if i != nil {
		t.inject = i
	}
}

// New 按配置创建目标。cifs 类型的挂载动作由后续版本的 root helper 完成，这里只做校验与写入。
func New(cfg config.StorageConfig, guard *runmode.Guard, probe platform.StorageProbe) (*Target, error) {
	tpl, err := layout.Parse(cfg.DestinationLayout)
	if err != nil {
		return nil, err
	}
	t := &Target{kind: cfg.Type, probe: probe, guard: guard, layout: tpl, inject: faultinject.Noop{}}
	switch cfg.Type {
	case config.StorageLocal:
		t.mountPoint = cfg.LocalRoot
		t.expect = platform.MountExpectation{Kind: "local"}
	case config.StoragePremounted:
		t.mountPoint = cfg.MountPoint
		t.expect = platform.MountExpectation{Kind: "network", Share: cfg.Share}
	case config.StorageCIFS:
		t.mountPoint = cfg.MountPoint
		t.expect = platform.MountExpectation{Kind: "cifs", Share: cfg.Share}
	default:
		return nil, fmt.Errorf("unknown storage type %q", cfg.Type)
	}
	t.root = filepath.Join(t.mountPoint, filepath.FromSlash(cfg.DestinationRoot))
	return t, nil
}

// Kind 目标类型。
func (t *Target) Kind() string { return t.kind }

// Root 目标根目录（挂载点 + destination_root）。
func (t *Target) Root() string { return t.root }

// MountPoint 挂载点。
func (t *Target) MountPoint() string { return t.mountPoint }

// Layout 路径模板。
func (t *Target) Layout() *layout.Template { return t.layout }

// Probe 确认挂载点是期望的文件系统。
func (t *Target) Probe(ctx context.Context) (platform.MountInfo, error) {
	if err := t.inject.Hit(ctx, faultinject.DestProbe, faultinject.HitMeta{Path: t.mountPoint}); err != nil {
		return platform.MountInfo{Path: t.mountPoint}, fmt.Errorf("%w: %v", domain.ErrStorageUnavailable, err)
	}
	info, err := t.probe.Probe(ctx, t.mountPoint, t.expect)
	if err != nil {
		return info, fmt.Errorf("%w: %v", domain.ErrStorageUnavailable, err)
	}
	return info, nil
}

// Health 健康检查结果。
type Health struct {
	OK         bool   `json:"ok"`
	Kind       string `json:"kind"`
	MountPoint string `json:"mount_point"`
	Root       string `json:"root"`
	FSType     string `json:"fs_type,omitempty"`
	Source     string `json:"source,omitempty"`
	FreeBytes  uint64 `json:"free_bytes"`
	Error      string `json:"error,omitempty"`
	CheckedAt  string `json:"checked_at"`
}

// Health 执行探测并汇总。
func (t *Target) Health(ctx context.Context) Health {
	h := Health{Kind: t.kind, MountPoint: t.mountPoint, Root: t.root, CheckedAt: time.Now().UTC().Format(time.RFC3339)}
	info, err := t.Probe(ctx)
	h.FSType, h.Source, h.FreeBytes = info.FSType, info.Source, info.FreeBytes
	if err != nil {
		h.Error = err.Error()
		return h
	}
	h.OK = true
	return h
}

// Resolve 把相对 destination_root 的路径解析为绝对路径，并确认未越界。
func (t *Target) Resolve(rel string) (string, error) {
	clean, err := layout.Sanitize(filepath.ToSlash(rel))
	if err != nil {
		return "", fmt.Errorf("%w: %v", domain.ErrPathEscapesRoot, err)
	}
	abs := filepath.Join(t.root, filepath.FromSlash(clean))
	if abs != t.root && !strings.HasPrefix(abs, t.root+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %s", domain.ErrPathEscapesRoot, rel)
	}
	return abs, nil
}

// checkWrite 所有写入入口的门禁。
func (t *Target) checkWrite(ctx context.Context) error {
	if !t.guard.AllowDestinationWrite() {
		return fmt.Errorf("%w (mode %s)", domain.ErrModeReadOnly, t.guard.Mode())
	}
	if _, err := t.Probe(ctx); err != nil {
		return err
	}
	return nil
}

// CreatePart 创建或打开 rel 对应的 .part 文件用于追加写。
func (t *Target) CreatePart(ctx context.Context, rel string) (*os.File, error) {
	if err := t.checkWrite(ctx); err != nil {
		return nil, err
	}
	abs, err := t.Resolve(rel)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o750); err != nil {
		return nil, fmt.Errorf("create destination directory: %w", err)
	}
	f, err := os.OpenFile(abs+PartSuffix, os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		return nil, fmt.Errorf("open part file: %w", err)
	}
	return f, nil
}

// Commit 把 rel 的 .part 重命名为正式文件。
func (t *Target) Commit(ctx context.Context, rel string) error {
	if err := t.checkWrite(ctx); err != nil {
		return err
	}
	abs, err := t.Resolve(rel)
	if err != nil {
		return err
	}
	if err := os.Rename(abs+PartSuffix, abs); err != nil {
		return fmt.Errorf("rename part file: %w", err)
	}
	return nil
}

// RemovePart 删除 rel 的 .part 文件；不存在不算错误。
func (t *Target) RemovePart(ctx context.Context, rel string) error {
	if err := t.checkWrite(ctx); err != nil {
		return err
	}
	abs, err := t.Resolve(rel)
	if err != nil {
		return err
	}
	if err := os.Remove(abs + PartSuffix); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove part file: %w", err)
	}
	return nil
}

// Stat 读取正式文件信息，用于增量判断；不受门禁限制。
func (t *Target) Stat(rel string) (os.FileInfo, error) {
	abs, err := t.Resolve(rel)
	if err != nil {
		return nil, err
	}
	return os.Stat(abs)
}

// RootExists 目标根目录是否已存在（只读检查）。
func (t *Target) RootExists() bool {
	info, err := os.Stat(t.root)
	return err == nil && info.IsDir()
}
