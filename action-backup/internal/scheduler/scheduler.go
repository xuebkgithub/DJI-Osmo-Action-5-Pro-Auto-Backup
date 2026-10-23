// Package scheduler 单 Worker 任务队列：取 QUEUED 任务、驱动状态机、处理暂停 / 取消。
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"action-backup/internal/backup"
	"action-backup/internal/database/repository"
	"action-backup/internal/domain"
	"action-backup/internal/events"
	"action-backup/internal/platform"
)

// DeviceSource 当前已插入设备。
type DeviceSource interface {
	Attached() []domain.Device
}

// Progress 当前任务进度快照。
type Progress struct {
	TaskID        int64   `json:"task_id"`
	RelativePath  string  `json:"relative_path"`
	Bytes         int64   `json:"bytes"`
	Total         int64   `json:"total"`
	Percent       float64 `json:"percent"`
	BytesPerSec   int64   `json:"bytes_per_sec"`
	StartedAt     string  `json:"started_at"`
	ElapsedSecond int64   `json:"elapsed_seconds"`
}

// State 调度器状态。
type State struct {
	Running bool      `json:"running"`
	Paused  bool      `json:"paused"`
	Current *Progress `json:"current,omitempty"`
}

// Options 调度参数。
type Options struct {
	// MaxRetries 自动重新入队的次数上限（retry_count 达到后不再自动重试）。
	MaxRetries int
	// ReconcileInterval 检查 INTERRUPTED 任务能否重新入队的周期。
	ReconcileInterval time.Duration
}

// TargetProbe 目标可用性探测。
type TargetProbe interface {
	Probe(ctx context.Context) (platform.MountInfo, error)
}

// Scheduler 调度器。
type Scheduler struct {
	tasks   repository.Tasks
	worker  *backup.Worker
	devices DeviceSource
	mounter platform.VolumeMounter
	bus     *events.Broadcaster
	log     *slog.Logger
	opts    Options
	target  TargetProbe

	mu            sync.Mutex
	paused        bool
	running       bool
	wake          chan struct{}
	current       *Progress
	currentCancel context.CancelCauseFunc
	currentID     int64
	lastBytes     int64
	lastReportAt  time.Time
}

// New 创建调度器。
func New(tasks repository.Tasks, worker *backup.Worker, devices DeviceSource, mounter platform.VolumeMounter, bus *events.Broadcaster, log *slog.Logger) *Scheduler {
	return &Scheduler{tasks: tasks, worker: worker, devices: devices, mounter: mounter, bus: bus, log: log, wake: make(chan struct{}, 1),
		opts: Options{MaxRetries: 3, ReconcileInterval: 30 * time.Second}}
}

// Configure 设置重试与重连参数及目标探测。
func (s *Scheduler) Configure(opts Options, target TargetProbe) {
	if opts.MaxRetries >= 0 {
		s.opts.MaxRetries = opts.MaxRetries
	}
	if opts.ReconcileInterval > 0 {
		s.opts.ReconcileInterval = opts.ReconcileInterval
	}
	s.target = target
}

// Start 启动工作循环与重连检查，直到 ctx 取消。
func (s *Scheduler) Start(ctx context.Context) {
	s.mu.Lock()
	s.running = true
	s.mu.Unlock()
	go s.loop(ctx)
	go s.reconcileLoop(ctx)
}

// reconcileLoop 周期性把设备在位、目标可用且未用尽重试次数的 INTERRUPTED 任务重新入队。
func (s *Scheduler) reconcileLoop(ctx context.Context) {
	t := time.NewTicker(s.opts.ReconcileInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if n := s.Reconcile(ctx); n > 0 {
			s.log.Info("re-queued interrupted tasks", "count", n)
		}
	}
}

// Reconcile 执行一次重连检查，返回重新入队的任务数。
func (s *Scheduler) Reconcile(ctx context.Context) int {
	s.mu.Lock()
	paused := s.paused
	s.mu.Unlock()
	if paused {
		return 0
	}
	if s.target != nil {
		if _, err := s.target.Probe(ctx); err != nil {
			return 0
		}
	}
	items, _, err := s.tasks.List(ctx, string(domain.TaskInterrupted), 500, 0)
	if err != nil {
		s.log.Error("list interrupted tasks failed", "err", err)
		return 0
	}
	attached := map[string]bool{}
	for _, d := range s.devices.Attached() {
		for _, v := range d.Volumes {
			attached[d.Key+"|"+v.Key] = true
		}
	}
	n := 0
	for i := len(items) - 1; i >= 0; i-- { // 旧任务优先
		t := items[i]
		if !attached[t.DeviceKey+"|"+t.VolumeKey] {
			continue
		}
		if t.RetryCount >= s.opts.MaxRetries {
			continue
		}
		if err := s.Retry(ctx, t.ID); err == nil {
			n++
		}
	}
	return n
}

// RetriesLeft 判断任务是否还可自动重试。
func (s *Scheduler) RetriesLeft(t repository.TaskRecord) bool {
	return t.RetryCount < s.opts.MaxRetries
}

