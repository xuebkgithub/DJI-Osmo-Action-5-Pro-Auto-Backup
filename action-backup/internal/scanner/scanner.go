// Package scanner 只读递归扫描相机卷上的 DCIM 目录。
package scanner

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"action-backup/internal/domain"
)

// Options 扫描参数。
type Options struct {
	// SourceDirectory 相对卷根的扫描起点，默认 DCIM。
	SourceDirectory string
	// Extensions 小写、带点的扩展名白名单。
	Extensions []string
	// IgnorePatterns 按文件或目录基名匹配（filepath.Match）的忽略规则。
	IgnorePatterns []string
}

// Scanner 扫描器。它只调用 Lstat / ReadDir，不创建、修改或删除任何文件。
type Scanner struct {
	opts Options
	exts map[string]bool
}

// New 创建扫描器。
func New(opts Options) *Scanner {
	if opts.SourceDirectory == "" {
		opts.SourceDirectory = "DCIM"
	}
	s := &Scanner{opts: opts, exts: map[string]bool{}}
	for _, e := range opts.Extensions {
		e = strings.ToLower(strings.TrimSpace(e))
		if !strings.HasPrefix(e, ".") {
			e = "." + e
		}
		s.exts[e] = true
	}
	return s
}

// Result 扫描结果。
type Result struct {
	Files        []domain.SourceFile
	Skipped      int // 扩展名不匹配或被忽略的文件数
	TotalBytes   int64
	SourceExists bool
}

// Scan 扫描一个已挂载的卷。mountPath 为卷根。
func (s *Scanner) Scan(ctx context.Context, device domain.Device, vol domain.Volume, mountPath string) (Result, error) {
	var res Result
	root := filepath.Join(mountPath, filepath.FromSlash(s.opts.SourceDirectory))
	st, err := os.Lstat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return res, nil
		}
		return res, fmt.Errorf("stat %s: %w", root, err)
	}
	if !st.IsDir() {
		return res, fmt.Errorf("%s is not a directory", root)
	}
	res.SourceExists = true
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			if p == root {
				return walkErr
			}
			// 单个条目不可读时跳过并继续
			return nil
		}
		name := d.Name()
		if p != root && s.ignored(name) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			res.Skipped++
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 || !d.Type().IsRegular() {
			res.Skipped++
			return nil
		}
		if !s.exts[strings.ToLower(filepath.Ext(name))] {
			res.Skipped++
			return nil
		}
		info, err := d.Info()
		if err != nil {
			res.Skipped++
			return nil
		}
		rel, err := filepath.Rel(mountPath, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		f := domain.SourceFile{
			VolumeID:     vol.ID,
			VolumeKey:    vol.Key,
			DeviceKey:    device.Key,
			RelativePath: rel,
			Size:         info.Size(),
			ModTime:      domain.NormalizeModTime(info.ModTime()),
		}
		f.Fingerprint = domain.ComputeFingerprint(device.Key, vol.Key, rel, f.Size, f.ModTime)
		res.Files = append(res.Files, f)
		res.TotalBytes += f.Size
		return nil
	})
	if err != nil {
		return res, err
	}
	return res, nil
}

func (s *Scanner) ignored(name string) bool {
	for _, pat := range s.opts.IgnorePatterns {
		if ok, _ := path.Match(pat, name); ok {
			return true
		}
	}
	return false
}
