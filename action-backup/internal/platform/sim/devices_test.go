package sim

import (
	"context"
	"testing"
	"time"

	"action-backup/internal/config"
	"action-backup/internal/platform/fsinfo"
	"action-backup/internal/platform/raw"
)

func fixtures(t *testing.T) config.SimFixturesConfig {
	t.Helper()
	dir := t.TempDir()
	return config.SimFixturesConfig{
		AttachedOnStart: true,
		Devices: []config.SimDevice{{
			Key: "cam", Serial: "SIM0001", Name: "Sim",
			Volumes: []config.SimVolume{{Type: "internal", Path: dir}, {Type: "sdcard", Path: dir}},
		}},
	}
}

func TestDiscoverAndIdentity(t *testing.T) {
	b, err := New(fixtures(t), config.DeviceConfig{VendorID: "0506", ProductID: "8002"})
	if err != nil {
		t.Fatal(err)
	}
	devs, _ := b.Discover(context.Background())
	if len(devs) != 1 {
		t.Fatalf("want 1 device, got %d", len(devs))
	}
	d := devs[0]
	if d.Key != "0506:8002:SIM0001" || d.WeakIdentity || d.SerialNumber != "SIM0001" {
		t.Errorf("identity: %+v", d)
	}
	if len(d.Volumes) != 2 || d.Volumes[0].LUN != 0 || d.Volumes[1].LUN != 1 || d.Volumes[0].Label != "OsmoAction" {
		t.Errorf("volumes: %+v", d.Volumes)
	}
	if _, err := b.MountReadOnly(context.Background(), d.Volumes[0]); err != nil {
		t.Errorf("mount: %v", err)
	}
}

func TestAttachDetachEvents(t *testing.T) {
	b, _ := New(fixtures(t), config.DeviceConfig{VendorID: "0506", ProductID: "8002"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, _ := b.Watch(ctx)
	if err := b.Detach("cam"); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-ch:
		if ev.Kind != raw.Detached || ev.Device.Key != "0506:8002:SIM0001" {
			t.Fatalf("bad event %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("no detach event")
	}
	if devs, _ := b.Discover(ctx); len(devs) != 0 {
		t.Fatal("detached device still discovered")
	}
	// 重复 detach 不发事件
	_ = b.Detach("cam")
	select {
	case ev := <-ch:
		t.Fatalf("unexpected event %+v", ev)
	case <-time.After(50 * time.Millisecond):
	}
	if err := b.Attach("cam"); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-ch:
		if ev.Kind != raw.Attached {
			t.Fatalf("bad event %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("no attach event")
	}
	if err := b.Attach("nope"); err == nil {
		t.Fatal("unknown key must error")
	}
}

func TestLocalProbe(t *testing.T) {
	dir := t.TempDir()
	info, err := (LocalProbe{}).Probe(context.Background(), dir, fsinfo.Expectation{Kind: "local"})
	if err != nil || !info.Matched || info.FSType != "local" {
		t.Fatalf("probe: %+v %v", info, err)
	}
	if _, err := (LocalProbe{}).Probe(context.Background(), dir+"/missing", fsinfo.Expectation{Kind: "local"}); err == nil {
		t.Fatal("missing dir must fail")
	}
	if _, err := (LocalProbe{}).Probe(context.Background(), dir, fsinfo.Expectation{Kind: "cifs"}); err == nil {
		t.Fatal("local dir must not satisfy cifs expectation")
	}
}