func (s *Scheduler) loop(ctx context.Context) {
	defer func() {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
	}()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		s.mu.Lock()
		paused := s.paused
		s.mu.Unlock()
		if !paused {
			task, err := s.tasks.NextQueued(ctx)
			if err != nil {
				s.log.Error("query queue failed", "err", err)
			} else if task != nil {
				if s.runTask(ctx, *task) {
					continue
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		case <-ticker.C:
		}
	}
}

func (s *Scheduler) kick() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Enqueue 把 PENDING 任务入队。
func (s *Scheduler) Enqueue(ctx context.Context, ids []int64) (int, error) {
	n := 0
	for _, id := range ids {
		if _, err := s.tasks.Transition(ctx, id, domain.TaskQueued, repository.TransitionOptions{}); err != nil {
			if errors.Is(err, domain.ErrInvalidTransition) {
				continue
			}
			return n, err
		}
		n++
	}
	s.kick()
	return n, nil
}

// Pause 暂停：不再取新任务，当前任务在断点处停下并回到 QUEUED。
func (s *Scheduler) Pause() {
	s.mu.Lock()
	s.paused = true
	cancel := s.currentCancel
	s.mu.Unlock()
	if cancel != nil {
		cancel(backup.ErrPaused)
	}
}

// Resume 恢复。
func (s *Scheduler) Resume() {
	s.mu.Lock()
	s.paused = false
	s.mu.Unlock()
	s.kick()
}

// Cancel 取消任务：正在传输的立即停止，排队的直接置为 CANCELLED。
func (s *Scheduler) Cancel(ctx context.Context, id int64) error {
	s.mu.Lock()
	cancel, isCurrent := s.currentCancel, s.currentID == id && s.currentCancel != nil
	s.mu.Unlock()
	if isCurrent {
		cancel(backup.ErrCancelled)
		return nil
	}
	if _, err := s.tasks.Transition(ctx, id, domain.TaskCancelled, repository.TransitionOptions{}); err != nil {
		return err
	}
	s.bus.Publish(ctx, events.Event{TaskID: &id, Type: "task.cancelled", Message: fmt.Sprintf("task %d cancelled", id)})
	return nil
}

// Retry 把 FAILED / INTERRUPTED 任务重新入队。其它状态（含正在传输）拒绝。
func (s *Scheduler) Retry(ctx context.Context, id int64) error {
	t, err := s.tasks.Get(ctx, id)
	if err != nil {
		return err
	}
	if t == nil {
		return fmt.Errorf("task %d not found", id)
	}
	if t.Status != domain.TaskFailed && t.Status != domain.TaskInterrupted {
		return fmt.Errorf("%w: task %d is %s, only FAILED or INTERRUPTED can be retried", domain.ErrInvalidTransition, id, t.Status)
	}
	if _, err := s.tasks.Transition(ctx, id, domain.TaskQueued, repository.TransitionOptions{}); err != nil {
		return err
	}
	s.bus.Publish(ctx, events.Event{TaskID: &id, Type: "task.retry", Message: fmt.Sprintf("task %d re-queued", id)})
	s.kick()
	return nil
}

// State 当前状态。
func (s *Scheduler) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := State{Running: s.running, Paused: s.paused}
	if s.current != nil {
		c := *s.current
		st.Current = &c
	}
	return st
}

