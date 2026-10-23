package faultinject

import (
	"context"
	"fmt"
	"path"
	"strings"
)

// Trigger 触发条件。多个条件同时满足才触发；都为零值表示每次匹配调用都触发。
type Trigger struct {
	// Once 等价于 Times = 1。
	Once bool `json:"once,omitempty" yaml:"once"`
	// NthCall 第 N 次匹配调用触发（从 1 开始）。
	NthCall int `json:"nth_call,omitempty" yaml:"nth_call"`
	// AfterBytes 当调用携带的累计字节数达到该值后触发。
	AfterBytes int64 `json:"after_bytes,omitempty" yaml:"after_bytes"`
	// Probability 0 到 1；0 表示不按概率。
	Probability float64 `json:"probability,omitempty" yaml:"probability"`
	// Times 最多触发次数；0 表示不限。
	Times int `json:"times,omitempty" yaml:"times"`
	// After 仅在指定 ID 的规则已命中过之后才生效，用于编排"写失败后 NAS 掉线"这类序列。
	After string `json:"after,omitempty" yaml:"after"`
}

// Action 触发后的动作。
type Action struct {
	// Kind: error | delay | stall | throttle | short_write | corrupt | disconnect | mismatch | crash
	Kind string `json:"kind" yaml:"kind"`
	// Errno error 动作使用：EIO ENOSPC ENODEV EACCES ENOTCONN ETIMEDOUT EBUSY
	Errno string `json:"errno,omitempty" yaml:"errno"`
	// Ms delay / stall 的毫秒数。
	Ms int `json:"ms,omitempty" yaml:"ms"`
	// BytesPerSec throttle 的速率。
	BytesPerSec int64 `json:"bytes_per_sec,omitempty" yaml:"bytes_per_sec"`
	// Offset / Length corrupt 动作翻转的区间；Offset 为 -1 表示文件末尾前 Length 字节。
	Offset int64 `json:"offset,omitempty" yaml:"offset"`
	Length int64 `json:"length,omitempty" yaml:"length"`
}

// Match 适用范围。
type Match struct {
	// PathGlob 对任务相对路径做 path.Match；空表示全部。
	PathGlob string `json:"path_glob,omitempty" yaml:"path_glob"`
}

// Rule 一条注入规则。
type Rule struct {
	ID      string  `json:"id" yaml:"id"`
	Point   Point   `json:"point" yaml:"point"`
	Trigger Trigger `json:"trigger" yaml:"trigger"`
	Action  Action  `json:"action" yaml:"action"`
	Match   Match   `json:"match" yaml:"match"`
}

// AllPoints 全部注入点。
var AllPoints = []Point{SourceOpen, SourceRead, DeviceDetach, DestProbe, DestOpen, DestWrite, DestSync, DestRename, PartCorrupt, VerifyHash, DBCommit, ProcessCrash}

var actionKinds = map[string]bool{
	"error": true, "delay": true, "stall": true, "throttle": true, "short_write": true,
	"corrupt": true, "disconnect": true, "mismatch": true, "crash": true,
}

// Validate 校验规则。
func (r Rule) Validate() error {
	if strings.TrimSpace(r.ID) == "" {
		return fmt.Errorf("fault rule: id is required")
	}
	valid := false
	for _, p := range AllPoints {
		if p == r.Point {
			valid = true
		}
	}
	if !valid {
		return fmt.Errorf("fault rule %s: unknown point %q", r.ID, r.Point)
	}
	if !actionKinds[r.Action.Kind] {
		return fmt.Errorf("fault rule %s: unknown action kind %q", r.ID, r.Action.Kind)
	}
	if r.Trigger.Probability < 0 || r.Trigger.Probability > 1 {
		return fmt.Errorf("fault rule %s: probability must be within 0..1", r.ID)
	}
	if r.Trigger.NthCall < 0 || r.Trigger.AfterBytes < 0 || r.Trigger.Times < 0 {
		return fmt.Errorf("fault rule %s: trigger values must be >= 0", r.ID)
	}
	if r.Action.Kind == "throttle" && r.Action.BytesPerSec <= 0 {
		return fmt.Errorf("fault rule %s: throttle requires bytes_per_sec > 0", r.ID)
	}
	if r.Match.PathGlob != "" {
		if _, err := path.Match(r.Match.PathGlob, "x"); err != nil {
			return fmt.Errorf("fault rule %s: bad path_glob: %w", r.ID, err)
		}
	}
	return nil
}

// Status 规则及计数。
type Status struct {
	Rule  Rule `json:"rule"`
	Calls int  `json:"calls"`
	Hits  int  `json:"hits"`
}

// HitRecord 一次命中。
type HitRecord struct {
	RuleID string
	Point  Point
	Meta   HitMeta
	Action string
}

// Controller 运行时管理规则；production 构建不提供。
type Controller interface {
	Injector
	Rules() []Status
	Add(r Rule) error
	Remove(id string) bool
	Reset()
	SetOnHit(fn func(HitRecord))
	SetDisconnect(fn func(ctx context.Context) error)
}
