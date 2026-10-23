package backup

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"action-backup/internal/database/repository"
	"action-backup/internal/domain"
	"action-backup/internal/events"
	"action-backup/internal/runmode"
)

// Planner 生成计划。
type Planner interface {
	Plan(ctx context.Context) (*domain.Plan, error)
}

// Queue 入队。
type Queue interface {
	Enqueue(ctx context.Context, ids []int64) (int, error)
	RetriesLeft(t repository.TaskRecord) bool
}

// Service 把计划转换为任务并入队。
type Service struct {
	planner Planner
	files   repository.SourceFiles
	tasks   repository.Tasks
	queue   Queue
	guard   *runmode.Guard
	bus     *events.Broadcaster
	log     *slog.Logger

	mu         sync.Mutex
	verifyMode domain.VerifyMode
	onConflict string
}

// SetConflictPolicy 设置冲突策略：manual | overwrite | skip。可在运行时调用。
func (s *Service) SetConflictPolicy(p string) {
	if p != "" {
		s.mu.Lock()
		s.onConflict = p
		s.mu.Unlock()
	}
}

// SetVerifyMode 设置新任务的校验方式。可在运行时调用，只影响之后创建的任务。
func (s *Service) SetVerifyMode(m domain.VerifyMode) {
	if m == domain.VerifyBasic || m == domain.VerifySHA256 {
		s.mu.Lock()
		s.verifyMode = m
		s.mu.Unlock()
	}
}

// Policy 返回当前校验方式与冲突策略。
func (s *Service) Policy() (domain.VerifyMode, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.verifyMode, s.onConflict
}

// NewService 创建服务。
func NewService(planner Planner, files repository.SourceFiles, tasks repository.Tasks, queue Queue, guard *runmode.Guard, verifyMode domain.VerifyMode, bus *events.Broadcaster, log *slog.Logger) *Service {
	return &Service{planner: planner, files: files, tasks: tasks, queue: queue, guard: guard, verifyMode: verifyMode, onConflict: "manual", bus: bus, log: log}
}

// StartResult 一次 start 的结果。
type StartResult struct {
	Plan      *domain.Plan `json:"plan"`
	Created   int          `json:"tasks_created"`
	Requeued  int          `json:"tasks_requeued"`
	Enqueued  int          `json:"tasks_enqueued"`
	Conflicts int          `json:"conflicts"`
	// Exhausted 已用尽自动重试次数、需人工 retry 的任务数。
	Exhausted int `json:"tasks_retry_exhausted"`
	// Overwritten 按 on_conflict: overwrite 转为上传的冲突数。
	Overwritten int `json:"conflicts_overwritten"`
}

// Start 扫描、规划、建任务、入队。dry-run 下只返回计划。
func (s *Service) Start(ctx context.Context) (*StartResult, error) {
	plan, err := s.planner.Plan(ctx)
	if err != nil {
		return nil, err
	}
	res := &StartResult{Plan: plan, Conflicts: plan.Conflict.Files}
	if !s.guard.AllowDestinationWrite() {
		return res, nil
	}
	verifyMode, onConflict := s.Policy()
	var ids []int64
	for _, it := range plan.Items {
		if it.Action == domain.PlanConflict && onConflict == "overwrite" && it.File.RelativePath != "" {
			res.Overwritten++
		} else if it.Action != domain.PlanUpload {
			continue
		}
		if it.VolumeID == 0 {
			s.log.Warn("plan item without persisted volume, skipping", "path", it.RelativePath)
			continue
		}
		f := it.File
		f.VolumeID = it.VolumeID
		fileID, err := s.files.Upsert(ctx, f)
		if err != nil {
			return res, fmt.Errorf("persist source file %s: %w", it.RelativePath, err)
		}
		open, err := s.tasks.OpenForFile(ctx, fileID)
		if err != nil {
			return res, err
		}
		if open != nil {
			switch open.Status {
			case domain.TaskFailed, domain.TaskInterrupted:
				if !s.queue.RetriesLeft(*open) {
					res.Exhausted++
					continue
				}
				ids = append(ids, open.ID)
				res.Requeued++
			}
			continue
		}
		id, err := s.tasks.Create(ctx, fileID, it.DestinationPath, verifyMode)
		if err != nil {
			return res, fmt.Errorf("create task for %s: %w", it.RelativePath, err)
		}
		ids = append(ids, id)
		res.Created++
	}
	n, err := s.queue.Enqueue(ctx, ids)
	if err != nil {
		return res, err
	}
	res.Enqueued = n
	s.bus.Publish(ctx, events.Event{Type: "backup.started",
		Message: fmt.Sprintf("backup started: %d new, %d re-queued, %d conflicts (%d overwritten), %d retry-exhausted", res.Created, res.Requeued, res.Conflicts, res.Overwritten, res.Exhausted),
		Data:    map[string]any{"created": res.Created, "requeued": res.Requeued, "enqueued": res.Enqueued, "conflicts": res.Conflicts, "overwritten": res.Overwritten, "exhausted": res.Exhausted, "upload_bytes": plan.Upload.Bytes}})
	return res, nil
}
