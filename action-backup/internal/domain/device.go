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
	LastSeenAt   time.Time
	Volumes      []Volume
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
