//go:build faultinject && recovery

package recovery

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"action-backup/internal/database"
)

const crashYAML = `
mode: simulation
server: { listen: "127.0.0.1:PORT" }
device: { auto_detect: true, auto_backup: true }
simulation:
  device_source: fixtures
  fixtures:
    attached_on_start: true
    devices:
      - key: sim-cam-1
        serial: SIM0001
        volumes:
          - { type: internal, path: "DIR/cam/internal" }
          - { type: sdcard, path: "DIR/cam/sdcard" }
  allow_local_destination: true
storage: { type: local, local_root: "DIR/nas", destination_root: "DJI-TEST" }
backup: { verify_mode: sha256, buffer_size_mb: 1, checkpoint_bytes_mb: 1, checkpoint_interval_sec: 1, retry_count: 3, reconcile_interval_sec: 1 }
faults:
  enabled: ENABLED
  rules:
    - id: crash-at-second-checkpoint
      point: process.crash
      trigger: { nth_call: 2 }
      action: { kind: crash }
database: { path: "DIR/t.db" }
logging: { level: info, format: text }
`

func buildBinary(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(t.TempDir(), "action-backup-test")
	cmd := exec.Command("go", "build", "-tags", "faultinject", "-o", bin, "./cmd/action-backup")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return bin
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func getJSON(base, path string) (map[string]any, error) {
	resp, err := http.Get(base + path)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out map[string]any
	return out, json.NewDecoder(resp.Body).Decode(&out)
}

func TestProcessCrashAtCheckpointThenRestartResumes(t *testing.T) {
	bin := buildBinary(t)
	dir := t.TempDir()
	for _, p := range []string{"cam/internal/DCIM/DJI_003", "cam/sdcard/DCIM/DJI_003", "nas"} {
		_ = os.MkdirAll(filepath.Join(dir, p), 0o755)
	}
	data := make([]byte, mib(6)+777)
	_, _ = io.ReadFull(rand.Reader, data)
	src := filepath.Join(dir, "cam/sdcard", filepath.FromSlash(bigFile))
	if err := os.WriteFile(src, data, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	want := hex.EncodeToString(sum[:])
	port := freePort(t)
	write := func(name, enabled string) string {
		p := filepath.Join(dir, name)
		y := strings.NewReplacer("DIR", dir, "PORT", fmt.Sprint(port), "ENABLED", enabled).Replace(crashYAML)
		if err := os.WriteFile(p, []byte(y), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	crashCfg := write("crash.yaml", "true")
	cleanCfg := write("clean.yaml", "false")
	base := fmt.Sprintf("http://127.0.0.1:%d", port)

	// 第一次运行：第二个断点落盘后进程被 os.Exit(137)
	logPath := filepath.Join(dir, "run1.log")
	logFile, _ := os.Create(logPath)
	cmd := exec.Command(bin, "--config", crashCfg)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waitErr := make(chan error, 1)
	go func() { waitErr <- cmd.Wait() }()
	select {
	case err := <-waitErr:
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != 137 {
			logs, _ := os.ReadFile(logPath)
			t.Fatalf("expected exit 137, got %v\n%s", err, logs)
		}
	case <-time.After(60 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("process did not crash in time")
	}
	logFile.Close()
	// 崩溃现场：part 存在，数据库里有断点，任务仍是 TRANSFERRING
	part := filepath.Join(dir, "nas/DJI-TEST/sdcard", filepath.FromSlash(bigFile)) + ".part"
	st, err := os.Stat(part)
	if err != nil {
		t.Fatalf("part file after crash: %v", err)
	}
	db, err := database.Open(context.Background(), filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	var offset int64
	var status string
	if err := db.QueryRow(`SELECT c.offset_bytes, t.status FROM checkpoints c JOIN backup_tasks t ON t.id = c.task_id`).Scan(&offset, &status); err != nil {
		t.Fatalf("checkpoint after crash: %v", err)
	}
	db.Close()
	if offset < int64(mib(2)) || offset > st.Size() || status != "TRANSFERRING" {
		t.Fatalf("crash state: offset=%d part=%d status=%s", offset, st.Size(), status)
	}

	// 第二次运行：启动恢复 → INTERRUPTED → 设备就绪自动重新入队 → 从断点续传
	log2, _ := os.Create(filepath.Join(dir, "run2.log"))
	cmd2 := exec.Command(bin, "--config", cleanCfg)
	cmd2.Stdout, cmd2.Stderr = log2, log2
	if err := cmd2.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd2.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() { _ = cmd2.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = cmd2.Process.Kill()
		}
		log2.Close()
	}()
	deadline := time.Now().Add(60 * time.Second)
	var task map[string]any
	for time.Now().Before(deadline) {
		out, err := getJSON(base, "/api/v1/tasks/1")
		if err == nil && out["data"] != nil {
			task = out["data"].(map[string]any)["task"].(map[string]any)
			if task["status"] == "COMPLETED" || task["status"] == "FAILED" {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if task == nil || task["status"] != "COMPLETED" {
		logs, _ := os.ReadFile(filepath.Join(dir, "run2.log"))
		t.Fatalf("task after restart: %v\n%s", task, logs)
	}
	if task["checksum_sha256"] != want || task["retry_count"].(float64) != 1 {
		t.Fatalf("task: %v", task)
	}
	got, err := os.ReadFile(strings.TrimSuffix(part, ".part"))
	if err != nil {
		t.Fatal(err)
	}
	gs := sha256.Sum256(got)
	if hex.EncodeToString(gs[:]) != want {
		t.Fatal("destination content mismatch after crash recovery")
	}
	if _, err := os.Stat(part); err == nil {
		t.Fatal("part file must be gone")
	}
	evs, _ := getJSON(base, "/api/v1/events?limit=100")
	seen := map[string]bool{}
	resumed := false
	for _, e := range evs["data"].([]any) {
		m := e.(map[string]any)
		seen[m["type"].(string)] = true
		if m["type"] == "backup.completed" {
			if d, ok := m["data"].(map[string]any); ok && d["resumed"] == true && d["resume_at"].(float64) == float64(offset) {
				resumed = true
			}
		}
	}
	if !seen["recovery.interrupted"] || !resumed {
		t.Fatalf("expected recovery.interrupted and resume at %d; events=%v", offset, seen)
	}
}
