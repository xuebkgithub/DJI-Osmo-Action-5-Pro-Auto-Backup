// Package runmode 定义运行模式与写入门禁。
package runmode

import "fmt"

// Mode 运行模式。
type Mode string

const (
	Production Mode = "production"
	DryRun     Mode = "dry-run"
	Simulation Mode = "simulation"
)

// Parse 解析模式字符串。
func Parse(s string) (Mode, error) {
	switch Mode(s) {
	case Production, DryRun, Simulation:
		return Mode(s), nil
	case "":
		return "", fmt.Errorf("mode is required (production, dry-run, simulation)")
	}
	return "", fmt.Errorf("unknown mode %q (expected production, dry-run or simulation)", s)
}

// Guard 写入门禁。所有目标写入入口在执行前必须先询问它。
type Guard struct {
	mode        Mode
	faultInject bool
}

// NewGuard 创建门禁。faultInjectBuild 为当前二进制是否带 faultinject 构建标签。
func NewGuard(mode Mode, faultInjectBuild bool) *Guard {
	return &Guard{mode: mode, faultInject: faultInjectBuild}
}

// Mode 当前模式。
func (g *Guard) Mode() Mode { return g.mode }

// AllowDestinationWrite dry-run 返回 false，其余模式返回 true。
func (g *Guard) AllowDestinationWrite() bool { return g.mode != DryRun }

// TestEndpointsEnabled 仅在 faultinject 构建且非 production 时为 true。
func (g *Guard) TestEndpointsEnabled() bool { return g.faultInject && g.mode != Production }

// FaultInjectBuild 当前二进制是否为测试构建。
func (g *Guard) FaultInjectBuild() bool { return g.faultInject }
