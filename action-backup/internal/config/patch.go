package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"action-backup/internal/domain"
)

// PatchKind 可修改项的值类型。
type PatchKind string

// 值类型。
const (
	KindBool   PatchKind = "bool"
	KindInt    PatchKind = "int"
	KindString PatchKind = "string"
)

// PatchField 一个允许通过 PATCH /config 修改的配置项。
type PatchField struct {
	Key      string    `json:"key"`
	Kind     PatchKind `json:"kind"`
	Choices  []string  `json:"choices,omitempty"`
	Min, Max int       `json:"-"`
	Doc      string    `json:"doc"`
}

// patchWhitelist 白名单；顺序即 API 输出顺序。
var patchWhitelist = []PatchField{
	{Key: "device.auto_backup", Kind: KindBool, Doc: "设备就绪后自动规划并开始备份（dry-run 只规划）"},
	{Key: "backup.verify_mode", Kind: KindString, Choices: []string{string(domain.VerifyBasic), string(domain.VerifySHA256)}, Doc: "新任务的校验方式；sha256 会重读目标文件"},
	{Key: "backup.on_conflict", Kind: KindString, Choices: []string{"manual", "overwrite", "skip"}, Doc: "计划中 conflict 项的处理"},
	{Key: "backup.retry_count", Kind: KindInt, Min: 0, Max: 100, Doc: "INTERRUPTED 任务自动重试上限"},
	{Key: "backup.reconcile_interval_sec", Kind: KindInt, Min: 1, Max: 3600, Doc: "检查 INTERRUPTED 任务能否重新入队的周期"},
	{Key: "logging.level", Kind: KindString, Choices: []string{"debug", "info", "warn", "error"}, Doc: "日志级别"},
}

// PatchableFields 返回白名单副本。
func PatchableFields() []PatchField {
	out := make([]PatchField, len(patchWhitelist))
	copy(out, patchWhitelist)
	return out
}

// ErrNotPatchable 键不在白名单。
var ErrNotPatchable = errors.New("config key is not patchable")

// Patch 是一组经过类型校验的修改，键为白名单中的点路径。
type Patch map[string]any

// NormalizePatch 校验并规整输入（JSON 解码后的 map），返回类型正确的 Patch。
func NormalizePatch(in map[string]any) (Patch, error) {
	if len(in) == 0 {
		return nil, errors.New("patch is empty")
	}
	out := Patch{}
	var errs []error
	for k, v := range in {
		f, ok := lookupField(k)
		if !ok {
			errs = append(errs, fmt.Errorf("%w: %q", ErrNotPatchable, k))
			continue
		}
		nv, err := coerce(f, v)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", k, err))
			continue
		}
		out[k] = nv
	}
	return out, errors.Join(errs...)
}

func lookupField(key string) (PatchField, bool) {
	for _, f := range patchWhitelist {
		if f.Key == key {
			return f, true
		}
	}
	return PatchField{}, false
}

func coerce(f PatchField, v any) (any, error) {
	switch f.Kind {
	case KindBool:
		switch x := v.(type) {
		case bool:
			return x, nil
		case string:
			b, err := strconv.ParseBool(x)
			if err != nil {
				return nil, errors.New("expected boolean")
			}
			return b, nil
		}
		return nil, errors.New("expected boolean")
	case KindInt:
		var n int
		switch x := v.(type) {
		case float64:
			if x != float64(int(x)) {
				return nil, errors.New("expected integer")
			}
			n = int(x)
		case int:
			n = x
		case string:
			i, err := strconv.Atoi(x)
			if err != nil {
				return nil, errors.New("expected integer")
			}
			n = i
		default:
			return nil, errors.New("expected integer")
		}
		if n < f.Min || n > f.Max {
			return nil, fmt.Errorf("must be between %d and %d", f.Min, f.Max)
		}
		return n, nil
	case KindString:
		s, ok := v.(string)
		if !ok {
			return nil, errors.New("expected string")
		}
		s = strings.TrimSpace(s)
		if len(f.Choices) > 0 {
			for _, c := range f.Choices {
				if c == s {
					return s, nil
				}
			}
			return nil, fmt.Errorf("must be one of %s", strings.Join(f.Choices, ", "))
		}
		return s, nil
	}
	return nil, errors.New("unknown kind")
}

