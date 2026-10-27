// Package backup 实现单个任务的流式传输、断点与校验。
package backup

import (
	"context"
	"crypto/sha256"
	"encoding"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"log/slog"
	"os"
	"time"

	"action-backup/internal/database/repository"
	"action-backup/internal/domain"
	"action-backup/internal/faultinject"
	"action-backup/internal/storage"
)

// 取消原因。
var (
	ErrPaused    = errors.New("paused by user")
	ErrCancelled = errors.New("cancelled by user")
)

// Options 传输参数。
type Options struct {
	BufferSize         int
	CheckpointInterval time.Duration
	CheckpointBytes    int64
}

// Deps 依赖。
type Deps struct {
	Target      *storage.Target
	Tasks       repository.Tasks
	Checkpoints repository.Checkpoints
	Inject      faultinject.Injector
	Log         *slog.Logger
}

// Worker 执行传输。
type Worker struct {
	opts Options
	d    Deps
}

// New 创建 Worker。
func New(opts Options, d Deps) *Worker {
	if opts.BufferSize <= 0 {
		opts.BufferSize = 4 << 20
	}
	if opts.CheckpointInterval <= 0 {
		opts.CheckpointInterval = 5 * time.Second
	}
	if opts.CheckpointBytes <= 0 {
		opts.CheckpointBytes = 64 << 20
	}
	if d.Inject == nil {
		d.Inject = faultinject.Noop{}
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	return &Worker{opts: opts, d: d}
}

// Job 一次传输任务。
type Job struct {
	Task       repository.TaskRecord
	SourcePath string
	OnProgress func(bytes, total int64)
}

// Outcome 传输结果。Status 为建议的任务终态。
type Outcome struct {
	Status   domain.TaskStatus
	Err      error
	Bytes    int64
	Checksum string
	Resumed  bool
	ResumeAt int64
}

type sourceIdentity struct {
	size    int64
	modTime time.Time
}

// Run 执行任务。调用方需已把任务置为 TRANSFERRING。
func (w *Worker) Run(ctx context.Context, job Job) Outcome {
	out, err := w.run(ctx, job)
	if err == nil {
		out.Status = domain.TaskCompleted
		return out
	}
	out.Err = err
	cause := context.Cause(ctx)
	switch {
	case errors.Is(err, ErrCancelled) || errors.Is(cause, ErrCancelled):
		out.Status = domain.TaskCancelled
		_ = w.d.Target.RemovePart(context.WithoutCancel(ctx), job.Task.DestinationPath)
	case errors.Is(err, ErrPaused) || errors.Is(cause, ErrPaused):
		out.Status = domain.TaskQueued
	case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
		out.Status = domain.TaskInterrupted
	case errors.Is(err, errSource), errors.Is(err, domain.ErrStorageUnavailable):
		out.Status = domain.TaskInterrupted
	case errors.Is(err, errDest):
		// 写入失败后再探测一次目标：掉线归为 INTERRUPTED，否则 FAILED
		if _, perr := w.d.Target.Probe(context.WithoutCancel(ctx)); perr != nil {
			out.Status = domain.TaskInterrupted
		} else {
			out.Status = domain.TaskFailed
		}
	default:
		out.Status = domain.TaskFailed
	}
	return out
}

var (
	errSource = errors.New("source error")
	errDest   = errors.New("destination error")
	errVerify = errors.New("verification failed")
)

func (w *Worker) run(ctx context.Context, job Job) (Outcome, error) {
	var out Outcome
	t := job.Task
	verifyMode := domain.VerifyMode(t.VerifyMode)

	if err := w.d.Inject.Hit(ctx, faultinject.SourceOpen, faultinject.HitMeta{TaskID: t.ID, RelativePath: t.RelativePath}); err != nil {
		return out, fmt.Errorf("%w: %v", errSource, err)
	}
	src, err := os.OpenFile(job.SourcePath, os.O_RDONLY, 0)
	if err != nil {
		return out, fmt.Errorf("%w: open %s: %v", errSource, job.SourcePath, err)
	}
	defer src.Close()
	ident, err := w.identify(src, t)
	if err != nil {
		return out, err
	}

	if err := w.d.Inject.Hit(ctx, faultinject.DestOpen, faultinject.HitMeta{TaskID: t.ID, RelativePath: t.RelativePath}); err != nil {
		return out, fmt.Errorf("%w: %v", errDest, err)
	}
	part, err := w.d.Target.CreatePart(ctx, t.DestinationPath)
	if err != nil {
		if errors.Is(err, domain.ErrStorageUnavailable) || errors.Is(err, domain.ErrModeReadOnly) {
			return out, err
		}
		return out, fmt.Errorf("%w: %v", errDest, err)
	}
	defer part.Close()

	// 断点恢复：只信任数据库记录的偏移，超出部分截断
	offset, hasher, err := w.resume(ctx, t, part, verifyMode)
	if err != nil {
		return out, err
	}
	out.Resumed, out.ResumeAt = offset > 0, offset
	if _, err := src.Seek(offset, io.SeekStart); err != nil {
		return out, fmt.Errorf("%w: seek: %v", errSource, err)
	}
	if _, err := part.Seek(offset, io.SeekStart); err != nil {
		return out, fmt.Errorf("%w: seek part: %v", errDest, err)
	}

	written := offset
	lastCheckpoint := time.Now()
	sinceCheckpoint := int64(0)
	report := func() {
		if job.OnProgress != nil {
			job.OnProgress(written, ident.size)
		}
	}
	report()
	// 读写流水线：预读 goroutine 读下一块时主循环写当前块，让 USB 读与 CIFS 写重叠。
	// 实机（2026-10-27）串行读写只有 24 MB/s，而 USB 读 ~110 MB/s、CIFS 写 ~70 MB/s，串行上限 1/(1/110+1/70) ≈ 43 MB/s。
	chunks, release, stopRead := w.readAhead(ctx, src)
	defer stopRead()
	for written < ident.size {
		if err := ctx.Err(); err != nil {
			_ = w.checkpoint(context.WithoutCancel(ctx), t, part, written, hasher)
			return out, context.Cause(ctx)
		}
		if err := w.d.Inject.Hit(ctx, faultinject.DeviceDetach, faultinject.HitMeta{TaskID: t.ID, RelativePath: t.RelativePath, Bytes: written}); err != nil {
			_ = w.checkpoint(context.WithoutCancel(ctx), t, part, written, hasher)
			return out, fmt.Errorf("%w: %v", errSource, err)
		}
		if err := w.d.Inject.Hit(ctx, faultinject.SourceRead, faultinject.HitMeta{TaskID: t.ID, RelativePath: t.RelativePath, Bytes: written}); err != nil {
			_ = w.checkpoint(context.WithoutCancel(ctx), t, part, written, hasher)
			return out, fmt.Errorf("%w: %v", errSource, err)
		}
		c, ok := <-chunks
		if !ok {
			// 预读因 ctx 取消而结束
			_ = w.checkpoint(context.WithoutCancel(ctx), t, part, written, hasher)
			return out, context.Cause(ctx)
		}
		if c.n > 0 {
			if err := w.d.Inject.Hit(ctx, faultinject.DestWrite, faultinject.HitMeta{TaskID: t.ID, RelativePath: t.RelativePath, Bytes: written}); err != nil {
				release(c.buf)
				_ = w.checkpoint(context.WithoutCancel(ctx), t, part, written, hasher)
				return out, fmt.Errorf("%w: %v", errDest, err)
			}
			if _, err := part.Write(c.buf[:c.n]); err != nil {
				release(c.buf)
				_ = w.checkpoint(context.WithoutCancel(ctx), t, part, written, hasher)
				return out, fmt.Errorf("%w: write: %v", errDest, err)
			}
			if hasher != nil {
				hasher.Write(c.buf[:c.n])
			}
			written += int64(c.n)
			sinceCheckpoint += int64(c.n)
			out.Bytes = written
		}
		release(c.buf)
		if c.err != nil {
			if c.err == io.EOF {
				break
			}
			_ = w.checkpoint(context.WithoutCancel(ctx), t, part, written, hasher)
			return out, fmt.Errorf("%w: read: %v", errSource, c.err)
		}
		if sinceCheckpoint >= w.opts.CheckpointBytes || time.Since(lastCheckpoint) >= w.opts.CheckpointInterval {
			if err := w.checkpoint(ctx, t, part, written, hasher); err != nil {
				return out, err
			}
			lastCheckpoint, sinceCheckpoint = time.Now(), 0
			report()
		}
	}
	report()
	if written != ident.size {
		return out, fmt.Errorf("%w: source shrank: wrote %d of %d bytes", errSource, written, ident.size)
	}

	// 收尾：持久化、关闭、校验、归档
	if err := w.d.Inject.Hit(ctx, faultinject.DestSync, faultinject.HitMeta{TaskID: t.ID, Bytes: written}); err != nil {
		return out, fmt.Errorf("%w: %v", errDest, err)
	}
	if err := part.Sync(); err != nil {
		return out, fmt.Errorf("%w: fsync: %v", errDest, err)
	}
	if err := part.Close(); err != nil {
		return out, fmt.Errorf("%w: close: %v", errDest, err)
	}
	if _, err := w.d.Tasks.Transition(ctx, t.ID, domain.TaskVerifying, repository.TransitionOptions{Bytes: &written}); err != nil {
		return out, err
	}
	checksum, err := w.verify(ctx, t, job.SourcePath, ident, written, hasher, verifyMode)
	if err != nil {
		if errors.Is(err, errVerify) {
			// 校验失败说明已写入的数据不可信：丢弃断点与临时文件，下次从头重传
			_ = w.d.Checkpoints.Delete(context.WithoutCancel(ctx), t.ID)
			_ = w.d.Target.RemovePart(context.WithoutCancel(ctx), t.DestinationPath)
		}
		return out, err
	}
	out.Checksum = checksum
	if err := w.d.Inject.Hit(ctx, faultinject.DestRename, faultinject.HitMeta{TaskID: t.ID}); err != nil {
		return out, fmt.Errorf("%w: %v", errDest, err)
	}
	if err := w.d.Target.Commit(ctx, t.DestinationPath); err != nil {
		if errors.Is(err, domain.ErrStorageUnavailable) {
			return out, err
		}
		return out, fmt.Errorf("%w: %v", errDest, err)
	}
	// 保留源文件修改时间；失败只告警（NAS 不支持 utimes 时不影响备份结果）
	if t.ModTimeNs > 0 {
		if err := w.d.Target.SetModTime(ctx, t.DestinationPath, time.Unix(0, t.ModTimeNs)); err != nil {
			w.d.Log.Warn("preserve destination mtime failed", "task", t.ID, "err", err)
		}
	}
	if err := w.d.Checkpoints.Delete(ctx, t.ID); err != nil {
		w.d.Log.Warn("delete checkpoint failed", "task", t.ID, "err", err)
	}
	return out, nil
}

// chunk 预读得到的一块数据；err 为 io.EOF 表示源已读完。
type chunk struct {
	buf []byte
	n   int
	err error
}

// readAhead 启动预读 goroutine：两块缓冲轮转，一块在写时另一块在读。
// 返回数据通道、归还缓冲的函数与停止函数；停止后 goroutine 立即退出，通道关闭。
func (w *Worker) readAhead(ctx context.Context, src io.Reader) (<-chan chunk, func([]byte), func()) {
	const depth = 2
	pool := make(chan []byte, depth)
	for i := 0; i < depth; i++ {
		pool <- make([]byte, w.opts.BufferSize)
	}
	chunks := make(chan chunk, depth)
	readCtx, stop := context.WithCancel(ctx)
	go func() {
		defer close(chunks)
		for {
			var buf []byte
			select {
			case <-readCtx.Done():
				return
			case buf = <-pool:
			}
			n, err := src.Read(buf)
			select {
			case chunks <- chunk{buf: buf, n: n, err: err}:
			case <-readCtx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()
	release := func(buf []byte) {
		select {
		case pool <- buf:
		default:
		}
	}
	return chunks, release, stop
}

// identify 复核源文件身份：大小与归一化修改时间必须与任务记录一致。
func (w *Worker) identify(src *os.File, t repository.TaskRecord) (sourceIdentity, error) {
	st, err := src.Stat()
	if err != nil {
		return sourceIdentity{}, fmt.Errorf("%w: stat: %v", errSource, err)
	}
	if !st.Mode().IsRegular() {
		return sourceIdentity{}, fmt.Errorf("%w: not a regular file", errSource)
	}
	id := sourceIdentity{size: st.Size(), modTime: domain.NormalizeModTime(st.ModTime())}
	if id.size != t.FileSize || id.modTime.UnixNano() != domain.NormalizeModTime(time.Unix(0, t.ModTimeNs)).UnixNano() {
		return id, fmt.Errorf("%w: source changed since planning (size %d->%d)", errVerify, t.FileSize, id.size)
	}
	return id, nil
}

// resume 根据断点决定起始偏移并重建哈希状态；不可信时从头开始。
func (w *Worker) resume(ctx context.Context, t repository.TaskRecord, part *os.File, mode domain.VerifyMode) (int64, hash.Hash, error) {
	var hasher hash.Hash
	if mode == domain.VerifySHA256 {
		hasher = sha256.New()
	}
	st, err := part.Stat()
	if err != nil {
		return 0, nil, fmt.Errorf("%w: stat part: %v", errDest, err)
	}
	cp, err := w.d.Checkpoints.Get(ctx, t.ID)
	if err != nil {
		return 0, nil, err
	}
	start := int64(0)
	trusted := false
	if cp != nil && cp.Offset > 0 && cp.Offset <= st.Size() && cp.Offset <= t.FileSize {
		trusted = true
		if hasher != nil {
			if cp.HashState == "" {
				trusted = false
			} else if err := restoreHash(hasher, cp.HashState); err != nil {
				w.d.Log.Warn("checkpoint hash state unusable, restarting file", "task", t.ID, "err", err)
				trusted = false
				hasher = sha256.New()
			}
		}
	}
	if trusted {
		start = cp.Offset
	} else if hasher != nil {
		hasher.Reset()
	}
	if err := part.Truncate(start); err != nil {
		return 0, nil, fmt.Errorf("%w: truncate part to %d: %v", errDest, start, err)
	}
	if !trusted && cp != nil {
		_ = w.d.Checkpoints.Delete(ctx, t.ID)
	}
	return start, hasher, nil
}

// checkpoint 把已写入数据刷盘并记录可信偏移。
func (w *Worker) checkpoint(ctx context.Context, t repository.TaskRecord, part *os.File, offset int64, hasher hash.Hash) error {
	if err := w.d.Inject.Hit(ctx, faultinject.DestSync, faultinject.HitMeta{TaskID: t.ID, Bytes: offset}); err != nil {
		return fmt.Errorf("%w: %v", errDest, err)
	}
	if err := part.Sync(); err != nil {
		return fmt.Errorf("%w: fsync: %v", errDest, err)
	}
	state := ""
	if hasher != nil {
		s, err := marshalHash(hasher)
		if err != nil {
			return err
		}
		state = s
	}
	if err := w.d.Inject.Hit(ctx, faultinject.DBCommit, faultinject.HitMeta{TaskID: t.ID, Bytes: offset}); err != nil {
		return err
	}
	if err := w.d.Checkpoints.Upsert(ctx, repository.Checkpoint{TaskID: t.ID, Offset: offset, PartPath: t.DestinationPath + storage.PartSuffix, HashState: state}); err != nil {
		return fmt.Errorf("persist checkpoint: %w", err)
	}
	partAbs, _ := w.d.Target.Resolve(t.DestinationPath)
	if err := w.d.Inject.Hit(ctx, faultinject.PartCorrupt, faultinject.HitMeta{TaskID: t.ID, RelativePath: t.RelativePath, Bytes: offset, Path: partAbs + storage.PartSuffix}); err != nil {
		return err
	}
	if err := w.d.Inject.Hit(ctx, faultinject.ProcessCrash, faultinject.HitMeta{TaskID: t.ID, Bytes: offset}); err != nil {
		return err
	}
	return nil
}

func (w *Worker) verify(ctx context.Context, t repository.TaskRecord, srcPath string, ident sourceIdentity, written int64, hasher hash.Hash, mode domain.VerifyMode) (string, error) {
	// basic：源身份复核、字节数、临时文件大小
	st, err := os.Stat(srcPath)
	if err != nil {
		return "", fmt.Errorf("%w: re-stat source: %v", errSource, err)
	}
	if st.Size() != ident.size || !domain.NormalizeModTime(st.ModTime()).Equal(ident.modTime) {
		return "", fmt.Errorf("%w: source changed during transfer", errVerify)
	}
	partAbs, err := w.d.Target.Resolve(t.DestinationPath)
	if err != nil {
		return "", err
	}
	pst, err := os.Stat(partAbs + storage.PartSuffix)
	if err != nil {
		return "", fmt.Errorf("%w: stat part: %v", errDest, err)
	}
	if pst.Size() != written || written != ident.size {
		return "", fmt.Errorf("%w: part size %d, written %d, source %d", errVerify, pst.Size(), written, ident.size)
	}
	if mode != domain.VerifySHA256 {
		return "", nil
	}
	want := hex.EncodeToString(hasher.Sum(nil))
	if err := w.d.Inject.Hit(ctx, faultinject.VerifyHash, faultinject.HitMeta{TaskID: t.ID}); err != nil {
		return "", fmt.Errorf("%w: %v", errVerify, err)
	}
	got, err := hashFile(ctx, partAbs+storage.PartSuffix, w.opts.BufferSize)
	if err != nil {
		return "", fmt.Errorf("%w: hash destination: %v", errDest, err)
	}
	if got != want {
		return "", fmt.Errorf("%w: sha256 mismatch source %s destination %s", errVerify, want, got)
	}
	return want, nil
}

func hashFile(ctx context.Context, path string, bufSize int) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	buf := make([]byte, bufSize)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, rerr := f.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return "", rerr
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func marshalHash(h hash.Hash) (string, error) {
	m, ok := h.(encoding.BinaryMarshaler)
	if !ok {
		return "", fmt.Errorf("hash state not serializable")
	}
	b, err := m.MarshalBinary()
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}

func restoreHash(h hash.Hash, state string) error {
	u, ok := h.(encoding.BinaryUnmarshaler)
	if !ok {
		return fmt.Errorf("hash state not restorable")
	}
	b, err := base64.StdEncoding.DecodeString(state)
	if err != nil {
		return err
	}
	return u.UnmarshalBinary(b)
}

// IsVerifyError 判断是否校验失败。
func IsVerifyError(err error) bool { return errors.Is(err, errVerify) }
