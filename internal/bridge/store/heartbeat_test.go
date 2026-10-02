package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/heartbeat"
)

func TestHeartbeatRoundTrip(t *testing.T) {
	s, _ := newTestStore(t)
	heartbeatRoundTrip(t, s)
}

// heartbeatRoundTrip is shared with the MySQL integration test.
func heartbeatRoundTrip(t *testing.T, s Store) {
	t.Helper()
	ctx := context.Background()

	if _, err := s.GetHeartbeat(ctx, "p", "slack", "app", "D1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing row: want ErrNotFound, got %v", err)
	}

	next := time.Date(2026, 10, 1, 11, 0, 0, 0, time.UTC)
	h := Heartbeat{ProjectID: "p", Channel: "slack", IdentityID: "app", PeerID: "D1"}
	h.State = heartbeat.StateOn
	h.Every = 30 * time.Minute
	h.Window = &heartbeat.Window{Start: 300, End: 1260}
	h.WeekdaysOnly = true
	h.Model = "model-a"
	h.AgendaFile = "notes/agenda.md"
	h.NextBeatAt = next
	if err := s.PutHeartbeat(ctx, h); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetHeartbeat(ctx, "p", "slack", "app", "D1")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != heartbeat.StateOn || got.Every != 30*time.Minute || got.Window == nil ||
		*got.Window != *h.Window || !got.WeekdaysOnly || got.Model != "model-a" ||
		got.AgendaFile != "notes/agenda.md" || !got.NextBeatAt.Equal(next) ||
		!got.LastBeatAt.IsZero() || !got.RemindedAt.IsZero() {
		t.Fatalf("round trip mismatch: %+v", got)
	}

	// Overwrite: defaults back to NULL, bookkeeping set.
	got.Every, got.Window, got.WeekdaysOnly, got.Model, got.AgendaFile = 0, nil, false, "", ""
	got.LastBeatAt, got.LastStatus, got.LastError = next, heartbeat.OutcomeError, "boom"
	if err := s.PutHeartbeat(ctx, got); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListHeartbeats(ctx, "p")
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v %d", err, len(list))
	}
	r := list[0]
	if r.Every != 0 || r.Window != nil || r.WeekdaysOnly || r.Model != "" || r.AgendaFile != "" ||
		r.LastStatus != heartbeat.OutcomeError || r.LastError != "boom" || !r.LastBeatAt.Equal(next) {
		t.Fatalf("overwrite mismatch: %+v", r)
	}
	if other, _ := s.ListHeartbeats(ctx, "q"); len(other) != 0 {
		t.Fatalf("other project sees %d rows", len(other))
	}
}
