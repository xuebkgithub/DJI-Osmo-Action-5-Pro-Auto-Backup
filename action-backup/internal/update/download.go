package update

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"
)

// 下载上限。安装包目前约 5 MB，留足余量；超过即判为异常。
const (
	maxPackageBytes = 256 << 20
	maxSumsBytes    = 1 << 20
	maxSigBytes     = 4 << 10
)

// fetchFile 把 url 下载到 dst。先写 dst+".part"，完成后 rename；.part 已存在时用 Range 续传，
// 服务器不支持续传（返回 200）就从头再来。失败按 attempts 次数重试，间隔递增。
// progress 可为 nil；total 未知时为 0。
func fetchFile(ctx context.Context, client *http.Client, url, dst, userAgent string, limit int64, attempts int, backoff time.Duration, progress func(done, total int64)) error {
	var err error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			t := time.NewTimer(backoff * time.Duration(i))
			select {
			case <-ctx.Done():
				t.Stop()
				return ctx.Err()
			case <-t.C:
			}
		}
		if err = fetchOnce(ctx, client, url, dst, userAgent, limit, progress); err == nil {
			return nil
		}
		var pe *permanentError
		if errors.As(err, &pe) || ctx.Err() != nil {
			return err
		}
	}
	return err
}

// permanentError 重试也没用的错误（404、超限）。
type permanentError struct{ err error }

func (e *permanentError) Error() string { return e.err.Error() }
func (e *permanentError) Unwrap() error { return e.err }

func fetchOnce(ctx context.Context, client *http.Client, url, dst, userAgent string, limit int64, progress func(done, total int64)) error {
	part := dst + ".part"
	var offset int64
	if info, err := os.Stat(part); err == nil && info.Mode().IsRegular() {
		offset = info.Size()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return &permanentError{err}
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/octet-stream")
	if offset > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(offset, 10)+"-")
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()

	flags := os.O_WRONLY | os.O_CREATE
	switch resp.StatusCode {
	case http.StatusPartialContent:
		flags |= os.O_APPEND
	case http.StatusOK:
		offset = 0
		flags |= os.O_TRUNC
	case http.StatusRequestedRangeNotSatisfiable:
		// .part 比服务器上的文件还长：作废重来
		_ = os.Remove(part)
		return fmt.Errorf("download %s: stale partial file discarded", url)
	case http.StatusNotFound:
		return &permanentError{fmt.Errorf("download %s: HTTP 404", url)}
	default:
		return fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}
	total := int64(0)
	if resp.ContentLength > 0 {
		total = offset + resp.ContentLength
	}
	if total > limit {
		return &permanentError{fmt.Errorf("download %s: %d bytes exceeds limit %d", url, total, limit)}
	}
	f, err := os.OpenFile(part, flags, 0o640)
	if err != nil {
		return &permanentError{err}
	}
	done := offset
	buf := make([]byte, 64<<10)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if done+int64(n) > limit {
				f.Close()
				_ = os.Remove(part)
				return &permanentError{fmt.Errorf("download %s: exceeds limit %d", url, limit)}
			}
			if _, werr := f.Write(buf[:n]); werr != nil {
				f.Close()
				return &permanentError{werr}
			}
			done += int64(n)
			if progress != nil {
				progress(done, total)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			f.Close()
			return fmt.Errorf("download %s: %w", url, rerr)
		}
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return &permanentError{err}
	}
	if err := f.Close(); err != nil {
		return &permanentError{err}
	}
	if total > 0 && done != total {
		return fmt.Errorf("download %s: got %d of %d bytes", url, done, total)
	}
	return os.Rename(part, dst)
}
