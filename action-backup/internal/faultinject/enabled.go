//go:build faultinject

package faultinject

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path"
	"sync"
	"syscall"
	"time"
)

// Enabled 当前二进制是否包含故障注入能力。
const Enabled = true

type ruleState struct {
	rule      Rule
	calls     int
	hits      int
	lastBytes int64
}

// Engine 真实注入器。
type Engine struct {
	mu         sync.Mutex
	rules      []*ruleState
	onHit      func(HitRecord)
	disconnect func(ctx context.Context) error
}

// New 创建注入器并载入初始规则。
func New(rules []Rule) (Controller, error) {
	e := &Engine{}
	for _, r := range rules {
		if err := e.Add(r); err != nil {
			return nil, err
		}
	}
	return e, nil
}

// SetOnHit 命中回调。
func (e *Engine) SetOnHit(fn func(HitRecord)) {
	e.mu.Lock()
	e.onHit = fn
	e.mu.Unlock()
}

// SetDisconnect disconnect 动作的回调（如模拟拔出设备）。
func (e *Engine) SetDisconnect(fn func(ctx context.Context) error) {
	e.mu.Lock()
	e.disconnect = fn
	e.mu.Unlock()
}

// Rules 当前规则与计数。
func (e *Engine) Rules() []Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]Status, 0, len(e.rules))
	for _, r := range e.rules {
		out = append(out, Status{Rule: r.rule, Calls: r.calls, Hits: r.hits})
	}
	return out
}

// Add 新增规则；ID 重复报错。
func (e *Engine) Add(r Rule) error {
	if err := r.Validate(); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, x := range e.rules {
		if x.rule.ID == r.ID {
			return fmt.Errorf("fault rule %s already exists", r.ID)
		}
	}
	e.rules = append(e.rules, &ruleState{rule: r})
	return nil
}

// Remove 删除规则。
func (e *Engine) Remove(id string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i, x := range e.rules {
		if x.rule.ID == id {
			e.rules = append(e.rules[:i], e.rules[i+1:]...)
			return true
		}
	}
	return false
}

// Reset 清空规则。
func (e *Engine) Reset() {
	e.mu.Lock()
	e.rules = nil
	e.mu.Unlock()
}

// Hit 评估注入点。多条规则命中时按顺序执行，第一个返回错误的动作终止后续。
func (e *Engine) Hit(ctx context.Context, point Point, meta HitMeta) error {
	e.mu.Lock()
	var fired []*ruleState
	for _, r := range e.rules {
		if r.rule.Point != point {
			continue
		}
		if g := r.rule.Match.PathGlob; g != "" {
			if ok, _ := path.Match(g, meta.RelativePath); !ok {
				continue
			}
		}
		r.calls++
		if !e.shouldFire(r, meta) || !e.afterSatisfied(r) {
			continue
		}
		r.hits++
		fired = append(fired, r)
	}
	onHit, disconnect := e.onHit, e.disconnect
	e.mu.Unlock()

	for _, r := range fired {
		if onHit != nil {
			onHit(HitRecord{RuleID: r.rule.ID, Point: point, Meta: meta, Action: r.rule.Action.Kind})
		}
		if err := e.act(ctx, r, meta, disconnect); err != nil {
			return err
		}
	}
	return nil
}

// afterSatisfied 调用方持有 e.mu。
func (e *Engine) afterSatisfied(r *ruleState) bool {
	if r.rule.Trigger.After == "" {
		return true
	}
	for _, x := range e.rules {
		if x.rule.ID == r.rule.Trigger.After {
			return x.hits > 0
		}
	}
	return false
}

func (e *Engine) shouldFire(r *ruleState, meta HitMeta) bool {
	t := r.rule.Trigger
	limit := t.Times
	if t.Once && limit == 0 {
		limit = 1
	}
	if t.NthCall > 0 && limit == 0 {
		limit = 1
	}
	if limit > 0 && r.hits >= limit {
		return false
	}
	if t.NthCall > 0 && r.calls != t.NthCall {
		return false
	}
	if t.AfterBytes > 0 && meta.Bytes < t.AfterBytes {
		return false
	}
	if t.Probability > 0 && rand.Float64() >= t.Probability {
		return false
	}
	return true
}

var errnos = map[string]syscall.Errno{
	"EIO": syscall.EIO, "ENOSPC": syscall.ENOSPC, "ENODEV": syscall.ENODEV, "EACCES": syscall.EACCES,
	"ENOTCONN": syscall.ENOTCONN, "ETIMEDOUT": syscall.ETIMEDOUT, "EBUSY": syscall.EBUSY, "EPERM": syscall.EPERM,
}

// ErrMismatch mismatch 动作返回的错误。
var ErrMismatch = errors.New("injected verification mismatch")

func (e *Engine) act(ctx context.Context, r *ruleState, meta HitMeta, disconnect func(context.Context) error) error {
	a := r.rule.Action
	switch a.Kind {
	case "error":
		errno, ok := errnos[a.Errno]
		if !ok {
			errno = syscall.EIO
		}
		return fmt.Errorf("fault %s at %s: %w", r.rule.ID, r.rule.Point, errno)
	case "delay":
		return sleepCtx(ctx, time.Duration(max(a.Ms, 1))*time.Millisecond)
	case "stall":
		ms := a.Ms
		if ms <= 0 {
			ms = 30000
		}
		return sleepCtx(ctx, time.Duration(ms)*time.Millisecond)
	case "throttle":
		e.mu.Lock()
		delta := meta.Bytes - r.lastBytes
		r.lastBytes = meta.Bytes
		e.mu.Unlock()
		if delta <= 0 {
			return nil
		}
		return sleepCtx(ctx, time.Duration(float64(delta)/float64(a.BytesPerSec)*float64(time.Second)))
	case "short_write":
		return fmt.Errorf("fault %s at %s: %w", r.rule.ID, r.rule.Point, io.ErrShortWrite)
	case "corrupt":
		return corrupt(meta.Path, a.Offset, a.Length)
	case "disconnect":
		if disconnect != nil {
			if err := disconnect(ctx); err != nil {
				return err
			}
		}
		return fmt.Errorf("fault %s at %s: %w", r.rule.ID, r.rule.Point, syscall.ENODEV)
	case "mismatch":
		return fmt.Errorf("fault %s: %w", r.rule.ID, ErrMismatch)
	case "crash":
		fmt.Fprintf(os.Stderr, "faultinject: rule %s crashing process at %s (bytes=%d)\n", r.rule.ID, r.rule.Point, meta.Bytes)
		os.Exit(137)
	}
	return nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-t.C:
		return nil
	}
}

// corrupt 翻转文件指定区间的字节。off 为 -1 时取文件末尾前 n 字节。
func corrupt(file string, off, n int64) error {
	if file == "" {
		return fmt.Errorf("corrupt: no file path in hit meta")
	}
	if n <= 0 {
		n = 16
	}
	f, err := os.OpenFile(file, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("corrupt: %w", err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if st.Size() == 0 {
		return nil
	}
	if off < 0 || off >= st.Size() {
		off = st.Size() - n
		if off < 0 {
			off = 0
		}
	}
	if off+n > st.Size() {
		n = st.Size() - off
	}
	buf := make([]byte, n)
	if _, err := f.ReadAt(buf, off); err != nil && err != io.EOF {
		return err
	}
	for i := range buf {
		buf[i] ^= 0xFF
	}
	if _, err := f.WriteAt(buf, off); err != nil {
		return err
	}
	return f.Sync()
}
