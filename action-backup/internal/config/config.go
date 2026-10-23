// Package config 解析与校验 YAML 配置。
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"action-backup/internal/faultinject"
)

// Config 顶层配置。
type Config struct {
	Mode       string            `yaml:"mode" json:"mode"`
	Server     ServerConfig      `yaml:"server" json:"server"`
	Device     DeviceConfig      `yaml:"device" json:"device"`
	Simulation *SimulationConfig `yaml:"simulation,omitempty" json:"simulation,omitempty"`
	Backup     BackupConfig      `yaml:"backup" json:"backup"`
	Storage    StorageConfig     `yaml:"storage" json:"storage"`
	Faults     *FaultsConfig     `yaml:"faults,omitempty" json:"faults,omitempty"`
	Database   DatabaseConfig    `yaml:"database" json:"database"`
	Logging    LoggingConfig     `yaml:"logging" json:"logging"`

	// Path 配置文件绝对路径，加载时填充。
	Path string `yaml:"-" json:"-"`
}

// ServerConfig HTTP 服务。
type ServerConfig struct {
	Listen string     `yaml:"listen" json:"listen"`
	Auth   AuthConfig `yaml:"auth" json:"auth"`
}

// AuthConfig 用户名密码认证。
type AuthConfig struct {
	Enabled              bool   `yaml:"enabled" json:"enabled"`
	Username             string `yaml:"username" json:"username"`
	PasswordHash         string `yaml:"password_hash" json:"password_hash"`
	SessionTTLHours      int    `yaml:"session_ttl_hours" json:"session_ttl_hours"`
	LoginRateLimitPerMin int    `yaml:"login_rate_limit_per_min" json:"login_rate_limit_per_min"`
}

// DeviceConfig 设备识别。
type DeviceConfig struct {
	VendorID   string `yaml:"vendor_id" json:"vendor_id"`
	ProductID  string `yaml:"product_id" json:"product_id"`
	AutoDetect bool   `yaml:"auto_detect" json:"auto_detect"`
	AutoBackup bool   `yaml:"auto_backup" json:"auto_backup"`
	MountRoot  string `yaml:"mount_root" json:"mount_root"`
	ReadOnly   bool   `yaml:"readonly" json:"readonly"`
}

// SimulationConfig 仅 simulation 模式。
type SimulationConfig struct {
	DeviceSource          string            `yaml:"device_source" json:"device_source"`
	Fixtures              SimFixturesConfig `yaml:"fixtures" json:"fixtures"`
	Volumes               SimVolumesConfig  `yaml:"volumes" json:"volumes"`
	AllowLocalDestination bool              `yaml:"allow_local_destination" json:"allow_local_destination"`
}

// SimFixturesConfig 目录型模拟设备。
type SimFixturesConfig struct {
	Devices         []SimDevice `yaml:"devices" json:"devices"`
	AttachedOnStart bool        `yaml:"attached_on_start" json:"attached_on_start"`
}

// SimDevice 一台模拟相机。
type SimDevice struct {
	Key     string      `yaml:"key" json:"key"`
	Serial  string      `yaml:"serial" json:"serial"`
	Name    string      `yaml:"name" json:"name"`
	Volumes []SimVolume `yaml:"volumes" json:"volumes"`
}

// SimVolume 模拟卷。
type SimVolume struct {
	Type  string `yaml:"type" json:"type"`
	Path  string `yaml:"path" json:"path"`
	Label string `yaml:"label" json:"label"`
}

// SimVolumesConfig mac 上读取 /Volumes 真相机。
type SimVolumesConfig struct {
	PollIntervalSec int    `yaml:"poll_interval_sec" json:"poll_interval_sec"`
	MatchBy         string `yaml:"match_by" json:"match_by"`
}

