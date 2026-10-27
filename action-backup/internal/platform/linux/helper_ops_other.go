//go:build !linux

package linux

import "errors"

// NewRealOps 非 Linux 上没有挂载实现；守护只在设备上运行。
func NewRealOps() (helperOps, error) { return nil, errors.New("mount helper only runs on linux") }