// Apply 把 Patch 应用到配置副本并返回；不校验模式组合，调用方应随后 Validate。
func (c *Config) Apply(p Patch) (*Config, error) {
	out := *c
	for k, v := range p {
		switch k {
		case "device.auto_backup":
			out.Device.AutoBackup = v.(bool)
		case "backup.verify_mode":
			out.Backup.VerifyMode = v.(string)
		case "backup.on_conflict":
			out.Backup.OnConflict = v.(string)
		case "backup.retry_count":
			out.Backup.RetryCount = v.(int)
		case "backup.reconcile_interval_sec":
			out.Backup.ReconcileIntervalSec = v.(int)
		case "logging.level":
			out.Logging.Level = v.(string)
		default:
			return nil, fmt.Errorf("%w: %q", ErrNotPatchable, k)
		}
	}
	return &out, nil
}

// SetValues 把任意点路径键值写入 YAML 文件，保留注释与顺序；不存在的键会创建。
// 用于 PATCH /config 与 passwd 子命令。写入为原子替换。
func SetValues(path string, values map[string]any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	var doc yaml.Node
	if len(strings.TrimSpace(string(data))) > 0 {
		if err := yaml.Unmarshal(data, &doc); err != nil {
			return fmt.Errorf("parse config: %w", err)
		}
	}
	if doc.Kind == 0 {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return errors.New("config root must be a mapping")
	}
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if err := setNode(doc.Content[0], strings.Split(k, "."), values[k]); err != nil {
			return fmt.Errorf("set %s: %w", k, err)
		}
	}
	var buf strings.Builder
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	if err := enc.Close(); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.yaml")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.WriteString(buf.String()); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Chmod(tmpName, info.Mode().Perm()); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return err
	}
	return nil
}

func setNode(m *yaml.Node, path []string, v any) error {
	if m.Kind != yaml.MappingNode {
		return errors.New("parent is not a mapping")
	}
	key := path[0]
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value != key {
			continue
		}
		val := m.Content[i+1]
		if len(path) == 1 {
			return setScalar(val, v)
		}
		if val.Kind != yaml.MappingNode {
			if val.Kind == yaml.ScalarNode && val.Tag == "!!null" {
				*val = yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			} else {
				return fmt.Errorf("%s is not a mapping", key)
			}
		}
		return setNode(val, path[1:], v)
	}
	// 不存在：追加
	k := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
	var val *yaml.Node
	if len(path) == 1 {
		val = &yaml.Node{Kind: yaml.ScalarNode}
		if err := setScalar(val, v); err != nil {
			return err
		}
	} else {
		val = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		if err := setNode(val, path[1:], v); err != nil {
			return err
		}
	}
	m.Content = append(m.Content, k, val)
	return nil
}

func setScalar(n *yaml.Node, v any) error {
	if n.Kind != yaml.ScalarNode {
		return errors.New("existing value is not a scalar")
	}
	n.Style = 0
	switch x := v.(type) {
	case bool:
		n.Tag, n.Value = "!!bool", strconv.FormatBool(x)
	case int:
		n.Tag, n.Value = "!!int", strconv.Itoa(x)
	case int64:
		n.Tag, n.Value = "!!int", strconv.FormatInt(x, 10)
	case string:
		n.Tag, n.Value = "!!str", x
		if x == "" || strings.ContainsAny(x, ":#{}[]&*!|>'\"%@`$") || strings.HasPrefix(x, " ") {
			n.Style = yaml.DoubleQuotedStyle
		}
	default:
		return fmt.Errorf("unsupported value type %T", v)
	}
	return nil
}

// ValidationError 修改后的配置未通过 Validate。
type ValidationError struct{ Err error }

func (e *ValidationError) Error() string { return "invalid config: " + e.Err.Error() }

// Unwrap 返回原始错误。
func (e *ValidationError) Unwrap() error { return e.Err }
