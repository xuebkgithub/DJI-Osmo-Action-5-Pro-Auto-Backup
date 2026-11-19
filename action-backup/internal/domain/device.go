package domain

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Device 一台相机的身份与最近状态。
type Device struct {
	ID           int64
	Key          string
	VendorID     string
	ProductID    string
	SerialNumber string
	Name         string
	// WeakIdentity 表示序列号来自占位值或无法解析，仅能靠 VID/PID 识别。
	WeakIdentity bool
	// USBVersion 描述符声明的 USB 协议版本（如 "3.2"）；LinkSpeedMbps 当前实际协商速率，未知为 0。
	// 两者是本次连接的状态而非设备身份，只在内存与事件里，不入库。
	USBVersion    string
	LinkSpeedMbps int64
	LastSeenAt    time.Time
	Volumes       []Volume
}

// USBHighSpeedMbps USB 2.0 High-Speed 的链路速率；不高于它即视为降速连接。
const USBHighSpeedMbps = 480

// USBLinkLimited 相机是否以 USB 2.0 或更低速率连接（线材 / 接口限制），备份速度会明显受限。
func (d Device) USBLinkLimited() bool {
	return d.LinkSpeedMbps > 0 && d.LinkSpeedMbps <= USBHighSpeedMbps
}

// NormalizeUSBVersion 把 sysfs version（" 3.20"）或 bcdUSB 文本规整成 "3.2" / "2.0" 这样的短形式。
// 无法解析时原样返回去掉空白后的值。
func NormalizeUSBVersion(s string) string {
	s = strings.TrimSpace(s)
	major, minor, ok := strings.Cut(s, ".")
	if !ok || major == "" || strings.Trim(major+minor, "0123456789") != "" {
		return s
	}
	major = strings.TrimLeft(major, "0")
	if major == "" {
		major = "0"
	}
	minor = strings.TrimRight(minor, "0")
	if minor == "" {
		minor = "0"
	}
	return major + "." + minor
}

// USBVersionFromBCD 把 bcdUSB 数值（如 0x0320）转成 "3.2"；非正数返回空串。
func USBVersionFromBCD(bcd int64) string {
	if bcd <= 0 {
		return ""
	}
	return NormalizeUSBVersion(fmt.Sprintf("%x.%02x", bcd>>8, bcd&0xff))
}

// 实测：相机 USB iSerialNumber 固定为占位值，真实序列号在产品名字符串中，如 OsmoAction5pro_SN:0A1B2C3D。
var productSerialRe = regexp.MustCompile(`(?i)SN:([0-9A-Z]+)`)

// PlaceholderSerial 相机 USB 描述符中的占位序列号。
const PlaceholderSerial = "123456789ABCDEF"

// ResolveSerial 从产品名字符串和 USB 序列号中得到可信序列号。
// 返回值 weak 为 true 表示没有可信序列号。
func ResolveSerial(productName, usbSerial string) (serial string, weak bool) {
	if m := productSerialRe.FindStringSubmatch(productName); m != nil {
		return strings.ToUpper(m[1]), false
	}
	s := strings.TrimSpace(usbSerial)
	if s == "" || strings.EqualFold(s, PlaceholderSerial) {
		return "", true
	}
	return s, true
}

// DeviceKey 生成设备唯一键。
func DeviceKey(vendorID, productID, serial string) string {
	if serial == "" {
		return fmt.Sprintf("%s:%s:unknown", strings.ToLower(vendorID), strings.ToLower(productID))
	}
	return fmt.Sprintf("%s:%s:%s", strings.ToLower(vendorID), strings.ToLower(productID), strings.ToUpper(serial))
}
