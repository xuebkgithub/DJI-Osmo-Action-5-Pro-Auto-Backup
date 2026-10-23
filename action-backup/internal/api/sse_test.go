package api

import (
	"testing"

	"action-backup/internal/events"
)

func TestCoalesceProgress(t *testing.T) {
	a, b := int64(1), int64(2)
	in := []events.Event{
		{Type: "backup.progress", TaskID: &a, Message: "a1"},
		{Type: "device.attached"},
		{Type: "backup.progress", TaskID: &b, Message: "b1"},
		{Type: "backup.progress", TaskID: &a, Message: "a2"},
		{Type: "backup.completed", TaskID: &a},
		{Type: "backup.progress", TaskID: &b, Message: "b2"},
	}
	out := coalesce(in)
	if len(out) != 4 {
		t.Fatalf("got %d events: %+v", len(out), out)
	}
	want := []string{"device.attached", "a2", "backup.completed", "b2"}
	for i, e := range out {
		got := e.Message
		if got == "" {
			got = e.Type
		}
		if got != want[i] {
			t.Fatalf("order: %v", out)
		}
	}
	if len(coalesce(in[:1])) != 1 {
		t.Fatal("single event unchanged")
	}
}
