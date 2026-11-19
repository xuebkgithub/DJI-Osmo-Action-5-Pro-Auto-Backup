package app

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"
)

// 浏览器开着页面（SSE 长连接）时停服务：必须很快、干净地退出。
// 2026-11-19 实机更新时这里等满了 10 秒超时并以 exit 1 结束，单元被记为 failed。
func TestShutdownWithOpenEventStream(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, err := New(ctx, writeTestConfig(t, false), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for a.Addr() == "" {
		if time.Now().After(deadline) {
			t.Fatal("server did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	resp, err := http.Get("http://" + a.Addr() + "/api/v1/events/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "event: hello") {
		t.Fatalf("stream did not open: %q %v", line, err)
	}

	start := time.Now()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run must return nil on requested shutdown, got %v", err)
		}
		if d := time.Since(start); d > 3*time.Second {
			t.Fatalf("shutdown took %s with an open event stream", d)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("shutdown hung on the open event stream")
	}
}
