package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"action-backup/internal/domain"
)

// Tasks 任务表。
type Tasks struct{ DB *sql.DB }

// TaskRecord 任务行及其关联的源文件信息。
type TaskRecord struct {
	ID               int64             `json:"id"`
	FileID           int64             `json:"file_id"`
	DestinationPath  string            `json:"destination_path"`
	Status           domain.TaskStatus `json:"status"`
	BytesTransferred int64             `json:"bytes_transferred"`
	RetryCount       int               `json:"retry_count"`
	VerifyMode       string            `json:"verify_mode"`
	ChecksumSHA256   string            `json:"checksum_sha256,omitempty"`
	LastError        string            `json:"last_error,omitempty"`
	StartedAt        string            `json:"started_at,omitempty"`
	CompletedAt      string            `json:"completed_at,omitempty"`
	CreatedAt        string            `json:"created_at"`

	RelativePath string `json:"relative_path"`
	FileSize     int64  `json:"file_size"`
	Fingerprint  string `json:"fingerprint"`
	ModTimeNs    int64  `json:"-"`
	VolumeID     int64  `json:"volume_id"`
	VolumeKey    string `json:"volume_key"`
	VolumeType   string `json:"volume_type"`
	DeviceKey    string `json:"device_key"`
}

const taskSelect = `
	SELECT t.id, t.file_id, t.destination_path, t.status, t.bytes_transferred, t.retry_count, t.verify_mode,
	       COALESCE(t.checksum_sha256,''), COALESCE(t.last_error,''), COALESCE(t.started_at,''), COALESCE(t.completed_at,''), t.created_at,
	       f.relative_path, f.file_size, f.fingerprint, f.modified_time_ns, v.id, v.volume_key, v.volume_type, d.device_key
	FROM backup_tasks t
	JOIN source_files f ON f.id = t.file_id
	JOIN volumes v ON v.id = f.volume_id
	JOIN devices d ON d.id = v.device_id`

func scanTask(sc interface{ Scan(...any) error }) (*TaskRecord, error) {
	var t TaskRecord
	var status string
	if err := sc.Scan(&t.ID, &t.FileID, &t.DestinationPath, &status, &t.BytesTransferred, &t.RetryCount, &t.VerifyMode,
		&t.ChecksumSHA256, &t.LastError, &t.StartedAt, &t.CompletedAt, &t.CreatedAt,
		&t.RelativePath, &t.FileSize, &t.Fingerprint, &t.ModTimeNs, &t.VolumeID, &t.VolumeKey, &t.VolumeType, &t.DeviceKey); err != nil {
		return nil, err
	}
	t.Status = domain.TaskStatus(status)
	return &t, nil
}

