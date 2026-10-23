package plist

import (
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	src := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Name</key><string>OsmoAction</string>
	<key>Size</key><integer>51493834752</integer>
	<key>Ratio</key><real>0.5</real>
	<key>Whole</key><true/>
	<key>Empty</key><false/>
	<key>Blob</key><data>
	aGVsbG8=
	</data>
	<key>When</key><date>2026-10-23T02:01:56Z</date>
	<key>Children</key>
	<array>
		<dict><key>BSD Name</key><string>disk4</string></dict>
		<string>x</string>
		<integer>-1</integer>
	</array>
	<key>Big</key><integer>18446744073709551615</integer>
</dict>
</plist>`
	v, err := Parse(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	m, ok := Dict(v)
	if !ok {
		t.Fatalf("top is %T", v)
	}
	if String(m, "Name") != "OsmoAction" {
		t.Error("string")
	}
	if n, ok := Int(m, "Size"); !ok || n != 51493834752 {
		t.Error("integer")
	}
	if r, _ := m["Ratio"].(float64); r != 0.5 {
		t.Error("real")
	}
	if !Bool(m, "Whole") || Bool(m, "Empty") {
		t.Error("bool")
	}
	if string(m["Blob"].([]byte)) != "hello" {
		t.Error("data")
	}
	if _, ok := m["When"]; !ok {
		t.Error("date")
	}
	a := Array(m, "Children")
	if len(a) != 3 {
		t.Fatalf("array len %d", len(a))
	}
	if c, _ := Dict(a[0]); String(c, "BSD Name") != "disk4" {
		t.Error("nested dict")
	}
	if a[2].(int64) != -1 {
		t.Error("negative")
	}
	if n, _ := Int(m, "Big"); n != -1 {
		t.Errorf("uint64 max should wrap to -1, got %d", n)
	}
}

func TestParseErrors(t *testing.T) {
	for _, src := range []string{"", "<plist></plist>", "<plist><dict><string>no key</string></dict></plist>", "<plist><integer>x</integer></plist>"} {
		if _, err := Parse(strings.NewReader(src)); err == nil {
			t.Errorf("%q should fail", src)
		}
	}
}
