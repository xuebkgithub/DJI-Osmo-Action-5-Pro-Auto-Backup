// Package layout 解析并渲染 destination_layout 模板。
package layout

import (
	"fmt"
	"path"
	"strings"
	"time"
)

// Vars 渲染变量。
type Vars struct {
	RelativePath string
	Filename     string
	VolumeType   string
	VolumeLabel  string
	Serial       string
	DeviceName   string
	MTime        time.Time
}

type part struct {
	literal string
	key     string
	arg     string
}

// Template 已解析的模板。
type Template struct {
	raw   string
	parts []part
}

var knownKeys = map[string]bool{
	"relative_path": true, "filename": true, "volume_type": true, "volume_label": true,
	"serial": true, "device_name": true, "mtime": true, "date": true,
}

// Default 默认模板。
const Default = "{volume_type}/{relative_path}"

// Parse 解析模板并做静态校验。
func Parse(raw string) (*Template, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("destination_layout is empty")
	}
	if strings.HasPrefix(raw, "/") {
		return nil, fmt.Errorf("destination_layout must be relative, got %q", raw)
	}
	t := &Template{raw: raw}
	rest := raw
	hasFile := false
	for len(rest) > 0 {
		i := strings.IndexByte(rest, '{')
		if i < 0 {
			t.parts = append(t.parts, part{literal: rest})
			break
		}
		if i > 0 {
			t.parts = append(t.parts, part{literal: rest[:i]})
		}
		j := strings.IndexByte(rest[i:], '}')
		if j < 0 {
			return nil, fmt.Errorf("destination_layout has unclosed '{' at %d", i)
		}
		token := rest[i+1 : i+j]
		key, arg, _ := strings.Cut(token, ":")
		if !knownKeys[key] {
			return nil, fmt.Errorf("destination_layout has unknown placeholder {%s}", token)
		}
		if key == "mtime" && arg == "" {
			return nil, fmt.Errorf("destination_layout {mtime:LAYOUT} requires a Go time layout")
		}
		if key == "relative_path" || key == "filename" {
			hasFile = true
		}
		t.parts = append(t.parts, part{key: key, arg: arg})
		rest = rest[i+j+1:]
	}
	if !hasFile {
		return nil, fmt.Errorf("destination_layout must contain {relative_path} or {filename}")
	}
	for _, seg := range strings.Split(raw, "/") {
		if seg == ".." {
			return nil, fmt.Errorf("destination_layout must not contain '..'")
		}
	}
	return t, nil
}

// String 返回原始模板。
func (t *Template) String() string { return t.raw }

// Render 渲染并做安全校验，返回相对 destination_root 的干净路径。
func (t *Template) Render(v Vars) (string, error) {
	var b strings.Builder
	for _, p := range t.parts {
		if p.key == "" {
			b.WriteString(p.literal)
			continue
		}
		val, err := t.value(p, v)
		if err != nil {
			return "", err
		}
		b.WriteString(val)
	}
	return Sanitize(b.String())
}

func (t *Template) value(p part, v Vars) (string, error) {
	switch p.key {
	case "relative_path":
		if v.RelativePath == "" {
			return "", fmt.Errorf("relative_path is empty")
		}
		return v.RelativePath, nil
	case "filename":
		if v.Filename != "" {
			return v.Filename, nil
		}
		if v.RelativePath == "" {
			return "", fmt.Errorf("filename is empty")
		}
		return path.Base(v.RelativePath), nil
	case "volume_type":
		return orDefault(v.VolumeType, "unknown"), nil
	case "volume_label":
		return orDefault(v.VolumeLabel, orDefault(v.VolumeType, "unknown")), nil
	case "serial":
		return orDefault(v.Serial, "unknown"), nil
	case "device_name":
		return orDefault(v.DeviceName, "unknown"), nil
	case "mtime":
		return v.MTime.Format(p.arg), nil
	case "date":
		return v.MTime.Format("2006-01-02"), nil
	}
	return "", fmt.Errorf("unknown placeholder %s", p.key)
}

func orDefault(s, d string) string {
	if strings.TrimSpace(s) == "" {
		return d
	}
	return s
}

// Sanitize 把渲染结果整理为安全的相对路径：禁止绝对路径、'..'、空段和反斜杠。
func Sanitize(p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("rendered path is empty")
	}
	if strings.ContainsRune(p, '\\') {
		return "", fmt.Errorf("rendered path contains backslash: %q", p)
	}
	if strings.ContainsRune(p, 0) {
		return "", fmt.Errorf("rendered path contains NUL")
	}
	if strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("rendered path is absolute: %q", p)
	}
	for _, seg := range strings.Split(p, "/") {
		switch seg {
		case "":
			return "", fmt.Errorf("rendered path has empty segment: %q", p)
		case "..":
			return "", fmt.Errorf("rendered path contains '..': %q", p)
		case ".":
			return "", fmt.Errorf("rendered path contains '.': %q", p)
		}
	}
	clean := path.Clean(p)
	if clean != p {
		return "", fmt.Errorf("rendered path is not clean: %q", p)
	}
	return clean, nil
}