// BackupConfig 备份策略。
type BackupConfig struct {
	// IncludeInternal / IncludeSDCard 省略时默认 true。
	IncludeInternal       *bool    `yaml:"include_internal" json:"include_internal"`
	IncludeSDCard         *bool    `yaml:"include_sdcard" json:"include_sdcard"`
	SourceDirectory       string   `yaml:"source_directory" json:"source_directory"`
	FileExtensions        []string `yaml:"file_extensions" json:"file_extensions"`
	IgnorePatterns        []string `yaml:"ignore_patterns" json:"ignore_patterns"`
	WorkerCount           int      `yaml:"worker_count" json:"worker_count"`
	BufferSizeMB          int      `yaml:"buffer_size_mb" json:"buffer_size_mb"`
	RetryCount            int      `yaml:"retry_count" json:"retry_count"`
	CheckpointIntervalSec int      `yaml:"checkpoint_interval_sec" json:"checkpoint_interval_sec"`
	CheckpointBytesMB     int      `yaml:"checkpoint_bytes_mb" json:"checkpoint_bytes_mb"`
	VerifyMode            string   `yaml:"verify_mode" json:"verify_mode"`
	DeleteSource          bool     `yaml:"delete_source" json:"delete_source"`
	// OnConflict 计划中 conflict 项的处理：manual（默认，不动）| overwrite（照常上传覆盖）| skip（忽略）
	OnConflict string `yaml:"on_conflict" json:"on_conflict"`
	// ReconcileIntervalSec 调度器检查 INTERRUPTED 任务是否可重新入队的周期。
	ReconcileIntervalSec int `yaml:"reconcile_interval_sec" json:"reconcile_interval_sec"`
}

// StorageConfig 目标存储。
type StorageConfig struct {
	Type              string `yaml:"type" json:"type"`
	Share             string `yaml:"share" json:"share"`
	MountPoint        string `yaml:"mount_point" json:"mount_point"`
	SMBVersion        string `yaml:"smb_version" json:"smb_version"`
	CredentialsFile   string `yaml:"credentials_file" json:"credentials_file"`
	LocalRoot         string `yaml:"local_root" json:"local_root"`
	DestinationRoot   string `yaml:"destination_root" json:"destination_root"`
	DestinationLayout string `yaml:"destination_layout" json:"destination_layout"`
}

// FaultsConfig 故障注入。
type FaultsConfig struct {
	Enabled bool               `yaml:"enabled" json:"enabled"`
	Rules   []faultinject.Rule `yaml:"rules" json:"rules"`
}

// DatabaseConfig SQLite。
type DatabaseConfig struct {
	Path string `yaml:"path" json:"path"`
}

// LoggingConfig 日志。
type LoggingConfig struct {
	Level  string `yaml:"level" json:"level"`
	Format string `yaml:"format" json:"format"`
}

// DefaultIgnorePatterns 扫描时忽略的系统元数据文件。
var DefaultIgnorePatterns = []string{
	"._*", ".DS_Store", ".Spotlight-V100", ".fseventsd", ".Trashes", "System Volume Information",
}

// Load 读取并解析配置文件，填充默认值，把相对路径解析为绝对路径。
func Load(path string) (*Config, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve config path: %w", err)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	cfg, err := Parse(data)
	if err != nil {
		return nil, err
	}
	cfg.Path = abs
	return cfg, nil
}

// Parse 解析 YAML 字节并填充默认值。相对路径按当前工作目录解析。
func Parse(data []byte) (*Config, error) {
	var cfg Config
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	cfg.applyDefaults()
	cfg.resolvePaths()
	return &cfg, nil
}

