// Package version 保存由构建脚本通过 -ldflags 注入的版本信息。
package version

var (
	// Version 语义化版本，默认开发版。
	Version = "0.1.0-dev"
	// Commit 构建时的 git 提交哈希。
	Commit = "unknown"
	// BuildTime 构建时间，UTC RFC3339。
	BuildTime = "unknown"
)
