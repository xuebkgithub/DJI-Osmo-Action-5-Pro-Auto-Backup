package linux

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"action-backup/internal/config"
)

// 守护只接受这些形状的参数，其余一律拒绝；路径都从配置文件推导，客户端不能指定挂载点。

var (
	blockNameRe = regexp.MustCompile(`^sd[a-z]{1,3}$`) // 整盘设备；相机实测两卷都无分区
	shareRe     = regexp.MustCompile(`^//[A-Za-z0-9][A-Za-z0-9.\-]*(:[0-9]{1,5})?/[^/\\\s,]+$`)
	versRe      = regexp.MustCompile(`^(2\.0|2\.1|3\.0|3\.02|3\.1\.1|3)$`)
)

// sourcePlan 一次源卷挂载。
type sourcePlan struct {
	Source string // /dev/sda
	Target string // <mount_root>/sda
	FSType string // exfat | vfat
}

// targetPlan 一次 CIFS 挂载。
type targetPlan struct {
	Share           string
	MountPoint      string
	CredentialsFile string
	Vers            string // 空表示由内核协商（2.1 起）
}

// blockName 从 /dev/sdX 取出并校验设备名。
func blockName(device string) (string, error) {
	if !strings.HasPrefix(device, "/dev/") {
		return "", fmt.Errorf("device %q must be under /dev", device)
	}
	name := strings.TrimPrefix(device, "/dev/")
	if !blockNameRe.MatchString(name) {
		return "", fmt.Errorf("device %q is not a whole USB disk (sd[a-z]+)", device)
	}
	return name, nil
}

func mountRootOf(cfg *config.Config) (string, error) {
	root := cfg.Device.MountRoot
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || root == "/" {
		return "", fmt.Errorf("device.mount_root %q must be an absolute clean path", root)
	}
	return root, nil
}

// planSource 校验设备确实属于配置的相机（VID/PID），文件系统为 exFAT / FAT，并推导挂载点。
func planSource(fs Sysfs, cfg *config.Config, device string) (sourcePlan, error) {
	name, err := blockName(device)
	if err != nil {
		return sourcePlan{}, err
	}
	root, err := mountRootOf(cfg)
	if err != nil {
		return sourcePlan{}, err
	}
	_, vol, err := fs.blockOwner(name, cfg.Device.VendorID, cfg.Device.ProductID)
	if err != nil {
		return sourcePlan{}, err
	}
	fstype := strings.ToLower(vol.Props["ID_FS_TYPE"])
	switch fstype {
	case "exfat", "vfat":
	default:
		return sourcePlan{}, fmt.Errorf("%s filesystem %q is not exfat/vfat", device, fstype)
	}
	return sourcePlan{Source: device, Target: filepath.Join(root, name), FSType: fstype}, nil
}

// sourceTarget 卸载时只需要设备名与 mount_root。
func sourceTarget(cfg *config.Config, device string) (string, error) {
	name, err := blockName(device)
	if err != nil {
		return "", err
	}
	root, err := mountRootOf(cfg)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, name), nil
}

// planTarget 校验 storage 段并推导 CIFS 挂载参数。
func planTarget(cfg *config.Config) (targetPlan, error) {
	st := cfg.Storage
	if st.Type != config.StorageCIFS {
		return targetPlan{}, fmt.Errorf("storage.type is %q, only cifs is mounted by the helper", st.Type)
	}
	if !shareRe.MatchString(st.Share) {
		return targetPlan{}, fmt.Errorf("storage.share %q is not //host/share", st.Share)
	}
	mp := st.MountPoint
	if !filepath.IsAbs(mp) || filepath.Clean(mp) != mp || mp == "/" {
		return targetPlan{}, fmt.Errorf("storage.mount_point %q must be an absolute clean path", mp)
	}
	cred := st.CredentialsFile
	if !filepath.IsAbs(cred) {
		return targetPlan{}, fmt.Errorf("storage.credentials_file %q must be absolute", cred)
	}
	info, err := os.Stat(cred)
	if err != nil {
		return targetPlan{}, fmt.Errorf("storage.credentials_file: %w", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return targetPlan{}, fmt.Errorf("storage.credentials_file %s must not be group/world readable (chmod 0600)", cred)
	}
	vers := strings.ToLower(strings.TrimSpace(st.SMBVersion))
	switch {
	case vers == "" || vers == "auto":
		vers = ""
	case vers == "1.0" || vers == "1":
		return targetPlan{}, errors.New("storage.smb_version 1.0 is forbidden")
	case !versRe.MatchString(vers):
		return targetPlan{}, fmt.Errorf("storage.smb_version %q unsupported (auto, 2.0, 2.1, 3.0, 3.02, 3.1.1)", st.SMBVersion)
	}
	return targetPlan{Share: st.Share, MountPoint: mp, CredentialsFile: cred, Vers: vers}, nil
}
