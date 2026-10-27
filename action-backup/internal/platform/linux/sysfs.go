package linux

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// DefaultSysfs sysfs 挂载点。
const DefaultSysfs = "/sys"

// 实测（OEC-Turbo，2026-10-27）：相机 USB 设备 /sys/bus/usb/devices/6-1，idVendor=0506、idProduct=8002，
// serial 为占位 123456789ABCDEF，product=OsmoAction5pro_SN:0A1B2C3D；usb-storage 把两个 LUN 暴露为
// /sys/bus/scsi/devices/1:0:0:0（block/sda，48 GB，exfat，LABEL=OsmoAction）与 1:0:0:1（block/sdb，116 GB，exfat，无卷标）。
// 两个卷都是整盘 exFAT，没有分区表条目可用。

// usbDevice sysfs 中匹配 VID/PID 的 USB 设备。
type usbDevice struct {
	// Path 形如 /sys/bus/usb/devices/6-1 的真实路径（已解析符号链接）。
	Path         string
	Name         string // sysfs 节点名，如 6-1
	Serial       string
	Product      string
	Manufacturer string
}

// blockVolume 挂在某个 USB 设备下的整盘块设备。
type blockVolume struct {
	Name      string // sda
	DevNumber string // 8:0
	LUN       int
	SizeBytes int64
	Props     map[string]string // udev 属性，可能为空（udev 尚未处理）
}

var scsiAddrRe = regexp.MustCompile(`^(\d+):(\d+):(\d+):(\d+)$`)

// Sysfs 封装根路径，便于用临时目录伪造的树做测试。
type Sysfs struct {
	Root     string // /sys
	UdevData string // /run/udev/data
}

// DefaultSysfsTree 真实系统。
func DefaultSysfsTree() Sysfs { return Sysfs{Root: DefaultSysfs, UdevData: DefaultUdevData} }

func (s Sysfs) attr(dir, name string) string {
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// findUSBDevices 列出 /sys/bus/usb/devices 下 idVendor/idProduct 匹配的设备（不含接口节点）。
func (s Sysfs) findUSBDevices(vendorID, productID string) ([]usbDevice, error) {
	base := filepath.Join(s.Root, "bus", "usb", "devices")
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil, fmt.Errorf("list usb devices: %w", err)
	}
	var out []usbDevice
	for _, e := range entries {
		dir := filepath.Join(base, e.Name())
		if !strings.EqualFold(s.attr(dir, "idVendor"), vendorID) || !strings.EqualFold(s.attr(dir, "idProduct"), productID) {
			continue
		}
		real, err := filepath.EvalSymlinks(dir)
		if err != nil {
			real = dir
		}
		out = append(out, usbDevice{
			Path: real, Name: e.Name(),
			Serial: s.attr(dir, "serial"), Product: s.attr(dir, "product"), Manufacturer: s.attr(dir, "manufacturer"),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// scsiLUNs 列出 USB 设备子树里的 SCSI 逻辑单元目录（h:c:t:l）。
func (s Sysfs) scsiLUNs(dev usbDevice) ([]string, error) {
	var out []string
	err := filepath.WalkDir(dev.Path, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if scsiAddrRe.MatchString(d.Name()) {
			out = append(out, path)
			return filepath.SkipDir
		}
		// 只在 USB 接口 → host → target 这几层向下走，避免遍历 power/ 等无关目录。
		name := d.Name()
		if path != dev.Path && !strings.HasPrefix(name, dev.Name+":") && !strings.HasPrefix(name, "host") && !strings.HasPrefix(name, "target") {
			return filepath.SkipDir
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(out)
	return out, nil
}

// errNotReady 表示 LUN 已出现但块设备或 udev 属性尚未就绪，下一轮再看。
var errNotReady = errors.New("block device not ready")

// volumesOf 读取 USB 设备下每个 LUN 的块设备。任一 LUN 未就绪返回 errNotReady。
func (s Sysfs) volumesOf(dev usbDevice) ([]blockVolume, error) {
	luns, err := s.scsiLUNs(dev)
	if err != nil {
		return nil, err
	}
	if len(luns) == 0 {
		// USB 设备已出现但 usb-storage 还没扫出 LUN（或根本不是存储模式）
		return nil, fmt.Errorf("no scsi lun yet: %w", errNotReady)
	}
	var out []blockVolume
	for _, lunDir := range luns {
		m := scsiAddrRe.FindStringSubmatch(filepath.Base(lunDir))
		lun, _ := strconv.Atoi(m[4])
		if t := s.attr(lunDir, "type"); t != "" && t != "0" {
			continue // 非磁盘类型
		}
		blocks, err := os.ReadDir(filepath.Join(lunDir, "block"))
		if err != nil || len(blocks) == 0 {
			return nil, fmt.Errorf("lun %d: %w", lun, errNotReady)
		}
		name := blocks[0].Name()
		blockDir := filepath.Join(lunDir, "block", name)
		sectors, _ := strconv.ParseInt(s.attr(blockDir, "size"), 10, 64)
		devNumber := s.attr(blockDir, "dev")
		if devNumber == "" {
			return nil, fmt.Errorf("lun %d: %w", lun, errNotReady)
		}
		v := blockVolume{Name: name, DevNumber: devNumber, LUN: lun, SizeBytes: sectors * 512}
		if v.SizeBytes == 0 {
			// 无介质（例如没插 microSD）：udev 不会有文件系统属性，直接跳过这个卷。
			continue
		}
		props, err := udevProps(s.UdevData, devNumber)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, fmt.Errorf("lun %d (%s): udev %w", lun, name, errNotReady)
			}
			return nil, err
		}
		v.Props = props
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LUN < out[j].LUN })
	return out, nil
}

// blockOwner 反查块设备名属于哪个匹配 VID/PID 的 USB 设备，供挂载守护做白名单校验。
func (s Sysfs) blockOwner(blockName, vendorID, productID string) (usbDevice, blockVolume, error) {
	devs, err := s.findUSBDevices(vendorID, productID)
	if err != nil {
		return usbDevice{}, blockVolume{}, err
	}
	for _, d := range devs {
		vols, err := s.volumesOf(d)
		if err != nil {
			continue
		}
		for _, v := range vols {
			if v.Name == blockName {
				return d, v, nil
			}
		}
	}
	return usbDevice{}, blockVolume{}, fmt.Errorf("block device %s does not belong to a %s:%s camera", blockName, vendorID, productID)
}
