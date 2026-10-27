package linux

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"time"

	"action-backup/internal/platform/raw"
)

// DefaultHelperSocket 挂载守护的 unix socket。目录由 action-backup-mounter.service 的 RuntimeDirectory 创建，
// 服务主进程以 actionbackup 组身份连接；守护进程把 socket 设为 root:actionbackup 0660。
const DefaultHelperSocket = "/run/action-backup-mounter/helper.sock"

// 请求类型。
const (
	OpPing          = "ping"
	OpMountSource   = "mount-source"  // 只读挂载一个相机卷：Device=/dev/sdX → Path
	OpUnmountSource = "umount-source" // 卸载相机卷
	OpMountTarget   = "mount-target"  // 按配置挂载 CIFS 目标
	OpUnmountTarget = "umount-target" // 卸载目标
)

// HelperRequest 一行 JSON，一次连接一个请求。
type HelperRequest struct {
	Op     string `json:"op"`
	Device string `json:"device,omitempty"`
}

// HelperResponse 守护的应答。
type HelperResponse struct {
	OK    bool   `json:"ok"`
	Path  string `json:"path,omitempty"`
	Error string `json:"error,omitempty"`
}

// Client 连接挂载守护。
type Client struct {
	Socket  string
	Timeout time.Duration
}

// NewClient 默认 socket，超时 60 秒（CIFS 挂载可能较慢）。
func NewClient() Client { return Client{Socket: DefaultHelperSocket, Timeout: 60 * time.Second} }

// Call 发送请求并等待应答。守护不在时返回明确错误。
func (c Client) Call(ctx context.Context, req HelperRequest) (HelperResponse, error) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", c.Socket)
	if err != nil {
		return HelperResponse{}, fmt.Errorf("mount helper unavailable at %s (is action-backup-mounter running?): %w", c.Socket, err)
	}
	defer func() { _ = conn.Close() }()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return HelperResponse{}, fmt.Errorf("send to mount helper: %w", err)
	}
	var resp HelperResponse
	if err := json.NewDecoder(bufio.NewReader(conn)).Decode(&resp); err != nil {
		return HelperResponse{}, fmt.Errorf("read mount helper reply: %w", err)
	}
	if !resp.OK {
		return resp, fmt.Errorf("mount helper: %s", resp.Error)
	}
	return resp, nil
}

// Mounter 通过挂载守护只读挂载相机卷。实现 platform.VolumeMounter。
type Mounter struct {
	Client Client
}

// MountReadOnly 已挂载则直接返回，否则请求守护挂载。
func (m Mounter) MountReadOnly(ctx context.Context, v raw.Volume) (string, error) {
	if v.MountPath != "" {
		if st, err := os.Stat(v.MountPath); err == nil && st.IsDir() {
			return v.MountPath, nil
		}
	}
	if v.DevicePath == "" {
		return "", fmt.Errorf("volume %s has no device path", v.Key)
	}
	resp, err := m.Client.Call(ctx, HelperRequest{Op: OpMountSource, Device: v.DevicePath})
	if err != nil {
		return "", fmt.Errorf("mount %s read-only: %w", v.DevicePath, err)
	}
	return resp.Path, nil
}

// Unmount 请求守护卸载。
func (m Mounter) Unmount(ctx context.Context, v raw.Volume) error {
	if v.DevicePath == "" {
		return nil
	}
	_, err := m.Client.Call(ctx, HelperRequest{Op: OpUnmountSource, Device: v.DevicePath})
	return err
}

// StorageMounter 通过挂载守护挂载 / 卸载 CIFS 目标。实现 platform.StorageMounter。
type StorageMounter struct {
	Client Client
}

// MountTarget 按守护读取的配置挂载 storage.share 到 storage.mount_point。
func (s StorageMounter) MountTarget(ctx context.Context) error {
	_, err := s.Client.Call(ctx, HelperRequest{Op: OpMountTarget})
	return err
}

// UnmountTarget 卸载目标。
func (s StorageMounter) UnmountTarget(ctx context.Context) error {
	_, err := s.Client.Call(ctx, HelperRequest{Op: OpUnmountTarget})
	return err
}
