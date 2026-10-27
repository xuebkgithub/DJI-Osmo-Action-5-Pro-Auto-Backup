package domain

import "errors"

var (
	// ErrModeReadOnly 当前运行模式禁止写目标存储（dry-run）。
	ErrModeReadOnly = errors.New("destination writes are disabled in this run mode")
	// ErrNotImplemented 功能尚未在当前阶段实现。
	ErrNotImplemented = errors.New("not implemented")
	// ErrStorageUnavailable 目标存储不可用或不是期望的挂载。
	ErrStorageUnavailable = errors.New("storage target unavailable")
	// ErrInvalidTransition 任务状态转移不合法。
	ErrInvalidTransition = errors.New("invalid task state transition")
	// ErrPathEscapesRoot 渲染后的路径越出目标根目录。
	ErrPathEscapesRoot = errors.New("path escapes destination root")
	// ErrDeviceNotFound 设备不存在。
	ErrDeviceNotFound = errors.New("device not found")
	// ErrStorageBusy 目标存储正被任务使用，拒绝卸载。
	ErrStorageBusy = errors.New("storage target is in use by a running task")
)
