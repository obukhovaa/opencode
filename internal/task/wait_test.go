package task

import (
	"context"
	"testing"
	"time"
)

func TestWaitForActiveTasks_ReturnsImmediatelyWhenNoneRunning(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistry(func() string { return dir })

	start := time.Now()
	err := r.WaitForActiveTasks(context.Background(), "S1", WaitOptions{IncludeMonitor: true})
	if err != nil {
		t.Fatalf("WaitForActiveTasks on empty session: %v", err)
	}
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Errorf("WaitForActiveTasks on empty session took %v; want immediate", d)
	}
}

func TestWaitForActiveTasks_ClosesAfterAllPendingTerminate(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistry(func() string { return dir })

	id1 := NewTaskID(KindBash)
	id2 := NewTaskID(KindBash)
	if err := r.Register(&Task{ID: id1, SessionID: "S", Kind: KindBash}); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(&Task{ID: id2, SessionID: "S", Kind: KindBash}); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		done <- r.WaitForActiveTasks(context.Background(), "S", WaitOptions{IncludeMonitor: true})
	}()

	// Quick sanity: still blocked.
	select {
	case err := <-done:
		t.Fatalf("WaitForActiveTasks returned prematurely: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	r.MarkFinished(id1, StateCompleted, nil)
	// Still blocked — one task still running.
	select {
	case err := <-done:
		t.Fatalf("WaitForActiveTasks returned after only one task finished: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	r.MarkFinished(id2, StateCompleted, nil)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("WaitForActiveTasks returned error after all finished: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("WaitForActiveTasks did not return after all tasks finished")
	}
}

func TestWaitForActiveTasks_RespectsContextCancel(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistry(func() string { return dir })

	id := NewTaskID(KindBash)
	if err := r.Register(&Task{ID: id, SessionID: "S", Kind: KindBash}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := r.WaitForActiveTasks(ctx, "S", WaitOptions{IncludeMonitor: true})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("WaitForActiveTasks should have returned ctx.Err()")
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("WaitForActiveTasks took %v after ctx deadline; want ~50ms", elapsed)
	}
}

func TestWaitForActiveTasks_ExcludesMonitorWhenIncludeMonitorFalse(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistry(func() string { return dir })

	bashID := NewTaskID(KindBash)
	monitorID := NewTaskID(KindMonitor)
	if err := r.Register(&Task{ID: bashID, SessionID: "S", Kind: KindBash}); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(&Task{ID: monitorID, SessionID: "S", Kind: KindMonitor}); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		done <- r.WaitForActiveTasks(context.Background(), "S", WaitOptions{IncludeMonitor: false})
	}()

	// Finishing only the bash task should unblock the wait — monitor is excluded.
	r.MarkFinished(bashID, StateCompleted, nil)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("WaitForActiveTasks returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("WaitForActiveTasks did not return when bash finished and monitor was excluded")
	}

	// Monitor must still be running.
	if mt, ok := r.Get(monitorID); !ok || mt.State() != StateRunning {
		t.Errorf("monitor task should still be running; got ok=%v state=%v", ok, mt.State())
	}
}

func TestWaitForActiveTasks_SnapshotAtStart_IgnoresLaterRegistrations(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistry(func() string { return dir })

	first := NewTaskID(KindBash)
	if err := r.Register(&Task{ID: first, SessionID: "S", Kind: KindBash}); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		done <- r.WaitForActiveTasks(context.Background(), "S", WaitOptions{IncludeMonitor: true})
	}()

	// After the wait has started, register a second task. The wait MUST
	// NOT observe it — snapshot-at-start semantics.
	time.Sleep(50 * time.Millisecond)
	second := NewTaskID(KindBash)
	if err := r.Register(&Task{ID: second, SessionID: "S", Kind: KindBash}); err != nil {
		t.Fatal(err)
	}

	// Finishing only the FIRST task is sufficient — second is excluded.
	r.MarkFinished(first, StateCompleted, nil)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("WaitForActiveTasks returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("WaitForActiveTasks did not return after the snapshotted task finished (snapshot semantics broken)")
	}

	// Second task should still be running.
	if st, ok := r.Get(second); !ok || st.State() != StateRunning {
		t.Errorf("post-wait task should still be running; got ok=%v state=%v", ok, st.State())
	}
}

func TestPendingForSession_FiltersTerminalAndCrossSession(t *testing.T) {
	dir := t.TempDir()
	r := NewRegistry(func() string { return dir })

	a := NewTaskID(KindBash)
	b := NewTaskID(KindBash)
	c := NewTaskID(KindBash)
	_ = r.Register(&Task{ID: a, SessionID: "S", Kind: KindBash})
	_ = r.Register(&Task{ID: b, SessionID: "S", Kind: KindBash})
	_ = r.Register(&Task{ID: c, SessionID: "OTHER", Kind: KindBash})
	r.MarkFinished(b, StateCompleted, nil) // terminal — excluded

	pending := r.PendingForSession("S", nil)
	if len(pending) != 1 || pending[0].ID != a {
		t.Errorf("PendingForSession=%v, want [%s]", taskIDs(pending), a)
	}

	// With monitor filter, exclude all
	noBash := r.PendingForSession("S", func(t *Task) bool { return t.Kind != KindBash })
	if len(noBash) != 0 {
		t.Errorf("PendingForSession with no-bash filter: want 0, got %d", len(noBash))
	}
}

func taskIDs(ts []*Task) []string {
	out := make([]string, 0, len(ts))
	for _, t := range ts {
		out = append(out, t.ID)
	}
	return out
}
