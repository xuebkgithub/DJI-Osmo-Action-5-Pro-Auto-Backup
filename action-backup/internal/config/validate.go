package config

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"action-backup/internal/domain"
	"action-backup/internal/runmode"
	"action-backup/internal/storage/layout"
)

var hexID = regexp.MustCompile(`^[0-9a-fA-F]{4}$`)

// StorageTypes 合法的 storage.type。
const (
	StorageCIFS       = "cifs"
	StoragePremounted = "premounted"
	StorageLocal      = "local"
)

// TestRootSuffix simulation 模式写真实 NAS 时 destination_root 必须带的后缀。
const TestRootSuffix = "-TEST"

// Validate 校验配置以及模式组合。faultInjectBuild 为当前二进制是否带 faultinject 标签。
func (c *Config) Validate(faultInjectBuild bool) error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }

	mode, err := runmode.Parse(c.Mode)
	if err != nil {
		errs = append(errs, err)
	}

	if strings.TrimSpace(c.Server.Listen) == "" {
		add("server.listen is required")
	}
	if c.Server.Auth.Enabled {
		if strings.TrimSpace(c.Server.Auth.Username) == "" {
			add("server.auth.username is required when auth is enabled")
		}
		if !isBcrypt(c.Server.Auth.PasswordHash) {
			add("server.auth.password_hash must be a bcrypt hash (run: action-backup passwd)")
		}
	}
	if !hexID.MatchString(c.Device.VendorID) {
		add("device.vendor_id must be 4 hex digits, got %q", c.Device.VendorID)
	}
	if !hexID.MatchString(c.Device.ProductID) {
		add("device.product_id must be 4 hex digits, got %q", c.Device.ProductID)
	}
	if c.Backup.WorkerCount < 1 {
		add("backup.worker_count must be >= 1")
	}
	if c.Backup.BufferSizeMB < 1 || c.Backup.BufferSizeMB > 256 {
		add("backup.buffer_size_mb must be between 1 and 256")
	}
	if c.Backup.VerifyMode != string(domain.VerifyBasic) && c.Backup.VerifyMode != string(domain.VerifySHA256) {
		add("backup.verify_mode must be basic or sha256, got %q", c.Backup.VerifyMode)
	}
	switch c.Backup.OnConflict {
	case "manual", "overwrite", "skip":
	default:
		add("backup.on_conflict must be manual, overwrite or skip, got %q", c.Backup.OnConflict)
	}
	if c.Backup.ReconcileIntervalSec < 1 {
		add("backup.reconcile_interval_sec must be >= 1")
	}
	if c.Backup.DeleteSource {
		add("backup.delete_source must be false: deleting camera files is forbidden")
	}
	if strings.Contains(c.Backup.SourceDirectory, "..") || strings.HasPrefix(c.Backup.SourceDirectory, "/") {
		add("backup.source_directory must be a relative path without '..'")
	}
	if strings.TrimSpace(c.Database.Path) == "" {
		add("database.path is required")
	}

	// storage
	switch c.Storage.Type {
	case StorageCIFS:
		if c.Storage.Share == "" || c.Storage.MountPoint == "" || c.Storage.CredentialsFile == "" {
			add("storage.type cifs requires share, mount_point and credentials_file")
		}
	case StoragePremounted:
		if c.Storage.MountPoint == "" {
			add("storage.type premounted requires mount_point")
		}
	case StorageLocal:
		if c.Storage.LocalRoot == "" {
			add("storage.type local requires local_root")
		}
	default:
		add("storage.type must be cifs, premounted or local, got %q", c.Storage.Type)
	}
	if c.Storage.Type != StorageLocal && strings.EqualFold(c.Storage.SMBVersion, "1.0") {
		add("storage.smb_version 1.0 is forbidden")
	}
	if root := c.Storage.DestinationRoot; strings.TrimSpace(root) == "" || strings.HasPrefix(root, "/") || strings.Contains(root, "..") {
		add("storage.destination_root must be a non-empty relative path without '..'")
	}
	if _, err := layout.Parse(c.Storage.DestinationLayout); err != nil {
		errs = append(errs, err)
	}

	// faults
	if c.Faults != nil && c.Faults.Enabled && !faultInjectBuild {
		add("faults.enabled is true but this binary was built without -tags faultinject")
	}
	if c.Faults != nil {
		seen := map[string]bool{}
		for _, r := range c.Faults.Rules {
			if err := r.Validate(); err != nil {
				errs = append(errs, err)
			}
			if seen[r.ID] {
				add("faults.rules: duplicate id %q", r.ID)
			}
			seen[r.ID] = true
		}
	}

	// simulation block
	if c.Simulation != nil {
		errs = append(errs, c.validateSimulation()...)
	}

	// mode combinations
	switch mode {
	case runmode.Production:
		if c.Simulation != nil {
			add("mode production forbids the simulation section")
		}
		if c.Faults != nil && c.Faults.Enabled {
			add("mode production forbids faults.enabled")
		}
		if c.Storage.Type == StorageLocal {
			add("mode production forbids storage.type local")
		}
		if !c.Server.Auth.Enabled {
			add("mode production requires server.auth.enabled: true")
		}
	case runmode.DryRun:
		if c.Storage.Type == StorageLocal {
			add("mode dry-run forbids storage.type local")
		}
	case runmode.Simulation:
		if c.Simulation == nil {
			add("mode simulation requires the simulation section")
		} else {
			if c.Storage.Type == StorageLocal && !c.Simulation.AllowLocalDestination {
				add("storage.type local requires simulation.allow_local_destination: true")
			}
			if c.Storage.Type != StorageLocal && !strings.HasSuffix(c.Storage.DestinationRoot, TestRootSuffix) {
				add("mode simulation with a real share requires storage.destination_root to end with %q", TestRootSuffix)
			}
		}
	}
	return errors.Join(errs...)
}

func (c *Config) validateSimulation() []error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }
	s := c.Simulation
	switch s.DeviceSource {
	case "fixtures", "volumes", "both":
	default:
		add("simulation.device_source must be fixtures, volumes or both, got %q", s.DeviceSource)
	}
	if (s.DeviceSource == "fixtures" || s.DeviceSource == "both") && len(s.Fixtures.Devices) == 0 {
		add("simulation.fixtures.devices must not be empty for device_source %q", s.DeviceSource)
	}
	seen := map[string]bool{}
	for i, d := range s.Fixtures.Devices {
		if d.Key == "" {
			add("simulation.fixtures.devices[%d].key is required", i)
		}
		if seen[d.Key] {
			add("simulation.fixtures.devices[%d].key %q is duplicated", i, d.Key)
		}
		seen[d.Key] = true
		if d.Serial == "" {
			add("simulation.fixtures.devices[%d].serial is required", i)
		}
		if len(d.Volumes) == 0 {
			add("simulation.fixtures.devices[%d].volumes must not be empty", i)
		}
		for j, v := range d.Volumes {
			if !domain.VolumeType(v.Type).Valid() || v.Type == string(domain.VolumeUnknown) {
				add("simulation.fixtures.devices[%d].volumes[%d].type must be internal or sdcard", i, j)
			}
			if v.Path == "" {
				add("simulation.fixtures.devices[%d].volumes[%d].path is required", i, j)
			}
		}
	}
	return errs
}

func isBcrypt(h string) bool {
	return len(h) == 60 && (strings.HasPrefix(h, "$2a$") || strings.HasPrefix(h, "$2b$") || strings.HasPrefix(h, "$2y$"))
}