// Create 新建 PENDING 任务。
func (r Tasks) Create(ctx context.Context, fileID int64, destination string, verifyMode domain.VerifyMode) (int64, error) {
	res, err := r.DB.ExecContext(ctx, `INSERT INTO backup_tasks(file_id, destination_path, status, verify_mode) VALUES(?, ?, 'PENDING', ?)`,
		fileID, destination, string(verifyMode))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// Get 按 ID 读取。
func (r Tasks) Get(ctx context.Context, id int64) (*TaskRecord, error) {
	t, err := scanTask(r.DB.QueryRowContext(ctx, taskSelect+` WHERE t.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return t, err
}

// OpenForFile 返回某源文件尚未终结的任务（含 FAILED / INTERRUPTED），没有返回 nil。
func (r Tasks) OpenForFile(ctx context.Context, fileID int64) (*TaskRecord, error) {
	t, err := scanTask(r.DB.QueryRowContext(ctx, taskSelect+`
		WHERE t.file_id = ? AND t.status IN ('PENDING','QUEUED','TRANSFERRING','VERIFYING','INTERRUPTED','FAILED')
		ORDER BY t.id DESC LIMIT 1`, fileID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return t, err
}

// NextQueued 取最早入队的 QUEUED 任务。
func (r Tasks) NextQueued(ctx context.Context) (*TaskRecord, error) {
	t, err := scanTask(r.DB.QueryRowContext(ctx, taskSelect+` WHERE t.status = 'QUEUED' ORDER BY t.id ASC LIMIT 1`))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return t, err
}

// List 分页列出任务，status 为空表示全部。
func (r Tasks) List(ctx context.Context, status string, limit, offset int) ([]TaskRecord, int, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	where := ""
	args := []any{}
	if status != "" {
		where = ` WHERE t.status = ?`
		args = append(args, strings.ToUpper(status))
	}
	var total int
	if err := r.DB.QueryRowContext(ctx, `SELECT COUNT(1) FROM backup_tasks t`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := r.DB.QueryContext(ctx, taskSelect+where+` ORDER BY t.id DESC LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []TaskRecord{}
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *t)
	}
	return out, total, rows.Err()
}

// TransitionOptions 状态转移附带更新。
type TransitionOptions struct {
	Error    string
	Checksum string
	Bytes    *int64
}

// ErrInvalidTransition 转移不合法。
var ErrInvalidTransition = domain.ErrInvalidTransition

// Transition 在事务内校验状态机并更新。返回更新前的状态。
func (r Tasks) Transition(ctx context.Context, id int64, to domain.TaskStatus, opt TransitionOptions) (domain.TaskStatus, error) {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var cur string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM backup_tasks WHERE id = ?`, id).Scan(&cur); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("task %d not found", id)
		}
		return "", err
	}
	from := domain.TaskStatus(cur)
	if !from.CanTransitionTo(to) {
		return from, fmt.Errorf("%w: task %d %s -> %s", domain.ErrInvalidTransition, id, from, to)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	sets := []string{"status = ?"}
	args := []any{string(to)}
	switch to {
	case domain.TaskTransferring:
		sets = append(sets, "started_at = COALESCE(started_at, ?)")
		args = append(args, now)
	case domain.TaskCompleted, domain.TaskSkipped, domain.TaskCancelled:
		sets = append(sets, "completed_at = ?")
		args = append(args, now)
	case domain.TaskQueued:
		if from == domain.TaskFailed || from == domain.TaskInterrupted {
			sets = append(sets, "retry_count = retry_count + 1")
		}
	}
	if to == domain.TaskFailed || to == domain.TaskInterrupted || opt.Error != "" {
		sets = append(sets, "last_error = ?")
		args = append(args, nullIfEmpty(opt.Error))
	} else if to == domain.TaskCompleted {
		sets = append(sets, "last_error = NULL")
	}
	if opt.Checksum != "" {
		sets = append(sets, "checksum_sha256 = ?")
		args = append(args, opt.Checksum)
	}
	if opt.Bytes != nil {
		sets = append(sets, "bytes_transferred = ?")
		args = append(args, *opt.Bytes)
	}
	args = append(args, id)
	if _, err := tx.ExecContext(ctx, `UPDATE backup_tasks SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...); err != nil {
		return from, err
	}
	return from, tx.Commit()
}

// UpdateProgress 更新已传输字节数。
func (r Tasks) UpdateProgress(ctx context.Context, id, bytes int64) error {
	_, err := r.DB.ExecContext(ctx, `UPDATE backup_tasks SET bytes_transferred = ? WHERE id = ?`, bytes, id)
	return err
}

