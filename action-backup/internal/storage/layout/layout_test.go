package layout

import (
	"strings"
	"testing"
	"time"
)

func vars() Vars {
	return Vars{
		RelativePath: "DCIM/DJI_003/DJI_20261023080221_0019_D.MP4",
		VolumeType:   "sdcard", VolumeLabel: "Untitled", Serial: "0A1B2C3D", DeviceName: "Osmo",
		MTime: time.Date(2026, 10, 23, 8, 50, 35, 0, time.UTC),
	}
}

func TestRender(t *testing.T) {
	cases := map[string]string{
		Default:                                  "sdcard/DCIM/DJI_003/DJI_20261023080221_0019_D.MP4",
		"{serial}/{volume_type}/{relative_path}": "0A1B2C3D/sdcard/DCIM/DJI_003/DJI_20261023080221_0019_D.MP4",
		"{mtime:2006}/{mtime:01}/{filename}":     "2026/10/DJI_20261023080221_0019_D.MP4",
		"{date}/{volume_label}/{filename}":       "2026-10-23/Untitled/DJI_20261023080221_0019_D.MP4",
		"flat/{filename}":                        "flat/DJI_20261023080221_0019_D.MP4",
	}
	for tpl, want := range cases {
		p, err := Parse(tpl)
		if err != nil {
			t.Fatalf("parse %q: %v", tpl, err)
		}
		got, err := p.Render(vars())
		if err != nil {
			t.Fatalf("render %q: %v", tpl, err)
		}
		if got != want {
			t.Errorf("%q => %q, want %q", tpl, got, want)
		}
	}
}

func TestParseRejects(t *testing.T) {
	for _, tpl := range []string{"", "/abs/{relative_path}", "{volume_type}", "{nope}/{filename}", "{mtime}/{filename}", "../{filename}", "{filename"} {
		if _, err := Parse(tpl); err == nil {
			t.Errorf("Parse(%q) should fail", tpl)
		}
	}
}

func TestRenderRejectsEscapes(t *testing.T) {
	p, _ := Parse("{serial}/{relative_path}")
	v := vars()
	v.Serial = ".."
	if _, err := p.Render(v); err == nil || !strings.Contains(err.Error(), "..") {
		t.Fatalf("expected '..' rejection, got %v", err)
	}
	v = vars()
	v.RelativePath = "/etc/passwd"
	if _, err := p.Render(v); err == nil {
		t.Fatal("expected absolute path rejection")
	}
	v = vars()
	v.RelativePath = "a//b.MP4"
	if _, err := p.Render(v); err == nil {
		t.Fatal("expected empty segment rejection")
	}
	v = vars()
	v.Serial = ""
	got, err := p.Render(v)
	if err != nil || !strings.HasPrefix(got, "unknown/") {
		t.Fatalf("empty serial should fall back to unknown, got %q %v", got, err)
	}
}
