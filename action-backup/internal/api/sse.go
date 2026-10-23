package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"action-backup/internal/events"
)

const (
	sseBuffer    = 256
	sseHeartbeat = 15 * time.Second
)

// eventsStream SSE：订阅事件总线并转发；同一任务的多条 backup.progress 在一次刷出中只保留最新。
// 该路由绕过 TimeoutHandler（见 NewRouter）。
func (h *handlers) eventsStream(w http.ResponseWriter, r *http.Request) {
	if h.d.Bus == nil {
		writeError(w, http.StatusNotImplemented, "NOT_IMPLEMENTED", "event bus not available", false)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "NO_FLUSH", "streaming unsupported", false)
		return
	}
	ch, cancel := h.d.Bus.Subscribe(sseBuffer)
	defer cancel()

	hd := w.Header()
	hd.Set("Content-Type", "text/event-stream; charset=utf-8")
	hd.Set("Cache-Control", "no-store")
	hd.Set("Connection", "keep-alive")
	hd.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	hello := map[string]any{"mode": string(h.d.Guard.Mode()), "version": h.d.Version, "server_time": time.Now().UTC().Format(time.RFC3339)}
	if err := writeSSE(w, "hello", 0, hello); err != nil {
		return
	}
	flusher.Flush()

	hb := time.NewTicker(sseHeartbeat)
	defer hb.Stop()
	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-hb.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case e, ok := <-ch:
			if !ok {
				return
			}
			batch := drain(ch, e)
			for _, ev := range batch {
				if err := writeSSE(w, ev.Type, ev.ID, ev); err != nil {
					return
				}
			}
			flusher.Flush()
		}
	}
}

// drain 取出通道中已排队的事件并合并进度：每个任务只保留最后一条 backup.progress。
func drain(ch <-chan events.Event, first events.Event) []events.Event {
	batch := []events.Event{first}
	for len(batch) < sseBuffer {
		select {
		case e, ok := <-ch:
			if !ok {
				return coalesce(batch)
			}
			batch = append(batch, e)
		default:
			return coalesce(batch)
		}
	}
	return coalesce(batch)
}

func coalesce(batch []events.Event) []events.Event {
	if len(batch) < 2 {
		return batch
	}
	last := map[int64]int{}
	for i, e := range batch {
		if e.Type == "backup.progress" && e.TaskID != nil {
			last[*e.TaskID] = i
		}
	}
	if len(last) == 0 {
		return batch
	}
	out := batch[:0]
	for i, e := range batch {
		if e.Type == "backup.progress" && e.TaskID != nil && last[*e.TaskID] != i {
			continue
		}
		out = append(out, e)
	}
	return out
}

func writeSSE(w http.ResponseWriter, event string, id int64, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if id > 0 {
		_, err = fmt.Fprintf(w, "event: %s\nid: %d\ndata: %s\n\n", event, id, data)
	} else {
		_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
	}
	return err
}
