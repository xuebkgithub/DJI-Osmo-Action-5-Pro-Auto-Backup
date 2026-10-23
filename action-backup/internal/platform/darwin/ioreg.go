//go:build darwin

package darwin

import (
	"bytes"
	"fmt"
	"strings"

	"action-backup/internal/platform/plist"
)

// usbNode ioreg IOUSB 平面中匹配 VID/PID 的设备。
type usbNode struct {
	EntryName   string
	ProductName string
	VendorName  string
	Serial      string
	EntryID     int64
	LocationID  int64
	DeviceSpeed int64
}

// lunNode ioreg IOService 子树中的逻辑单元及其块设备。
type lunNode struct {
	LUN     int
	BSDName string
	Size    int64
}

// diskInfo diskutil info -plist 的关键字段。
type diskInfo struct {
	Device         string
	VolumeName     string
	MountPoint     string
	FilesystemType string
	Size           int64
	VolumeUUID     string
	MediaName      string
	BusProtocol    string
	WholeDisk      bool
}

// findUSBDevices 在 ioreg -p IOUSB -a -l 的输出中查找 idVendor/idProduct 匹配的节点。
func findUSBDevices(data []byte, vendorID, productID int64) ([]usbNode, error) {
	root, err := plist.Parse(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("parse ioreg IOUSB: %w", err)
	}
	var out []usbNode
	walk(root, func(n map[string]any) bool {
		vid, ok1 := plist.Int(n, "idVendor")
		pid, ok2 := plist.Int(n, "idProduct")
		if !ok1 || !ok2 || vid != vendorID || pid != productID {
			return true
		}
		node := usbNode{
			EntryName:   plist.String(n, "IORegistryEntryName"),
			ProductName: plist.String(n, "USB Product Name"),
			VendorName:  plist.String(n, "USB Vendor Name"),
			Serial:      plist.String(n, "USB Serial Number"),
		}
		node.EntryID, _ = plist.Int(n, "IORegistryEntryID")
		node.LocationID, _ = plist.Int(n, "locationID")
		node.DeviceSpeed, _ = plist.Int(n, "Device Speed")
		out = append(out, node)
		return false // 不再深入该设备的子节点
	})
	return out, nil
}

// findLUNs 在 ioreg -p IOService -r -n <name> 的子树中收集 IOSCSILogicalUnitNub 及其下的整盘 IOMedia。
func findLUNs(data []byte) ([]lunNode, error) {
	root, err := plist.Parse(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("parse ioreg subtree: %w", err)
	}
	var out []lunNode
	walk(root, func(n map[string]any) bool {
		if plist.String(n, "IOObjectClass") != "IOSCSILogicalUnitNub" {
			return true
		}
		lun, ok := plist.Int(n, "SCSI Logical Unit Number")
		if !ok {
			return true
		}
		node := lunNode{LUN: int(lun)}
		walk(n, func(m map[string]any) bool {
			if plist.String(m, "IOObjectClass") == "IOMedia" && plist.Bool(m, "Whole") {
				if bsd := plist.String(m, "BSD Name"); bsd != "" && node.BSDName == "" {
					node.BSDName = bsd
					node.Size, _ = plist.Int(m, "Size")
					return false
				}
			}
			return true
		})
		out = append(out, node)
		return false
	})
	return out, nil
}

// parseDiskInfo 解析 diskutil info -plist 的输出。
func parseDiskInfo(data []byte) (diskInfo, error) {
	root, err := plist.Parse(bytes.NewReader(data))
	if err != nil {
		return diskInfo{}, fmt.Errorf("parse diskutil info: %w", err)
	}
	m, ok := plist.Dict(root)
	if !ok {
		return diskInfo{}, fmt.Errorf("diskutil info: top level is not a dict")
	}
	info := diskInfo{
		Device:         plist.String(m, "DeviceIdentifier"),
		VolumeName:     plist.String(m, "VolumeName"),
		MountPoint:     plist.String(m, "MountPoint"),
		FilesystemType: strings.ToLower(plist.String(m, "FilesystemType")),
		VolumeUUID:     plist.String(m, "VolumeUUID"),
		MediaName:      plist.String(m, "MediaName"),
		BusProtocol:    plist.String(m, "BusProtocol"),
		WholeDisk:      plist.Bool(m, "WholeDisk"),
	}
	info.Size, _ = plist.Int(m, "Size")
	if info.Size == 0 {
		info.Size, _ = plist.Int(m, "TotalSize")
	}
	return info, nil
}

// walk 深度优先遍历 ioreg 树；visit 返回 false 时不进入该节点的子节点。
func walk(v any, visit func(map[string]any) bool) {
	switch n := v.(type) {
	case map[string]any:
		if !visit(n) {
			return
		}
		for _, c := range plist.Array(n, "IORegistryEntryChildren") {
			walk(c, visit)
		}
	case []any:
		for _, c := range n {
			walk(c, visit)
		}
	}
}