// MarkInterruptedOnStartup 把上次未完成的 TRANSFERRING / VERIFYING 标记为 INTERRUPTED。
func (r Tasks) MarkInterruptedOnStartup(ctx context.Context) (int64, error) {
	res, err := r.DB.ExecContext(ctx, `UPDATE backup_tasks SET status = 'INTERRUPTED', last_error = 'service restarted'
		WHERE status IN ('TRANSFERRING','VERIFYING')`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// CountByStatus 按状态统计任务数，所有状态都会出现在结果中。
func (r Tasks) CountByStatus(ctx context.Context) (map[domain.TaskStatus]int, error) {
	out := make(map[domain.TaskStatus]int, len(domain.AllTaskStatuses))
	for _, s := range domain.AllTaskStatuses {
		out[s] = 0
	}
	rows, err := r.DB.QueryContext(ctx, `SELECT status, COUNT(1) FROM backup_tasks GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		var n int
		if err := rows.Scan(&s, &n); err != nil {
			return nil, err
		}
		out[domain.TaskStatus(s)] = n
	}
	return out, rows.Err()
}

// TaskRow backup_tasks 关键字段（计划阶段使用）。
type TaskRow struct {
	ID              int64
	FileID          int64
	DestinationPath string
	Status          string
}

// LatestCompleted 返回某源文件最近一次 COMPLETED 任务；没有返回 nil。
func (r Tasks) LatestCompleted(ctx context.Context, fileID int64) (*TaskRow, error) {
	row := r.DB.QueryRowContext(ctx, `
		SELECT id, file_id, destination_path, status FROM backup_tasks
		WHERE file_id = ? AND status = 'COMPLETED' ORDER BY id DESC LIMIT 1`, fileID)
	var t TaskRow
	if err := row.Scan(&t.ID, &t.FileID, &t.DestinationPath, &t.Status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &t, nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Checkpoint 断点行。
type Checkpoint struct {
	TaskID    int64
	Offset    int64
	PartPath  string
	HashState string
	UpdatedAt string
}

// Checkpoints 断点表。
type Checkpoints struct{ DB *sql.DB }

// Get 读取断点；没有返回 nil。
func (r Checkpoints) Get(ctx context.Context, taskID int64) (*Checkpoint, error) {
	var c Checkpoint
	err := r.DB.QueryRowContext(ctx, `SELECT task_id, offset_bytes, part_path, COALESCE(prefix_sha256,''), updated_at FROM checkpoints WHERE task_id = ?`, taskID).
		Scan(&c.TaskID, &c.Offset, &c.PartPath, &c.HashState, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// Upsert 写入断点，同时更新任务的 bytes_transferred。
func (r Checkpoints) Upsert(ctx context.Context, c Checkpoint) error {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO checkpoints(task_id, offset_bytes, part_path, prefix_sha256, updated_at) VALUES(?, ?, ?, ?, ?)
		ON CONFLICT(task_id) DO UPDATE SET offset_bytes = excluded.offset_bytes, part_path = excluded.part_path,
			prefix_sha256 = excluded.prefix_sha256, updated_at = excluded.updated_at`,
		c.TaskID, c.Offset, c.PartPath, nullIfEmpty(c.HashState), now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE backup_tasks SET bytes_transferred = ? WHERE id = ?`, c.Offset, c.TaskID); err != nil {
		return err
	}
	return tx.Commit()
}

// Delete 删除断点。
func (r Checkpoints) Delete(ctx context.Context, taskID int64) error {
	_, err := r.DB.ExecContext(ctx, `DELETE FROM checkpoints WHERE task_id = ?`, taskID)
	return err
}

// Upsert 按 (volume, path, fingerprint) 写入源文件版本，返回 ID。
func (r SourceFiles) Upsert(ctx context.Context, f domain.SourceFile) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := r.DB.ExecContext(ctx, `
		INSERT INTO source_files(volume_id, relative_path, file_size, modified_time_ns, fingerprint, last_seen_at)
		VALUES(?, ?, ?, ?, ?, ?)
		ON CONFLICT(volume_id, relative_path, fingerprint) DO UPDATE SET last_seen_at = excluded.last_seen_at`,
		f.VolumeID, f.RelativePath, f.Size, f.ModTime.UnixNano(), f.Fingerprint, now); err != nil {
		return 0, err
	}
	var id int64
	err := r.DB.QueryRowContext(ctx, `SELECT id FROM source_files WHERE volume_id = ? AND relative_path = ? AND fingerprint = ?`,
		f.VolumeID, f.RelativePath, f.Fingerprint).Scan(&id)
	return id, err
}
