package notify

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"action-backup/internal/config"
	"action-backup/internal/events"
)

func TestMeoWSend(t *testing.T) {
	var got struct {
		path string
		body map[string]any
	}
	status := 200
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.path = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&got.body)
		_ = json.NewEncoder(w).Encode(map[string]any{"status": status, "msg": "x"})
	}))
	defer srv.Close()
	m, err := NewMeoW(config.MeoWConfig{Nickname: "熊猫 a", BaseURL: srv.URL + "/"}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Send(context.Background(), Message{Title: "T", Body: "B", URL: "http://x"}); err != nil {
		t.Fatal(err)
	}
	if got.path != "/熊猫%20a" && got.path != "/熊猫 a" {
		t.Fatalf("path %q", got.path)
	}
	if got.body["title"] != "T" || got.body["msg"] != "B" || got.body["url"] != "http://x" {
		t.Fatalf("body %v", got.body)
	}
	status = 404
	if err := m.Send(context.Background(), Message{Body: "B"}); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("business status must surface: %v", err)
	}
	if _, err := NewMeoW(config.MeoWConfig{}, time.Second); err == nil {
		t.Fatal("empty nickname must fail")
	}
}

func TestTelegramSendRedactsToken(t *testing.T) {
	const token = "123456:SECRET-TOKEN"
	var gotPath string
	var gotBody map[string]any
	ok := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		if !ok {
			w.WriteHeader(401)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": 401, "description": "Unauthorized " + token})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer srv.Close()
	tg, err := NewTelegram(config.TelegramConfig{BotToken: token, ChatID: "42", BaseURL: srv.URL}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := tg.Send(context.Background(), Message{Title: "T", Body: "B", URL: "http://x"}); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/bot"+token+"/sendMessage" || gotBody["chat_id"] != "42" || gotBody["text"] != "T\nB\nhttp://x" {
		t.Fatalf("request %s %v", gotPath, gotBody)
	}
	ok = false
	err = tg.Send(context.Background(), Message{Body: "B"})
	if err == nil || strings.Contains(err.Error(), token) || !strings.Contains(err.Error(), "***") {
		t.Fatalf("token must be redacted: %v", err)
	}
	// 连接错误的 URL 也含 token
	dead, _ := NewTelegram(config.TelegramConfig{BotToken: token, ChatID: "42", BaseURL: "http://127.0.0.1:1"}, time.Second)
	if err := dead.Send(context.Background(), Message{Body: "B"}); err == nil || strings.Contains(err.Error(), token) {
		t.Fatalf("dial error must not leak token: %v", err)
	}
	if _, err := NewTelegram(config.TelegramConfig{BotToken: config.Masked, ChatID: "1"}, time.Second); err == nil {
		t.Fatal("masked token must be rejected")
	}
	if tg, err := NewTelegram(config.TelegramConfig{BotToken: token, ChatID: "1", ProxyURL: "socks5://127.0.0.1:1080"}, time.Second); err != nil || tg.client.Transport.(*http.Transport).Proxy == nil {
		t.Fatalf("proxy must be set: %v", err)
	}
}

type fakeChannel struct {
	name string
	mu   sync.Mutex
	msgs []Message
	fail int
}

func (f *fakeChannel) Name() string { return f.name }
func (f *fakeChannel) Send(_ context.Context, m Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail > 0 {
		f.fail--
		return io.ErrUnexpectedEOF
	}
	f.msgs = append(f.msgs, m)
	return nil
}
func (f *fakeChannel) got() []Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Message(nil), f.msgs...)
}

func newTestDispatcher(t *testing.T, ch *fakeChannel, cfg config.NotificationsConfig) (*Dispatcher, *events.Broadcaster, context.Context) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := events.New(nil, log)
	d := New(bus, log, Options{CoalesceWindow: 100 * time.Millisecond, MinInterval: time.Millisecond, RetryBackoff: time.Millisecond, Retries: 2})
	d.newChannel = func(name string, _ config.NotificationsConfig, _ time.Duration) (Channel, error) {
		if name != ch.name {
			return nil, ErrChannelNotConfigured
		}
		return ch, nil
	}
	d.Configure(cfg)
	d.Start(ctx)
	return d, bus, ctx
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timeout")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func enabledCfg() config.NotificationsConfig {
	cfg := config.NotificationsConfig{Enabled: true, SiteURL: "http://site"}
	cfg.MeoW.Enabled, cfg.MeoW.Nickname = true, "n"
	return cfg
}