func (c *Config) applyDefaults() {
	if c.Server.Listen == "" {
		c.Server.Listen = "127.0.0.1:8080"
	}
	if c.Server.Auth.SessionTTLHours == 0 {
		c.Server.Auth.SessionTTLHours = 24
	}
	if c.Server.Auth.LoginRateLimitPerMin == 0 {
		c.Server.Auth.LoginRateLimitPerMin = 5
	}
	if c.Device.VendorID == "" {
		c.Device.VendorID = "0506"
	}
	if c.Device.ProductID == "" {
		c.Device.ProductID = "8002"
	}
	if c.Backup.SourceDirectory == "" {
		c.Backup.SourceDirectory = "DCIM"
	}
	if c.Backup.IncludeInternal == nil {
		c.Backup.IncludeInternal = boolPtr(true)
	}
	if c.Backup.IncludeSDCard == nil {
		c.Backup.IncludeSDCard = boolPtr(true)
	}
	if len(c.Backup.FileExtensions) == 0 {
		c.Backup.FileExtensions = []string{".mp4"}
	}
	for i, ext := range c.Backup.FileExtensions {
		ext = strings.ToLower(strings.TrimSpace(ext))
		if !strings.HasPrefix(ext, ".") {
			ext = "." + ext
		}
		c.Backup.FileExtensions[i] = ext
	}
	if len(c.Backup.IgnorePatterns) == 0 {
		c.Backup.IgnorePatterns = append([]string(nil), DefaultIgnorePatterns...)
	}
	if c.Backup.WorkerCount == 0 {
		c.Backup.WorkerCount = 1
	}
	if c.Backup.BufferSizeMB == 0 {
		c.Backup.BufferSizeMB = 4
	}
	if c.Backup.RetryCount == 0 {
		c.Backup.RetryCount = 3
	}
	if c.Backup.CheckpointIntervalSec == 0 {
		c.Backup.CheckpointIntervalSec = 5
	}
	if c.Backup.CheckpointBytesMB == 0 {
		c.Backup.CheckpointBytesMB = 64
	}
	if c.Backup.VerifyMode == "" {
		c.Backup.VerifyMode = "basic"
	}
	if c.Backup.OnConflict == "" {
		c.Backup.OnConflict = "manual"
	}
	if c.Backup.ReconcileIntervalSec == 0 {
		c.Backup.ReconcileIntervalSec = 30
	}
	if c.Storage.SMBVersion == "" {
		c.Storage.SMBVersion = "auto"
	}
	if c.Storage.DestinationLayout == "" {
		c.Storage.DestinationLayout = "{volume_type}/{relative_path}"
	}
	if c.Logging.Level == "" {
		c.Logging.Level = "info"
	}
	if c.Logging.Format == "" {
		c.Logging.Format = "json"
	}
	if c.Simulation != nil {
		if c.Simulation.DeviceSource == "" {
			c.Simulation.DeviceSource = "fixtures"
		}
		if c.Simulation.Volumes.PollIntervalSec == 0 {
			c.Simulation.Volumes.PollIntervalSec = 2
		}
		if c.Simulation.Volumes.MatchBy == "" {
			c.Simulation.Volumes.MatchBy = "usb_vid_pid"
		}
		for i := range c.Simulation.Fixtures.Devices {
			d := &c.Simulation.Fixtures.Devices[i]
			if d.Name == "" {
				d.Name = "Simulated Osmo Action 5 Pro"
			}
		}
	}
}

func (c *Config) resolvePaths() {
	c.Database.Path = absPath(c.Database.Path)
	c.Storage.LocalRoot = absPath(c.Storage.LocalRoot)
	c.Storage.MountPoint = absPath(c.Storage.MountPoint)
	c.Storage.CredentialsFile = absPath(c.Storage.CredentialsFile)
	c.Device.MountRoot = absPath(c.Device.MountRoot)
	if c.Simulation != nil {
		for i := range c.Simulation.Fixtures.Devices {
			for j := range c.Simulation.Fixtures.Devices[i].Volumes {
				v := &c.Simulation.Fixtures.Devices[i].Volumes[j]
				v.Path = absPath(v.Path)
			}
		}
	}
}

func boolPtr(b bool) *bool { return &b }

// IncludesInternal 是否备份内置存储。
func (b BackupConfig) IncludesInternal() bool { return b.IncludeInternal == nil || *b.IncludeInternal }

// IncludesSDCard 是否备份 microSD。
func (b BackupConfig) IncludesSDCard() bool { return b.IncludeSDCard == nil || *b.IncludeSDCard }

func absPath(p string) string {
	if p == "" {
		return ""
	}
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return p
}

// Redacted 返回去除敏感字段的副本，供 API 输出。
func (c *Config) Redacted() Config {
	out := *c
	if out.Server.Auth.PasswordHash != "" {
		out.Server.Auth.PasswordHash = "***"
	}
	return out
}
