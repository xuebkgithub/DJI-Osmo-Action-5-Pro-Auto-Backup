-- 0001_init: 初始 Schema，对应架构文档 §6.2，并补充实测需要的列。
CREATE TABLE IF NOT EXISTS devices (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    device_key TEXT NOT NULL UNIQUE,
    vendor_id TEXT NOT NULL,
    product_id TEXT NOT NULL,
    serial_number TEXT,
    name TEXT NOT NULL,
    weak_identity INTEGER NOT NULL DEFAULT 0,
    last_seen_at TEXT,
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS volumes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    device_id INTEGER NOT NULL REFERENCES devices(id),
    volume_key TEXT NOT NULL,
    volume_type TEXT NOT NULL
        CHECK (volume_type IN ('internal','sdcard','unknown')),
    filesystem TEXT NOT NULL,
    label TEXT,
    lun INTEGER NOT NULL DEFAULT -1,
    size_bytes INTEGER NOT NULL DEFAULT 0,
    mount_path TEXT,
    last_seen_at TEXT,
    UNIQUE(device_id, volume_key)
);

CREATE TABLE IF NOT EXISTS source_files (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    volume_id INTEGER NOT NULL REFERENCES volumes(id),
    relative_path TEXT NOT NULL,
    file_size INTEGER NOT NULL CHECK (file_size >= 0),
    modified_time_ns INTEGER NOT NULL,
    fingerprint TEXT NOT NULL,
    last_seen_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(volume_id, relative_path, fingerprint)
);

CREATE TABLE IF NOT EXISTS backup_tasks (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    file_id INTEGER NOT NULL REFERENCES source_files(id),
    destination_path TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'PENDING'
        CHECK (status IN (
            'PENDING','QUEUED','TRANSFERRING',
            'VERIFYING','COMPLETED','FAILED',
            'INTERRUPTED','SKIPPED','CANCELLED'
        )),
    bytes_transferred INTEGER NOT NULL DEFAULT 0,
    retry_count INTEGER NOT NULL DEFAULT 0,
    verify_mode TEXT NOT NULL DEFAULT 'basic',
    checksum_sha256 TEXT,
    last_error TEXT,
    started_at TEXT,
    completed_at TEXT,
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS checkpoints (
    task_id INTEGER PRIMARY KEY
        REFERENCES backup_tasks(id) ON DELETE CASCADE,
    offset_bytes INTEGER NOT NULL DEFAULT 0,
    part_path TEXT NOT NULL,
    prefix_sha256 TEXT,
    updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id INTEGER REFERENCES backup_tasks(id),
    level TEXT NOT NULL,
    event_type TEXT NOT NULL,
    message TEXT NOT NULL,
    data TEXT,
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_files_volume_path
    ON source_files(volume_id, relative_path);
CREATE INDEX IF NOT EXISTS idx_tasks_status
    ON backup_tasks(status, created_at);
CREATE INDEX IF NOT EXISTS idx_tasks_file
    ON backup_tasks(file_id);
CREATE INDEX IF NOT EXISTS idx_events_created
    ON events(created_at);