// WaitIdle 等待队列清空且无任务在运行（测试用）。
func (s *Scheduler) WaitIdle(ctx context.Context) error {
	for {
		s.mu.Lock()
		busy := s.currentCancel != nil
		s.mu.Unlock()
		if !busy {
			t, err := s.tasks.NextQueued(ctx)
			if err != nil {
				return err
			}
			if t == nil {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// runTask 执行一个任务。返回 false 表示因暂停未启动（任务仍为 QUEUED）。
// 取消句柄在检查暂停的同一临界区内登记，保证 Pause 一定能命中正在启动或运行的任务。
func (s *Scheduler) runTask(ctx context.Context, t repository.TaskRecord) bool {
	taskID := t.ID
	runCtx, cancel := context.WithCancelCause(ctx)
	started := time.Now()
	s.mu.Lock()
	if s.paused {
		s.mu.Unlock()
		cancel(nil)
		return false
	}
	s.currentCancel, s.currentID = cancel, t.ID
	s.current = &Progress{TaskID: t.ID, RelativePath: t.RelativePath, Total: t.FileSize, StartedAt: started.UTC().Format(time.RFC3339)}
	s.lastReportAt, s.lastBytes = started, t.BytesTransferred
	s.mu.Unlock()
	clear := func() {
		cancel(nil)
		s.mu.Lock()
		s.currentCancel, s.currentID, s.current = nil, 0, nil
		s.mu.Unlock()
	}

	if _, err := s.tasks.Transition(ctx, t.ID, domain.TaskTransferring, repository.TransitionOptions{}); err != nil {
		s.log.Error("start task failed", "task", t.ID, "err", err)
		clear()
		return true
	}
	srcPath, err := s.resolveSource(ctx, t)
	if err != nil {
		clear()
		s.finish(ctx, t, backup.Outcome{Status: domain.TaskInterrupted, Err: err})
		return true
	}
	s.bus.Publish(ctx, events.Event{TaskID: &taskID, Type: "task.started", Message: fmt.Sprintf("transferring %s (%d bytes)", t.RelativePath, t.FileSize),
		Data: map[string]any{"path": t.RelativePath, "size": t.FileSize, "destination": t.DestinationPath}})

	outcome := s.worker.Run(runCtx, backup.Job{Task: t, SourcePath: srcPath, OnProgress: func(b, total int64) { s.onProgress(t, b, total, started) }})
	clear()
	s.finish(ctx, t, outcome)
	return true
}

func (s *Scheduler) onProgress(t repository.TaskRecord, bytes, total int64, started time.Time) {
	s.mu.Lock()
	now := time.Now()
	if s.current == nil || s.current.TaskID != t.ID {
		s.mu.Unlock()
		return
	}
	s.current.Bytes, s.current.Total = bytes, total
	if total > 0 {
		s.current.Percent = float64(bytes) * 100 / float64(total)
	}
	s.current.ElapsedSecond = int64(now.Sub(started).Seconds())
	if dt := now.Sub(s.lastReportAt); dt >= time.Second {
		s.current.BytesPerSec = int64(float64(bytes-s.lastBytes) / dt.Seconds())
		s.lastReportAt, s.lastBytes = now, bytes
		snap := *s.current
		s.mu.Unlock()
		taskID := t.ID
		s.bus.PublishTransient(events.Event{TaskID: &taskID, Type: "backup.progress", Message: "progress",
			Data: map[string]any{"task_id": snap.TaskID, "bytes": snap.Bytes, "total": snap.Total, "percent": snap.Percent, "bytes_per_sec": snap.BytesPerSec}})
		return
	}
	s.mu.Unlock()
}

func (s *Scheduler) finish(ctx context.Context, t repository.TaskRecord, o backup.Outcome) {
	taskID := t.ID
	ctx = context.WithoutCancel(ctx)
	opt := repository.TransitionOptions{Checksum: o.Checksum}
	if o.Err != nil {
		opt.Error = o.Err.Error()
	}
	if o.Bytes > 0 {
		b := o.Bytes
		opt.Bytes = &b
	}
	if _, err := s.tasks.Transition(ctx, t.ID, o.Status, opt); err != nil {
		s.log.Error("finish task failed", "task", t.ID, "status", o.Status, "err", err)
		return
	}
	data := map[string]any{"path": t.RelativePath, "status": string(o.Status), "bytes": o.Bytes, "resumed": o.Resumed, "resume_at": o.ResumeAt}
	switch o.Status {
	case domain.TaskCompleted:
		if o.Checksum != "" {
			data["sha256"] = o.Checksum
		}
		s.bus.Publish(ctx, events.Event{TaskID: &taskID, Type: "backup.completed", Message: fmt.Sprintf("completed %s", t.RelativePath), Data: data})
	case domain.TaskQueued:
		s.bus.Publish(ctx, events.Event{TaskID: &taskID, Type: "task.paused", Message: fmt.Sprintf("paused %s at %d bytes", t.RelativePath, o.Bytes), Data: data})
	case domain.TaskCancelled:
		s.bus.Publish(ctx, events.Event{TaskID: &taskID, Type: "task.cancelled", Message: fmt.Sprintf("cancelled %s", t.RelativePath), Data: data})
	case domain.TaskInterrupted:
		data["error"] = opt.Error
		s.bus.Publish(ctx, events.Event{Level: "warn", TaskID: &taskID, Type: "task.interrupted", Message: fmt.Sprintf("interrupted %s: %s", t.RelativePath, opt.Error), Data: data})
	default:
		data["error"] = opt.Error
		s.bus.Publish(ctx, events.Event{Level: "error", TaskID: &taskID, Type: "task.failed", Message: fmt.Sprintf("failed %s: %s", t.RelativePath, opt.Error), Data: data})
	}
}

// resolveSource 找到任务对应卷当前的挂载路径。
func (s *Scheduler) resolveSource(ctx context.Context, t repository.TaskRecord) (string, error) {
	for _, d := range s.devices.Attached() {
		if d.Key != t.DeviceKey {
			continue
		}
		for _, v := range d.Volumes {
			if v.Key != t.VolumeKey {
				continue
			}
			mp, err := s.mounter.MountReadOnly(ctx, platform.RawVolume{Key: v.Key, LUN: v.LUN, Label: v.Label, Filesystem: v.Filesystem, SizeBytes: v.SizeBytes, MountPath: v.MountPath})
			if err != nil {
				return "", fmt.Errorf("mount volume %s: %w", v.Key, err)
			}
			return filepath.Join(mp, filepath.FromSlash(t.RelativePath)), nil
		}
		return "", fmt.Errorf("volume %s not present on device %s", t.VolumeKey, t.DeviceKey)
	}
	return "", fmt.Errorf("device %s not attached", t.DeviceKey)
}
