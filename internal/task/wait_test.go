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

func newScopedRegistry(t *testing.T) Registry {
	t.Helper()
	dir := t.TempDir()
	return NewRegistry(func() string { return dir })
}

func registerScoped(t *testing.T, reg Registry, owner, parent string) *Task {
	t.Helper()
	tk := &Task{ID: NewTaskID(KindBash), SessionID: owner, ParentSessionID: parent, Kind: KindBash}
	if err := reg.Register(tk); err != nil {
		t.Fatal(err)
	}
	return tk
}

// The session-and-children scope covers the session and its direct
// children only — never a sibling sharing the same parent/root, never a
// grandchild — and the zero-value scope stays exact.
func TestScope_SessionAndChildrenIsOneLevelOfDescent(t *testing.T) {
	reg := newScopedRegistry(t)
	own := registerScoped(t, reg, "S", "")
	child := registerScoped(t, reg, "C", "S")
	registerScoped(t, reg, "G", "C")         // grandchild of S
	registerScoped(t, reg, "B", "ROOT")      // sibling step: shares a root with A, not a child of A
	registerScoped(t, reg, "A", "ROOT")      // the caller's own row as a step session
	registerScoped(t, reg, "X", "ELSEWHERE") // unrelated

	ids := func(ts []*Task) map[string]bool {
		m := map[string]bool{}
		for _, t := range ts {
			m[t.SessionID] = true
		}
		return m
	}
	exact := ids(reg.PendingForSession("S", nil))
	if len(exact) != 1 || !exact["S"] {
		t.Errorf("exact scope = %v, want only S", exact)
	}
	tree := ids(reg.PendingForSessionTree("S", nil))
	if len(tree) != 2 || !tree["S"] || !tree["C"] {
		t.Errorf("children scope = %v, want S and C (not the grandchild G)", tree)
	}
	if sib := ids(reg.PendingForSessionTree("A", nil)); len(sib) != 1 || !sib["A"] {
		t.Errorf("sibling B must not be in A's scope: %v", sib)
	}
	if lst := ids(reg.ListBySessionTree("S")); len(lst) != 2 || !lst["C"] {
		t.Errorf("ListBySessionTree = %v", lst)
	}
	// A childless session degrades to exact.
	if got := reg.PendingForSessionTree("X", nil); len(got) != 1 || got[0].SessionID != "X" {
		t.Errorf("childless scope = %v", ids(got))
	}
	_ = own
	_ = child
}

// WaitForActiveTasks honors Scope in its own snapshot: with the children
// scope it blocks on the child's task; with the zero value it ignores it.
func TestWaitForActiveTasks_ScopeIsHonoredByTheWaitItself(t *testing.T) {
	reg := newScopedRegistry(t)
	child := registerScoped(t, reg, "C", "S")

	// Exact (zero-value) scope: nothing of S's own is pending → returns at once.
	start := time.Now()
	if err := reg.WaitForActiveTasks(context.Background(), "S", WaitOptions{}); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 200*time.Millisecond {
		t.Fatal("exact scope must not wait on a child's task")
	}

	// Children scope: blocks until the child's task finishes.
	go func() {
		time.Sleep(150 * time.Millisecond)
		reg.MarkFinished(child.ID, StateCompleted, nil)
	}()
	start = time.Now()
	if err := reg.WaitForActiveTasks(context.Background(), "S", WaitOptions{Scope: ScopeSessionAndChildren}); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond {
		t.Fatalf("children scope returned in %v without waiting for the child's task", elapsed)
	}
}

// A monitor's scanned-line counter is observability only.
func TestTask_ScannedLinesCounter(t *testing.T) {
	tk := &Task{ID: NewTaskID(KindMonitor), SessionID: "S", Kind: KindMonitor}
	if tk.ScannedLines() != 0 {
		t.Fatal("fresh task must report 0 scanned lines")
	}
	tk.AddScannedLines(3)
	tk.AddScannedLines(4)
	if tk.ScannedLines() != 7 {
		t.Errorf("scanned = %d, want 7", tk.ScannedLines())
	}
	if tk.State() != StateRunning {
		t.Error("the counter must not touch the state")
	}
}
