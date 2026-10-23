package repository

import (
	"context"
	"database/sql"
	"errors"
)

// FileRow source_files 表行。
type FileRow struct {
	ID          int64
	VolumeID    int64
	Path        string
	Size        int64
	ModTimeNs   int64
	Fingerprint string
}

// SourceFiles 源文件表。
type SourceFiles struct{ DB *sql.DB }

// LatestByPath 返回卷上某路径最近一次记录的版本；不存在返回 nil。
func (r SourceFiles) LatestByPath(ctx context.Context, volumeID int64, relPath string) (*FileRow, error) {
	row := r.DB.QueryRowContext(ctx, `
		SELECT id, volume_id, relative_path, file_size, modified_time_ns, fingerprint
		FROM source_files WHERE volume_id = ? AND relative_path = ?
		ORDER BY id DESC LIMIT 1`, volumeID, relPath)
	var f FileRow
	if err := row.Scan(&f.ID, &f.VolumeID, &f.Path, &f.Size, &f.ModTimeNs, &f.Fingerprint); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &f, nil
}

// VolumeID 按设备键与卷键查 volumes.id；不存在返回 0。
func (r Devices) VolumeID(ctx context.Context, deviceKey, volumeKey string) (int64, error) {
	var id int64
	err := r.DB.QueryRowContext(ctx, `
		SELECT v.id FROM volumes v JOIN devices d ON d.id = v.device_id
		WHERE d.device_key = ? AND v.volume_key = ?`, deviceKey, volumeKey).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}
