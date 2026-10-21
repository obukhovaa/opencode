package task

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

type fakeDeps struct {
	mu          sync.Mutex
	pairs       []recordedPair
	busyOn      map[string]bool
	resumeCalls atomic.Int32
	requesters  []string
}

type recordedPair struct {
	sessionID string
	pair      SyntheticPair
}

func (f *fakeDeps) WritePair(ctx context.Context, sessionID string, p SyntheticPair) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pairs = append(f.pairs, recordedPair{sessionID, p})
	return nil
}

func (f *fakeDeps) IsSessionBusy(sessionID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.busyOn[sessionID]
}

func (f *fakeDeps) ResumeSession(sessionID, requester string, _ map[string]any) {
	f.resumeCalls.Add(1)
	f.mu.Lock()
	f.requesters = append(f.requesters, requester)
	f.mu.Unlock()
}

func (f *fakeDeps) snapshot() []recordedPair {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]recordedPair, len(f.pairs))
	copy(out, f.pairs)
	return out
}

func setupTaskFixture(t *testing.T, busy bool) (*fakeDeps, Registry, *Task, func()) {
	t.Helper()
	ResetGlobalRegistry()
	dir := t.TempDir()
	reg := NewRegistry(func() string { return dir })
	SetGlobalRegistry(reg)
	deps := &fakeDeps{busyOn: map[string]bool{}}
	if busy {
		deps.busyOn["s1"] = true
	}
	restore := SetDeps(deps)
	id := NewTaskID(KindBash)
	tk := &Task{ID: id, SessionID: "s1", Kind: KindBash, OriginatingToolName: "bash"}
	if err := reg.Register(tk); err != nil {
		t.Fatal(err)
	}
	return deps, reg, tk, func() {
		restore()
		ResetGlobalRegistry()
	}
}

