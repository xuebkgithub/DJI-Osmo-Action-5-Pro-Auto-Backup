// Package repository 提供各表的数据访问。
package repository

import (
	"context"
	"database/sql"
	"time"

	"action-backup/internal/domain"
)

// Devices 设备与卷。
type Devices struct{ DB *sql.DB }

// Upsert 写入设备及其卷，返回设备 ID。
func (r Devices) Upsert(ctx context.Context, d domain.Device) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	weak := 0
	if d.WeakIdentity {
		weak = 1
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO devices(device_key, vendor_id, product_id, serial_number, name, weak_identity, last_seen_at)
		VALUES(?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(device_key) DO UPDATE SET
			vendor_id = excluded.vendor_id, product_id = excluded.product_id,
			serial_number = excluded.serial_number, name = excluded.name,
			weak_identity = excluded.weak_identity, last_seen_at = excluded.last_seen_at`,
		d.Key, d.VendorID, d.ProductID, d.SerialNumber, d.Name, weak, now); err != nil {
		return 0, err
	}
	var id int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM devices WHERE device_key = ?`, d.Key).Scan(&id); err != nil {
		return 0, err
	}
	for _, v := range d.Volumes {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO volumes(device_id, volume_key, volume_type, filesystem, label, lun, size_bytes, mount_path, last_seen_at)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(device_id, volume_key) DO UPDATE SET
				volume_type = excluded.volume_type, filesystem = excluded.filesystem, label = excluded.label,
				lun = excluded.lun, size_bytes = excluded.size_bytes, mount_path = excluded.mount_path,
				last_seen_at = excluded.last_seen_at`,
			id, v.Key, string(v.Type), v.Filesystem, v.Label, v.LUN, v.SizeBytes, v.MountPath, now); err != nil {
			return 0, err
		}
	}
	return id, tx.Commit()
}

// List 返回全部设备（含卷），按最近出现时间倒序。
func (r Devices) List(ctx context.Context) ([]domain.Device, error) {
	rows, err := r.DB.QueryContext(ctx, `
		SELECT id, device_key, vendor_id, product_id, COALESCE(serial_number,''), name, weak_identity, COALESCE(last_seen_at,'')
		FROM devices ORDER BY last_seen_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Device
	for rows.Next() {
		var d domain.Device
		var weak int
		var seen string
		if err := rows.Scan(&d.ID, &d.Key, &d.VendorID, &d.ProductID, &d.SerialNumber, &d.Name, &weak, &seen); err != nil {
			return nil, err
		}
		d.WeakIdentity = weak == 1
		d.LastSeenAt, _ = time.Parse(time.RFC3339, seen)
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		vols, err := r.volumes(ctx, out[i].ID, out[i].Key)
		if err != nil {
			return nil, err
		}
		out[i].Volumes = vols
	}
	return out, nil
}

func (r Devices) volumes(ctx context.Context, deviceID int64, deviceKey string) ([]domain.Volume, error) {
	rows, err := r.DB.QueryContext(ctx, `
		SELECT id, volume_key, volume_type, filesystem, COALESCE(label,''), lun, size_bytes, COALESCE(mount_path,''), COALESCE(last_seen_at,'')
		FROM volumes WHERE device_id = ? ORDER BY lun, id`, deviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Volume
	for rows.Next() {
		v := domain.Volume{DeviceID: deviceID, DeviceKey: deviceKey}
		var t, seen string
		if err := rows.Scan(&v.ID, &v.Key, &t, &v.Filesystem, &v.Label, &v.LUN, &v.SizeBytes, &v.MountPath, &seen); err != nil {
			return nil, err
		}
		v.Type = domain.VolumeType(t)
		v.LastSeenAt, _ = time.Parse(time.RFC3339, seen)
		out = append(out, v)
	}
	return out, rows.Err()
}
