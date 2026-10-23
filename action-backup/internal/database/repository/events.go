package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

// EventRecord 事件表行。
type EventRecord struct {
	ID        int64          `json:"id"`
	TaskID    *int64         `json:"task_id,omitempty"`
	Level     string         `json:"level"`
	Type      string         `json:"type"`
	Message   string         `json:"message"`
	Data      map[string]any `json:"data,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

// Events 事件表。
type Events struct{ DB *sql.DB }

// Insert 写入事件。
func (r Events) Insert(ctx context.Context, e EventRecord) (int64, error) {
	var data any
	if len(e.Data) > 0 {
		b, err := json.Marshal(e.Data)
		if err != nil {
			return 0, err
		}
		data = string(b)
	}
	at := e.CreatedAt
	if at.IsZero() {
		at = time.Now()
	}
	res, err := r.DB.ExecContext(ctx, `
		INSERT INTO events(task_id, level, event_type, message, data, created_at) VALUES(?, ?, ?, ?, ?, ?)`,
		e.TaskID, e.Level, e.Type, e.Message, data, at.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ListRecent 返回最近 limit 条事件，按时间倒序。
func (r Events) ListRecent(ctx context.Context, limit int) ([]EventRecord, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := r.DB.QueryContext(ctx, `
		SELECT id, task_id, level, event_type, message, data, created_at FROM events ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EventRecord
	for rows.Next() {
		var e EventRecord
		var data sql.NullString
		var at string
		if err := rows.Scan(&e.ID, &e.TaskID, &e.Level, &e.Type, &e.Message, &data, &at); err != nil {
			return nil, err
		}
		if data.Valid && data.String != "" {
			_ = json.Unmarshal([]byte(data.String), &e.Data)
		}
		e.CreatedAt, _ = time.Parse(time.RFC3339Nano, at)
		out = append(out, e)
	}
	return out, rows.Err()
}