func TestDispatcherFiltersFormatsAndCoalesces(t *testing.T) {
	ch := &fakeChannel{name: ChannelMeoW}
	d, bus, ctx := newTestDispatcher(t, ch, enabledCfg())
	bus.Publish(ctx, events.Event{Type: "device.attached", Message: "x", Data: map[string]any{"name": "Osmo", "serial": "0A1B", "volumes": 2}})
	bus.Publish(ctx, events.Event{Type: "backup.started", Message: "x", Data: map[string]any{"created": 3, "requeued": 1, "upload_bytes": int64(5 << 30), "conflicts": 0}})
	bus.Publish(ctx, events.Event{Type: "backup.progress", Message: "ignored"})
	bus.Publish(ctx, events.Event{Type: "system.started", Message: "ignored"})
	for i := 0; i < 5; i++ {
		bus.Publish(ctx, events.Event{Type: "task.failed", Message: "failed f.MP4: boom"})
	}
	bus.Publish(ctx, events.Event{Type: "backup.finished", Message: "x", Data: map[string]any{"completed": 4, "failed": 1, "interrupted": 0, "bytes": int64(1 << 20), "duration_sec": int64(75)}})
	waitFor(t, func() bool { return len(ch.got()) >= 4 })
	time.Sleep(150 * time.Millisecond)
	msgs := ch.got()
	if len(msgs) != 4 {
		t.Fatalf("want 4 messages, got %d: %+v", len(msgs), msgs)
	}
	if !strings.Contains(msgs[0].Body, "Osmo 已插入（SN 0A1B），2 个卷") || msgs[0].URL != "http://site" {
		t.Fatalf("attached: %+v", msgs[0])
	}
	if !strings.Contains(msgs[1].Body, "新建 3 个文件，重新入队 1 个，待上传 5.0 GB") {
		t.Fatalf("started: %+v", msgs[1])
	}
	if !strings.Contains(msgs[2].Title, "有失败") || !strings.Contains(msgs[2].Body, "成功 4 个文件 / 1.0 MB，用时 1 分 15 秒，失败 1 个") {
		t.Fatalf("finished: %+v", msgs[2])
	}
	if !strings.Contains(msgs[3].Body, "5 个文件备份失败或中断。最近一条：failed f.MP4: boom") {
		t.Fatalf("coalesced: %+v", msgs[3])
	}
	_ = d
}

func TestDispatcherRespectsSwitches(t *testing.T) {
	ch := &fakeChannel{name: ChannelMeoW}
	cfg := enabledCfg()
	off := false
	cfg.On.DeviceAttached = &off
	d, bus, ctx := newTestDispatcher(t, ch, cfg)
	bus.Publish(ctx, events.Event{Type: "device.attached", Message: "x"})
	bus.Publish(ctx, events.Event{Type: "backup.finished", Message: "x", Data: map[string]any{"completed": 1}})
	waitFor(t, func() bool { return len(ch.got()) == 1 })
	if !strings.Contains(ch.got()[0].Title, "备份完成") {
		t.Fatalf("%+v", ch.got())
	}
	// 关总开关后不再发
	cfg.Enabled = false
	d.Configure(cfg)
	bus.Publish(ctx, events.Event{Type: "backup.finished", Message: "x", Data: map[string]any{"completed": 1}})
	time.Sleep(50 * time.Millisecond)
	if len(ch.got()) != 1 {
		t.Fatalf("disabled dispatcher must not send: %+v", ch.got())
	}
}

func TestDispatcherRetryAndFailureEvent(t *testing.T) {
	ch := &fakeChannel{name: ChannelMeoW, fail: 1}
	_, bus, ctx := newTestDispatcher(t, ch, enabledCfg())
	sub, cancel := bus.Subscribe(16)
	defer cancel()
	bus.Publish(ctx, events.Event{Type: "backup.finished", Message: "x", Data: map[string]any{"completed": 1}})
	waitFor(t, func() bool { return len(ch.got()) == 1 }) // 失败一次后重试成功
	ch.mu.Lock()
	ch.fail = 10
	ch.mu.Unlock()
	bus.Publish(ctx, events.Event{Type: "backup.finished", Message: "x", Data: map[string]any{"completed": 2}})
	deadline := time.After(3 * time.Second)
	for {
		select {
		case e := <-sub:
			if e.Type == "notify.failed" {
				if e.Data["channel"] != ChannelMeoW || e.Level != "warn" {
					t.Fatalf("%+v", e)
				}
				return
			}
		case <-deadline:
			t.Fatal("no notify.failed event")
		}
	}
}

func TestDispatcherTest(t *testing.T) {
	ch := &fakeChannel{name: ChannelTelegram}
	cfg := config.NotificationsConfig{} // 总开关关闭也能测试
	d, _, ctx := newTestDispatcher(t, ch, cfg)
	if r := d.Test(ctx, ChannelTelegram); !r.OK || len(ch.got()) != 1 || !strings.Contains(ch.got()[0].Title, "测试") {
		t.Fatalf("%+v %+v", r, ch.got())
	}
	if r := d.Test(ctx, ChannelMeoW); r.OK || r.Error == "" {
		t.Fatalf("unconfigured channel must report error: %+v", r)
	}
}
