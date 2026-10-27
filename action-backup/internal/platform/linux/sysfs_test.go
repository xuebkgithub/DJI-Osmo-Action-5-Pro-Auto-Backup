package linux

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeTree 按 OEC-Turbo 实测（2026-10-27）伪造 sysfs 与 udev 数据库：
// usb 6-1（0506:8002，product=OsmoAction5pro_SN:0A1B2C3D）→ 接口 6-1:1.1 → host1 → target1:0:0 → 1:0:0:0/block/sda、1:0:0:1/block/sdb。
type fakeTree struct {
	Sysfs
	usbDev string // /sys/devices/.../usb6/6-1
}

func writeAttr(t *testing.T, dir, name, value string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(value+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newFakeTree(t *testing.T) fakeTree {
	t.Helper()
	root := t.TempDir()
	devs := filepath.Join(root, "devices", "platform", "usbhost", "fd000000.usb", "xhci-hcd.1.auto", "usb6")
	usb := filepath.Join(devs, "6-1")
	writeAttr(t, usb, "idVendor", "0506")
	writeAttr(t, usb, "idProduct", "8002")
	writeAttr(t, usb, "serial", "123456789ABCDEF")
	writeAttr(t, usb, "product", "OsmoAction5pro_SN:0A1B2C3D")
	writeAttr(t, usb, "manufacturer", "DJI")
	// 一个无关的 USB 设备（根集线器）
	hub := filepath.Join(devs, "usb6")
	writeAttr(t, hub, "idVendor", "1d6b")
	writeAttr(t, hub, "idProduct", "0003")
	// power/ 之类无关目录不应被遍历进去
	writeAttr(t, filepath.Join(usb, "power"), "control", "on")
	busUSB := filepath.Join(root, "bus", "usb", "devices")
	if err := os.MkdirAll(busUSB, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"6-1", "usb6"} {
		if err := os.Symlink(filepath.Join(devs, n), filepath.Join(busUSB, n)); err != nil {
			t.Fatal(err)
		}
	}
	tr := fakeTree{Sysfs: Sysfs{Root: root, UdevData: filepath.Join(root, "udev")}, usbDev: usb}
	if err := os.MkdirAll(tr.UdevData, 0o755); err != nil {
		t.Fatal(err)
	}
	return tr
}

// addLUN 增加一个 LUN；blockName 为空表示块设备尚未出现。
func (tr fakeTree) addLUN(t *testing.T, lun int, blockName string, sectors int64, devNumber string, props map[string]string) {
	t.Helper()
	lunDir := filepath.Join(tr.usbDev, "6-1:1.1", "host1", "target1:0:0", "1:0:0:"+itoa(lun))
	writeAttr(t, lunDir, "type", "0")
	if blockName == "" {
		return
	}
	blk := filepath.Join(lunDir, "block", blockName)
	writeAttr(t, blk, "size", itoa64(sectors))
	writeAttr(t, blk, "dev", devNumber)
	if props == nil {
		return
	}
	var sb strings.Builder
	sb.WriteString("S:disk/by-path/x\nI:1\n")
	for k, v := range props {
		sb.WriteString("E:" + k + "=" + v + "\n")
	}
	sb.WriteString("G:systemd\nV:1\n")
	if err := os.WriteFile(filepath.Join(tr.UdevData, "b"+devNumber), []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func itoa(i int) string     { return itoa64(int64(i)) }
func itoa64(i int64) string { return strings.TrimSpace(strings.Repeat(" ", 0) + fmtInt(i)) }
func fmtInt(i int64) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}

func cameraProps(uuid, label string) map[string]string {
	p := map[string]string{"ID_VENDOR_ID": "0506", "ID_MODEL_ID": "8002", "ID_FS_TYPE": "exfat", "ID_FS_UUID": uuid, "ID_BUS": "usb"}
	if label != "" {
		p["ID_FS_LABEL"] = label
	}
	return p
}

func TestFindUSBDevices(t *testing.T) {
	tr := newFakeTree(t)
	devs, err := tr.findUSBDevices("0506", "8002")
	if err != nil {
		t.Fatal(err)
	}
	if len(devs) != 1 || devs[0].Name != "6-1" || devs[0].Product != "OsmoAction5pro_SN:0A1B2C3D" || devs[0].Manufacturer != "DJI" || devs[0].Serial != "123456789ABCDEF" {
		t.Fatalf("unexpected %+v", devs)
	}
	if want, _ := filepath.EvalSymlinks(tr.usbDev); devs[0].Path != want {
		t.Errorf("path %s, want resolved %s", devs[0].Path, want)
	}
	if other, _ := tr.findUSBDevices("0506", "9999"); len(other) != 0 {
		t.Error("other PID must not match")
	}
}

func TestVolumesOf(t *testing.T) {
	tr := newFakeTree(t)
	tr.addLUN(t, 1, "sdb", 243474432, "8:16", cameraProps("0123-4567", ""))
	tr.addLUN(t, 0, "sda", 100573896, "8:0", cameraProps("F59D-F69A", "OsmoAction"))
	devs, _ := tr.findUSBDevices("0506", "8002")
	vols, err := tr.volumesOf(devs[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(vols) != 2 {
		t.Fatalf("want 2 volumes, got %+v", vols)
	}
	if vols[0].LUN != 0 || vols[0].Name != "sda" || vols[0].SizeBytes != 100573896*512 || vols[0].Props["ID_FS_LABEL"] != "OsmoAction" {
		t.Errorf("lun0 %+v", vols[0])
	}
	if vols[1].LUN != 1 || vols[1].Name != "sdb" || vols[1].Props["ID_FS_UUID"] != "0123-4567" {
		t.Errorf("lun1 %+v", vols[1])
	}
}

func TestVolumesOfNotReady(t *testing.T) {
	tr := newFakeTree(t)
	tr.addLUN(t, 0, "sda", 100573896, "8:0", cameraProps("F59D-F69A", "OsmoAction"))
	tr.addLUN(t, 1, "", 0, "", nil) // LUN 出现了但 sd 还没探到
	devs, _ := tr.findUSBDevices("0506", "8002")
	if _, err := tr.volumesOf(devs[0]); err == nil || !strings.Contains(err.Error(), "not ready") {
		t.Fatalf("want not ready, got %v", err)
	}
	// 块设备有了但 udev 还没写数据库
	tr2 := newFakeTree(t)
	tr2.addLUN(t, 0, "sda", 100573896, "8:0", nil)
	devs, _ = tr2.findUSBDevices("0506", "8002")
	if _, err := tr2.volumesOf(devs[0]); err == nil || !strings.Contains(err.Error(), "udev") {
		t.Fatalf("want udev not ready, got %v", err)
	}
}

func TestVolumesOfNoMedium(t *testing.T) {
	tr := newFakeTree(t)
	tr.addLUN(t, 0, "sda", 100573896, "8:0", cameraProps("F59D-F69A", "OsmoAction"))
	tr.addLUN(t, 1, "sdb", 0, "8:16", nil) // 没插卡：size 0，udev 无文件系统属性
	devs, _ := tr.findUSBDevices("0506", "8002")
	vols, err := tr.volumesOf(devs[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(vols) != 1 || vols[0].LUN != 0 {
		t.Fatalf("want only lun0, got %+v", vols)
	}
}

func TestBlockOwner(t *testing.T) {
	tr := newFakeTree(t)
	tr.addLUN(t, 0, "sda", 100573896, "8:0", cameraProps("F59D-F69A", "OsmoAction"))
	if _, v, err := tr.blockOwner("sda", "0506", "8002"); err != nil || v.Name != "sda" {
		t.Fatalf("sda should belong to camera: %v %+v", err, v)
	}
	if _, _, err := tr.blockOwner("sdz", "0506", "8002"); err == nil {
		t.Error("sdz must be rejected")
	}
	if _, _, err := tr.blockOwner("sda", "0506", "9999"); err == nil {
		t.Error("other PID must be rejected")
	}
}

func TestUdevPropsMissing(t *testing.T) {
	if _, err := udevProps(t.TempDir(), "8:0"); !os.IsNotExist(err) {
		t.Fatalf("want not exist, got %v", err)
	}
}
