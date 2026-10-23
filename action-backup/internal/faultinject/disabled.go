//go:build !faultinject

package faultinject

import "fmt"

// Enabled 当前二进制是否包含故障注入能力。
const Enabled = false

// New 在 production 构建中拒绝任何规则，返回 Noop。
func New(rules []Rule) (Controller, error) {
	if len(rules) > 0 {
		return nil, fmt.Errorf("fault injection rules given but binary was built without -tags faultinject")
	}
	return nil, nil
}
