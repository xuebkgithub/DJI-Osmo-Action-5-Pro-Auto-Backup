//go:build faultinject

package faultinject

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func mustNew(t *testing.T, rules ...Rule) Controller {
	t.Helper()
	c, err := New(rules)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestValidate(t *testing.T) {
	bad := []Rule{
		{ID: "", Point: DestWrite, Action: Action{Kind: "error"}},
		{ID: "x", Point: "nope", Action: Action{Kind: "error"}},
		{ID: "x", Point: DestWrite, Action: Action{Kind: "explode"}},
		{ID: "x", Point: DestWrite, Action: Action{Kind: "throttle"}},
		{ID: "x", Point: DestWrite, Trigger: Trigger{Probability: 2}, Action: Action{Kind: "error"}},
		{ID: "x", Point: DestWrite, Action: Action{Kind: "error"}, Match: Match{PathGlob: "["}},
	}
	for i, r := range bad {
		if err := r.Validate(); err == nil {
			t.Errorf("rule %d should be invalid", i)
		}
	}
	if _, err := New([]Rule{{ID: "a", Point: DestWrite, Action: Action{Kind: "error"}}, {ID: "a", Point: DestWrite, Action: Action{Kind: "error"}}}); err == nil {
		t.Error("duplicate id must fail")
	}
}

func TestTriggers(t *testing.T) {
	ctx := context.Background()
	c := mustNew(t,
		Rule{ID: "once", Point: DestWrite, Trigger: Trigger{Once: true}, Action: Action{Kind: "error", Errno: "ENOSPC"}},
		Rule{ID: "nth", Point: SourceRead, Trigger: Trigger{NthCall: 3}, Action: Action{Kind: "error", Errno: "EIO"}},
		Rule{ID: "bytes", Point: DestSync, Trigger: Trigger{AfterBytes: 1000, Times: 2}, Action: Action{Kind: "error"}},
		Rule{ID: "glob", Point: DestOpen, Match: Match{PathGlob: "DCIM/*/*.MP4"}, Action: Action{Kind: "error", Errno: "EACCES"}},
	)
	if err := c.Hit(ctx, DestWrite, HitMeta{}); !errors.Is(err, syscall.ENOSPC) {
		t.Fatalf("once first: %v", err)
	}
	if err := c.Hit(ctx, DestWrite, HitMeta{}); err != nil {
		t.Fatalf("once second must not fire: %v", err)
	}
	for i := 1; i <= 4; i++ {
		err := c.Hit(ctx, SourceRead, HitMeta{})
		if (i == 3) != errors.Is(err, syscall.EIO) {
			t.Fatalf("nth call %d: %v", i, err)
		}
	}
	for i, b := range []int64{10, 999, 1000, 5000, 9000} {
		err := c.Hit(ctx, DestSync, HitMeta{Bytes: b})
		want := i == 2 || i == 3
		if (err != nil) != want {
			t.Fatalf("after_bytes call %d (%d): %v", i, b, err)
		}
	}
	if err := c.Hit(ctx, DestOpen, HitMeta{RelativePath: "DCIM/DJI_003/a.MP4"}); !errors.Is(err, syscall.EACCES) {
		t.Fatalf("glob match: %v", err)
	}
	if err := c.Hit(ctx, DestOpen, HitMeta{RelativePath: "DCIM/DJI_003/a.LRF"}); err != nil {
		t.Fatalf("glob miss: %v", err)
	}
	st := c.Rules()
	if len(st) != 4 || st[0].Hits != 1 || st[0].Calls != 2 || st[1].Hits != 1 || st[2].Hits != 2 {
		t.Fatalf("status %+v", st)
	}
	// after：依赖规则命中前不生效
	d := mustNew(t,
		Rule{ID: "first", Point: DestWrite, Trigger: Trigger{Once: true}, Action: Action{Kind: "error"}},
		Rule{ID: "second", Point: DestProbe, Trigger: Trigger{After: "first"}, Action: Action{Kind: "error", Errno: "ENOTCONN"}},
	)
	if err := d.Hit(ctx, DestProbe, HitMeta{}); err != nil {
		t.Fatalf("after: must not fire before dependency: %v", err)
	}
	_ = d.Hit(ctx, DestWrite, HitMeta{})
	if err := d.Hit(ctx, DestProbe, HitMeta{}); !errors.Is(err, syscall.ENOTCONN) {
		t.Fatalf("after: must fire once dependency hit: %v", err)
	}
	if !c.Remove("once") || c.Remove("once") {
		t.Fatal("remove")
	}
	c.Reset()
	if len(c.Rules()) != 0 {
		t.Fatal("reset")
	}
}

func TestActions(t *testing.T) {
	ctx := context.Background()
	hits := 0
	disconnected := false
	c := mustNew(t,
		Rule{ID: "delay", Point: DestWrite, Action: Action{Kind: "delay", Ms: 30}},
		Rule{ID: "throttle", Point: SourceRead, Action: Action{Kind: "throttle", BytesPerSec: 1 << 20}},
		Rule{ID: "mismatch", Point: VerifyHash, Action: Action{Kind: "mismatch"}},
		Rule{ID: "disc", Point: DeviceDetach, Action: Action{Kind: "disconnect"}},
		Rule{ID: "short", Point: DestRename, Action: Action{Kind: "short_write"}},
	)
	c.SetOnHit(func(HitRecord) { hits++ })
	c.SetDisconnect(func(context.Context) error { disconnected = true; return nil })
	start := time.Now()
	if err := c.Hit(ctx, DestWrite, HitMeta{}); err != nil || time.Since(start) < 25*time.Millisecond {
		t.Fatalf("delay: %v %v", err, time.Since(start))
	}
	_ = c.Hit(ctx, SourceRead, HitMeta{Bytes: 0})
	start = time.Now()
	_ = c.Hit(ctx, SourceRead, HitMeta{Bytes: 512 << 10}) // 0.5 MiB at 1 MiB/s ≈ 0.5 s
	if d := time.Since(start); d < 400*time.Millisecond {
		t.Fatalf("throttle too fast: %v", d)
	}
	if err := c.Hit(ctx, VerifyHash, HitMeta{}); !errors.Is(err, ErrMismatch) {
		t.Fatalf("mismatch: %v", err)
	}
	if err := c.Hit(ctx, DeviceDetach, HitMeta{}); !errors.Is(err, syscall.ENODEV) || !disconnected {
		t.Fatalf("disconnect: %v %v", err, disconnected)
	}
	if err := c.Hit(ctx, DestRename, HitMeta{}); err == nil {
		t.Fatal("short_write")
	}
	if hits != 6 {
		t.Fatalf("hits %d", hits)
	}
	// stall 受 ctx 取消控制
	s := mustNew(t, Rule{ID: "stall", Point: DestWrite, Action: Action{Kind: "stall", Ms: 10000}})
	cctx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if err := s.Hit(cctx, DestWrite, HitMeta{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stall: %v", err)
	}
}

func TestCorrupt(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.part")
	data := make([]byte, 4096)
	for i := range data {
		data[i] = byte(i)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	c := mustNew(t, Rule{ID: "c", Point: PartCorrupt, Action: Action{Kind: "corrupt", Offset: -1, Length: 8}})
	if err := c.Hit(context.Background(), PartCorrupt, HitMeta{Path: p}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(p)
	for i := 0; i < 4088; i++ {
		if got[i] != data[i] {
			t.Fatalf("byte %d changed unexpectedly", i)
		}
	}
	for i := 4088; i < 4096; i++ {
		if got[i] != data[i]^0xFF {
			t.Fatalf("byte %d not flipped", i)
		}
	}
	if err := c.Hit(context.Background(), PartCorrupt, HitMeta{}); err == nil {
		t.Fatal("missing path must error")
	}
}
