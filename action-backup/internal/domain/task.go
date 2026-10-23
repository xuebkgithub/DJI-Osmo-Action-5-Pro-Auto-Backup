package domain

import "time"

// TaskStatus 备份任务状态。
type TaskStatus string

const (
	TaskPending      TaskStatus = "PENDING"
	TaskQueued       TaskStatus = "QUEUED"
	TaskTransferring TaskStatus = "TRANSFERRING"
	TaskVerifying    TaskStatus = "VERIFYING"
	TaskCompleted    TaskStatus = "COMPLETED"
	TaskFailed       TaskStatus = "FAILED"
	TaskInterrupted  TaskStatus = "INTERRUPTED"
	TaskSkipped      TaskStatus = "SKIPPED"
	TaskCancelled    TaskStatus = "CANCELLED"
)

// AllTaskStatuses 全部状态，顺序固定，用于统计输出。
var AllTaskStatuses = []TaskStatus{
	TaskPending, TaskQueued, TaskTransferring, TaskVerifying,
	TaskCompleted, TaskFailed, TaskInterrupted, TaskSkipped, TaskCancelled,
}

var transitions = map[TaskStatus][]TaskStatus{
	TaskPending: {TaskQueued, TaskSkipped, TaskCancelled},
	TaskQueued:  {TaskTransferring, TaskCancelled},
	// TRANSFERRING -> QUEUED 仅用于用户暂停，保留断点，不记为故障。
	TaskTransferring: {TaskVerifying, TaskInterrupted, TaskFailed, TaskCancelled, TaskQueued},
	TaskVerifying:    {TaskCompleted, TaskFailed, TaskInterrupted, TaskCancelled},
	TaskInterrupted:  {TaskQueued, TaskCancelled},
	TaskFailed:       {TaskQueued, TaskCancelled},
}

// CanTransitionTo 判断状态机是否允许该转移。
func (s TaskStatus) CanTransitionTo(next TaskStatus) bool {
	for _, allowed := range transitions[s] {
		if allowed == next {
			return true
		}
	}
	return false
}

// IsTerminal 判断是否终态。
func (s TaskStatus) IsTerminal() bool {
	switch s {
	case TaskCompleted, TaskSkipped, TaskCancelled:
		return true
	}
	return false
}

// VerifyMode 校验模式。
type VerifyMode string

const (
	VerifyBasic  VerifyMode = "basic"
	VerifySHA256 VerifyMode = "sha256"
)

// BackupTask 一次备份尝试。
type BackupTask struct {
	ID               int64
	FileID           int64
	DestinationPath  string
	Status           TaskStatus
	BytesTransferred int64
	RetryCount       int
	VerifyMode       VerifyMode
	ChecksumSHA256   string
	LastError        string
	StartedAt        *time.Time
	CompletedAt      *time.Time
	CreatedAt        time.Time
}
