package fsinfo

import "testing"

func TestShareMatches(t *testing.T) {
	cases := []struct {
		want, src string
		ok        bool
	}{
		{"//192.168.1.100/VideoBackup", "//evan@192.168.1.100/VideoBackup", true},
		{"//192.168.1.100/VideoBackup", "//192.168.1.100/videobackup/", true},
		{"//192.168.1.100/VideoBackup", "//192.168.1.100/Other", false},
		{"//nas.local/VideoBackup", "//192.168.1.100/VideoBackup", false},
		{"", "anything", true},
		{`\\192.168.1.100\VideoBackup`, "//192.168.1.100/VideoBackup", true},
	}
	for _, c := range cases {
		if got := ShareMatches(c.want, c.src); got != c.ok {
			t.Errorf("ShareMatches(%q,%q)=%v want %v", c.want, c.src, got, c.ok)
		}
	}
}

func TestMatches(t *testing.T) {
	share := "//192.168.1.100/VideoBackup"
	if !Matches(Expectation{Kind: "network", Share: share}, "smbfs", "//u@192.168.1.100/VideoBackup", true) {
		t.Error("smbfs satisfies network")
	}
	if !Matches(Expectation{Kind: "cifs", Share: share}, "cifs", share, true) {
		t.Error("cifs satisfies cifs")
	}
	if Matches(Expectation{Kind: "cifs", Share: share}, "ext4", "/dev/mmcblk0p2", true) {
		t.Error("local fs must not satisfy cifs: NAS disconnected but directory remains")
	}
	if Matches(Expectation{Kind: "network", Share: share}, "cifs", share, false) {
		t.Error("subdirectory of a mount must not be accepted as the mount point")
	}
	if Matches(Expectation{Kind: "smbfs", Share: share}, "cifs", share, true) {
		t.Error("smbfs expectation rejects cifs")
	}
	if !Matches(Expectation{Kind: "local"}, "apfs", "/dev/disk3s1", false) {
		t.Error("local accepts anything")
	}
	if Matches(Expectation{Kind: "bogus"}, "cifs", share, true) {
		t.Error("unknown kind rejected")
	}
}