func TestEnqueue_IdleTriggersResume(t *testing.T) {
	deps, _, tk, cleanup := setupTaskFixture(t, false)
	defer cleanup()
	err := EnqueueTaskCompletion(context.Background(), CompletionInput{
		SessionID:           "s1",
		OriginatingToolName: "bash",
		TaskID:              tk.ID,
		Kind:                KindBash,
		Status:              StatusCompleted,
		Content:             "ok",
		SuppressIfNotified:  true,
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if got := deps.resumeCalls.Load(); got != 1 {
		t.Errorf("resume calls: want 1 got %d", got)
	}
	pairs := deps.snapshot()
	if len(pairs) != 1 {
		t.Fatalf("pairs: want 1 got %d", len(pairs))
	}
	if pairs[0].pair.AssistantToolName != "bash" {
		t.Errorf("assistant tool name: want bash got %q", pairs[0].pair.AssistantToolName)
	}
	if pairs[0].pair.AssistantToolCallID == "" {
		t.Error("assistant tool call id is empty")
	}
	if pairs[0].pair.AssistantToolCallID != pairs[0].pair.ToolToolCallID {
		t.Error("assistant and tool messages have mismatched tool_call_id")
	}
	if pairs[0].pair.ToolContent != "ok" {
		t.Errorf("tool content: want ok got %q", pairs[0].pair.ToolContent)
	}
	if tk.State() != StateCompleted {
		t.Errorf("state: want completed got %v", tk.State())
	}
}

// The auto-resumed turn works for whoever spawned the task, so the resume
// carries the task's requester; an unregistered task ID has none.
func TestEnqueue_ResumeCarriesTaskRequester(t *testing.T) {
	tests := []struct {
		name       string
		requester  string
		unregister bool
		want       string
	}{
		{name: "task requester is passed through", requester: "alice@", want: "alice@"},
		{name: "task without requester resumes without one", want: ""},
		{name: "unregistered task id resumes without one", requester: "alice@", unregister: true, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps, _, tk, cleanup := setupTaskFixture(t, false)
			defer cleanup()
			tk.Requester = tt.requester
			taskID := tk.ID
			if tt.unregister {
				taskID = NewTaskID(KindBash)
			}
			err := EnqueueTaskCompletion(context.Background(), CompletionInput{
				SessionID:           "s1",
				OriginatingToolName: "bash",
				TaskID:              taskID,
				Kind:                KindBash,
				Status:              StatusCompleted,
				Content:             "ok",
				SuppressIfNotified:  true,
			})
			if err != nil {
				t.Fatalf("enqueue: %v", err)
			}
			deps.mu.Lock()
			defer deps.mu.Unlock()
			if len(deps.requesters) != 1 || deps.requesters[0] != tt.want {
				t.Fatalf("resume requesters = %q, want [%q]", deps.requesters, tt.want)
			}
		})
	}
}

func TestEnqueue_BusyDoesNotResume(t *testing.T) {
	deps, _, tk, cleanup := setupTaskFixture(t, true)
	defer cleanup()
	err := EnqueueTaskCompletion(context.Background(), CompletionInput{
		SessionID:           "s1",
		OriginatingToolName: "bash",
		TaskID:              tk.ID,
		Kind:                KindBash,
		Status:              StatusCompleted,
		Content:             "ok",
		SuppressIfNotified:  true,
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if got := deps.resumeCalls.Load(); got != 0 {
		t.Errorf("resume calls: want 0 got %d", got)
	}
}

func TestEnqueue_NotifiedDedupe(t *testing.T) {
	deps, _, tk, cleanup := setupTaskFixture(t, false)
	defer cleanup()
	for range 3 {
		err := EnqueueTaskCompletion(context.Background(), CompletionInput{
			SessionID:           "s1",
			OriginatingToolName: "bash",
			TaskID:              tk.ID,
			Kind:                KindBash,
			Status:              StatusCompleted,
			Content:             "ok",
			SuppressIfNotified:  true,
		})
		if err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}
	pairs := deps.snapshot()
	if len(pairs) != 1 {
		t.Fatalf("dedupe: want 1 pair got %d", len(pairs))
	}
	if got := deps.resumeCalls.Load(); got != 1 {
		t.Errorf("resume calls: want 1 got %d", got)
	}
}

func TestEnqueue_MonitorEventBypassesNotified(t *testing.T) {
	deps, _, tk, cleanup := setupTaskFixture(t, false)
	defer cleanup()
	for range 4 {
		err := EnqueueTaskCompletion(context.Background(), CompletionInput{
			SessionID:           "s1",
			OriginatingToolName: "monitor",
			TaskID:              tk.ID,
			Kind:                KindMonitor,
			Status:              StatusMonitorEvent,
			Content:             "match",
		})
		if err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}
	if len(deps.snapshot()) != 4 {
		t.Fatalf("monitor events: want 4 pairs got %d", len(deps.snapshot()))
	}
	if tk.State() != StateRunning {
		t.Errorf("monitor event should not change state, got %v", tk.State())
	}
	if tk.Notified.Load() {
		t.Error("monitor event must not set notified flag")
	}
}

func TestEnqueue_CronUsesSuppressFalse(t *testing.T) {
	// Cron sets SuppressIfNotified=false; even if Notified is already true,
	// the call writes the pair (cron does not rely on the dedupe gate).
	deps, _, tk, cleanup := setupTaskFixture(t, false)
	defer cleanup()
	tk.Notified.Store(true)
	err := EnqueueTaskCompletion(context.Background(), CompletionInput{
		SessionID:           "s1",
		OriginatingToolName: "task",
		TaskID:              tk.ID,
		Kind:                KindCron,
		Status:              StatusCompleted,
		Content:             "cron result",
		SuppressIfNotified:  false,
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if len(deps.snapshot()) != 1 {
		t.Fatalf("cron write: want 1 pair got %d", len(deps.snapshot()))
	}
}

// TestEnqueue_FlowOwnedSkipsResume pins the flow-owned suppression: a task
// spawned under a flow step's step-scoped ctx writes its completion pair and
// transitions terminal state as usual, but NEVER triggers idle auto-resume —
// the owning step's end-of-turn drain is the reaction mechanism, and after
// the step has ended a resume would zombie-run the routed session under the
// active/primary agent instead of the step's agent (GENAI-239).
func TestEnqueue_FlowOwnedSkipsResume(t *testing.T) {
	deps, reg, tk, cleanup := setupTaskFixture(t, false)
	defer cleanup()
	tk.FlowOwned = true

	err := EnqueueTaskCompletion(context.Background(), CompletionInput{
		SessionID:           "s1",
		OriginatingToolName: "bash",
		TaskID:              tk.ID,
		Kind:                KindBash,
		Status:              StatusCompleted,
		Content:             "ok",
		SuppressIfNotified:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := deps.resumeCalls.Load(); got != 0 {
		t.Fatalf("ResumeSession called %d times for a flow-owned task; want 0", got)
	}
	// Everything short of the resume still happens: the synthetic pair is
	// written and the registry transitions the task to terminal state.
	if pairs := deps.snapshot(); len(pairs) != 1 {
		t.Fatalf("pairs written = %d, want 1", len(pairs))
	}
	got, ok := reg.Get(tk.ID)
	if !ok {
		t.Fatal("task vanished from registry")
	}
	if got.State() != StateCompleted {
		t.Fatalf("task state = %v, want StateCompleted", got.State())
	}
}

// TestEnqueue_FlowOwnedMonitorEventSkipsResume covers the non-terminal
// monitor-event path: flow-owned suppression applies there too (the event
// pair still lands; the in-flight step run reloads it on its next cycle).
func TestEnqueue_FlowOwnedMonitorEventSkipsResume(t *testing.T) {
	deps, _, tk, cleanup := setupTaskFixture(t, false)
	defer cleanup()
	tk.FlowOwned = true

	err := EnqueueTaskCompletion(context.Background(), CompletionInput{
		SessionID:           "s1",
		OriginatingToolName: "monitor",
		TaskID:              tk.ID,
		Kind:                KindMonitor,
		Status:              StatusMonitorEvent,
		Content:             "match: ERROR",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := deps.resumeCalls.Load(); got != 0 {
		t.Fatalf("ResumeSession called %d times for a flow-owned monitor event; want 0", got)
	}
	if pairs := deps.snapshot(); len(pairs) != 1 {
		t.Fatalf("pairs written = %d, want 1", len(pairs))
	}
}

// A completion that finds the session busy skips its resume and records it,
// with the task's requester, for the holder to take on release; a
// flow-owned task records nothing, and an idle session's resume clears a
// stale record.
func TestEnqueue_BusyRecordsSkippedResume(t *testing.T) {
	deps, _, tk, cleanup := setupTaskFixture(t, true)
	defer cleanup()
	tk.Requester = "alice@"
	tk.JobArgs = map[string]any{"ticket": "T-1"}
	t.Cleanup(func() { TakeSkippedResume("s1") })

	enqueue := func(id string) {
		t.Helper()
		if err := EnqueueTaskCompletion(context.Background(), CompletionInput{
			SessionID: "s1", OriginatingToolName: "bash", TaskID: id,
			Kind: KindBash, Status: StatusCompleted, Content: "ok", SuppressIfNotified: true,
		}); err != nil {
			t.Fatal(err)
		}
	}
	enqueue(tk.ID)
	if got := deps.resumeCalls.Load(); got != 0 {
		t.Fatalf("ResumeSession called %d times on a busy session", got)
	}
	r, ok := TakeSkippedResume("s1")
	if !ok || r.Requester != "alice@" || r.JobArgs["ticket"] != "T-1" {
		t.Fatalf("TakeSkippedResume = %+v, %v; want alice@ with the task's job args, true", r, ok)
	}
	if _, ok := TakeSkippedResume("s1"); ok {
		t.Fatal("TakeSkippedResume did not clear the record")
	}

	// ResumeIfIdle refuses while busy, resumes once idle.
	if ResumeIfIdle("s1", r) {
		t.Fatal("ResumeIfIdle resumed a busy session")
	}
	deps.mu.Lock()
	deps.busyOn["s1"] = false
	deps.mu.Unlock()
	if !ResumeIfIdle("s1", r) || deps.resumeCalls.Load() != 1 {
		t.Fatal("ResumeIfIdle did not resume an idle session")
	}

	// Flow-owned: never resumed, so never recorded.
	deps.mu.Lock()
	deps.busyOn["s1"] = true
	deps.mu.Unlock()
	reg := GlobalRegistry()
	flowTask := &Task{ID: NewTaskID(KindBash), SessionID: "s1", Kind: KindBash, OriginatingToolName: "bash", FlowOwned: true}
	if err := reg.Register(flowTask); err != nil {
		t.Fatal(err)
	}
	enqueue(flowTask.ID)
	if _, ok := TakeSkippedResume("s1"); ok {
		t.Error("a flow-owned completion recorded a skipped resume")
	}
}

type handoffDeps struct {
	*fakeDeps
	busy func(string) bool
}

func (d *handoffDeps) IsSessionBusy(sessionID string) bool { return d.busy(sessionID) }

func TestEnqueue_ReleaseDuringBusyCheckResumesOnce(t *testing.T) {
	for _, busySnapshot := range []bool{true, false} {
		t.Run(fmt.Sprintf("busy=%v", busySnapshot), func(t *testing.T) {
			deps, _, tk, cleanup := setupTaskFixture(t, true)
			defer cleanup()
			t.Cleanup(func() { TakeSkippedResume("s1") })
			restore := SetDeps(&handoffDeps{fakeDeps: deps, busy: func(sessionID string) bool {
				// The run releases its slot and takes its pending resume while
				// Enqueue is checking the slot. Exercise either observed value.
				if pending, ok := TakeSkippedResume(sessionID); ok {
					deps.ResumeSession(sessionID, pending.Requester, pending.JobArgs)
				}
				return busySnapshot
			}})
			defer restore()
			if err := EnqueueTaskCompletion(context.Background(), CompletionInput{
				SessionID: "s1", OriginatingToolName: "bash", TaskID: tk.ID,
				Kind: KindBash, Status: StatusCompleted, Content: "done", SuppressIfNotified: true,
			}); err != nil {
				t.Fatal(err)
			}
			if got := deps.resumeCalls.Load(); got != 1 {
				t.Fatalf("resume calls = %d, want exactly one", got)
			}
			if _, ok := TakeSkippedResume("s1"); ok {
				t.Error("completion record stranded after the slot was released")
			}
		})
	}
}
