// Package events 提供进程内事件广播与持久化。
package events

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"action-backup/internal/database/repository"
)

// Event 事件。
type Event = repository.EventRecord

// Store 持久化接口。
type Store interface {
	Insert(ctx context.Context, e Event) (int64, error)
}

// Broadcaster 事件总线。
type Broadcaster struct {
	mu    sync.RWMutex
	subs  map[int]chan Event
	next  int
	store Store
	log   *slog.Logger
}

// New 创建总线；store 可为 nil。
func New(store Store, log *slog.Logger) *Broadcaster {
	if log == nil {
		log = slog.Default()
	}
	return &Broadcaster{subs: map[int]chan Event{}, store: store, log: log}
}

// Publish 持久化并分发事件。订阅者缓冲满时丢弃，不阻塞发布者。
func (b *Broadcaster) Publish(ctx context.Context, e Event) {
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now()
	}
	if e.Level == "" {
		e.Level = "info"
	}
	if b.store != nil {
		id, err := b.store.Insert(ctx, e)
		if err != nil {
			b.log.Error("persist event failed", "type", e.Type, "err", err)
		} else {
			e.ID = id
		}
	}
	b.log.Log(ctx, level(e.Level), e.Message, "event", e.Type)
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.subs {
		select {
		case ch <- e:
		default:
		}
	}
}

// PublishTransient 只分发不持久化、不写日志，用于高频进度事件。
func (b *Broadcaster) PublishTransient(e Event) {
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now()
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.subs {
		select {
		case ch <- e:
		default:
		}
	}
}

// Subscribe 订阅事件；返回取消函数。
func (b *Broadcaster) Subscribe(buffer int) (<-chan Event, func()) {
	if buffer <= 0 {
		buffer = 64
	}
	ch := make(chan Event, buffer)
	b.mu.Lock()
	id := b.next
	b.next++
	b.subs[id] = ch
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		if _, ok := b.subs[id]; ok {
			delete(b.subs, id)
			close(ch)
		}
		b.mu.Unlock()
	}
}

func level(s string) slog.Level {
	switch s {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	}
	return slog.LevelInfo
}
