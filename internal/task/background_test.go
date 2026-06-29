package task

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
)

type fakeDeps struct {
	mu          sync.Mutex
	pairs       []recordedPair
	busyOn      map[string]bool
	resumeCalls atomic.Int32
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

func (f *fakeDeps) ResumeSession(sessionID string) {
	f.resumeCalls.Add(1)
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
