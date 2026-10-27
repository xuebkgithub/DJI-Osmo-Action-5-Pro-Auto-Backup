package linux

import (
	"os"
	"path/filepath"
	"testing"
)

// 实机 OEC-Turbo 开机后主服务沙箱里的 mountinfo（已脱敏）：
// ReadWritePaths 先绑定了还没挂 NAS 的本地目录，CIFS 随后由守护挂载并传播进来，叠在上面。
const stackedMountInfo = `577 568 179:7 / / rw,noatime,nodiratime shared:1 master:1 - ext4 /dev/mmcblk0p7 rw,errors=remount-ro,commit=600,data=writeback
609 616 179:7 /mnt/action-backup-nas /mnt/action-backup-nas rw,nosuid,noatime,nodiratime shared:328 master:1 - ext4 /dev/mmcblk0p7 rw,errors=remount-ro,commit=600,data=writeback
617 609 0:50 / /mnt/action-backup-nas rw,nosuid,nodev,noexec,relatime shared:333 master:306 - cifs //172.20.100.254/home rw,vers=3.1.1,cache=strict,username=REDACTED,uid=999
616 577 0:50 / /mnt/action-backup-nas rw,nosuid,nodev,noexec,relatime shared:332 master:306 - cifs //172.20.100.254/home rw,vers=3.1.1,cache=strict,username=REDACTED,uid=999
620 577 8:0 / /run/action-backup-mounter/source/sda ro,nosuid,nodev,noexec,relatime shared:340 master:310 - exfat /dev/sda ro,uid=999,gid=995
`

func writeMountInfo(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "mountinfo")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestMountEntryInPicksTopOfStack(t *testing.T) {
	p := writeMountInfo(t, stackedMountInfo)
	src, fstype, found := mountEntryIn(p, "/mnt/action-backup-nas")
	if !found {
		t.Fatal("mount point not found")
	}
	if fstype != "cifs" || src != "//172.20.100.254/home" {
		t.Errorf("got %s %q, want cifs //172.20.100.254/home (top of stack, not the ReadWritePaths bind)", fstype, src)
	}
	src, fstype, found = mountEntryIn(p, "/run/action-backup-mounter/source/sda")
	if !found || fstype != "exfat" || src != "/dev/sda" {
		t.Errorf("single entry: got %s %q found=%v", fstype, src, found)
	}
	if _, _, found = mountEntryIn(p, "/mnt/action-backup-nas/sub"); found {
		t.Error("subdirectory must not match")
	}
}

func TestMountEntryInEscapes(t *testing.T) {
	p := writeMountInfo(t, "30 1 0:40 / /mnt/my\\040nas rw - cifs //nas/my\\040share rw\n")
	src, fstype, found := mountEntryIn(p, "/mnt/my nas")
	if !found || fstype != "cifs" || src != "//nas/my share" {
		t.Errorf("got %s %q found=%v", fstype, src, found)
	}
}
