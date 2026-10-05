package tools

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/opencode-ai/opencode/internal/task"
)

type passthroughDeps struct {
	mu      sync.Mutex
	notify  chan struct{}
	pairs   int
	resumed int
}

func (p *passthroughDeps) WritePair(_ context.Context, _ string, _ task.SyntheticPair) error {
	p.mu.Lock()
	p.pairs++
	if p.notify != nil {
		close(p.notify)
		p.notify = nil
	}
	p.mu.Unlock()
	return nil
}
func (p *passthroughDeps) IsSessionBusy(string) bool { return false }
func (p *passthroughDeps) ResumeSession(string, string) {
	p.mu.Lock()
	p.resumed++
	p.mu.Unlock()
}

func setupForToolTest(t *testing.T) (*passthroughDeps, func()) {
	t.Helper()
	task.ResetGlobalRegistry()
	dir := t.TempDir()
	reg := task.NewRegistry(func() string { return dir })
	task.SetGlobalRegistry(reg)
	d := &passthroughDeps{}
	restore := task.SetDeps(d)
	return d, func() { restore(); task.ResetGlobalRegistry() }
}

func TestTaskList_EmptySession(t *testing.T) {
	_, cleanup := setupForToolTest(t)
	defer cleanup()
	tool := NewTaskListTool()
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "s1")
	resp, err := tool.Run(ctx, ToolCall{Input: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.Content, "No background tasks for this session") {
		t.Errorf("unexpected content: %q", resp.Content)
	}
}

func TestTaskList_ListAndFilter(t *testing.T) {
	_, cleanup := setupForToolTest(t)
	defer cleanup()
	reg := task.GlobalRegistry()
	_ = reg.Register(&task.Task{ID: task.NewTaskID(task.KindBash), SessionID: "s1", Kind: task.KindBash, Description: "alpha"})
	_ = reg.Register(&task.Task{ID: task.NewTaskID(task.KindMonitor), SessionID: "s1", Kind: task.KindMonitor, Description: "beta"})
	// Other-session task that must NOT show up.
	_ = reg.Register(&task.Task{ID: task.NewTaskID(task.KindBash), SessionID: "s2", Kind: task.KindBash, Description: "other"})

	tool := NewTaskListTool()
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "s1")
	resp, err := tool.Run(ctx, ToolCall{Input: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(resp.Content, "other") {
		t.Errorf("cross-session leak: %q", resp.Content)
	}
	if !strings.Contains(resp.Content, "alpha") || !strings.Contains(resp.Content, "beta") {
		t.Errorf("missing entries: %q", resp.Content)
	}
}

func TestTaskStop_CrossSessionRefused(t *testing.T) {
	_, cleanup := setupForToolTest(t)
	defer cleanup()
	reg := task.GlobalRegistry()
	id := task.NewTaskID(task.KindBash)
	_ = reg.Register(&task.Task{ID: id, SessionID: "owner", Kind: task.KindBash})

	tool := &taskstopTool{}
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "intruder")
	resp, err := tool.Run(ctx, ToolCall{Input: `{"task_id":"` + id + `"}`})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.Content, "does not belong to this session") {
		t.Errorf("expected cross-session refusal, got: %q", resp.Content)
	}
}

func TestTaskStop_UnknownID(t *testing.T) {
	_, cleanup := setupForToolTest(t)
	defer cleanup()
	tool := &taskstopTool{}
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "s1")
	resp, err := tool.Run(ctx, ToolCall{Input: `{"task_id":"shell_nope"}`})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.Content, "No task found") {
		t.Errorf("expected unknown-id error, got: %q", resp.Content)
	}
}

func TestTaskStop_AlreadyTerminal(t *testing.T) {
	_, cleanup := setupForToolTest(t)
	defer cleanup()
	reg := task.GlobalRegistry()
	id := task.NewTaskID(task.KindBash)
	tk := &task.Task{ID: id, SessionID: "s1", Kind: task.KindBash}
	_ = reg.Register(tk)
	reg.MarkFinished(id, task.StateCompleted, nil)

	tool := &taskstopTool{}
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "s1")
	resp, err := tool.Run(ctx, ToolCall{Input: `{"task_id":"` + id + `"}`})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.Content, "is not running") {
		t.Errorf("expected already-terminal response, got: %q", resp.Content)
	}
}

// Session-and-children scope (background-wait-integrity): a child's task is
// listed with an owner marker and may be killed; a sibling step's is neither.
func TestTaskList_ChildOwnedListedAndMarked_SiblingsHidden(t *testing.T) {
	_, cleanup := setupForToolTest(t)
	defer cleanup()
	reg := task.GlobalRegistry()
	child := task.NewTaskID(task.KindBash)
	_ = reg.Register(&task.Task{ID: child, SessionID: "child", ParentSessionID: "s1", Kind: task.KindBash, Description: "child-work"})
	_ = reg.Register(&task.Task{ID: task.NewTaskID(task.KindBash), SessionID: "s2", ParentSessionID: "root", Kind: task.KindBash, Description: "sibling-work"})
	mon := task.NewTaskID(task.KindMonitor)
	_ = reg.Register(&task.Task{ID: mon, SessionID: "s1", Kind: task.KindMonitor, Description: "watch"})
	tk, _ := reg.Get(mon)
	tk.AddScannedLines(42)

	tool := NewTaskListTool()
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "s1")
	resp, err := tool.Run(ctx, ToolCall{Input: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.Content, child) || !strings.Contains(resp.Content, "owner=child") {
		t.Errorf("child-owned task must be listed with its owner: %q", resp.Content)
	}
	if strings.Contains(resp.Content, "sibling-work") {
		t.Errorf("sibling step's task leaked: %q", resp.Content)
	}
	if !strings.Contains(resp.Content, "scanned_lines=42") {
		t.Errorf("monitor row must carry the scanned-line count: %q", resp.Content)
	}
}

func TestTaskStop_ChildOwnedAllowed_SiblingRefused(t *testing.T) {
	_, cleanup := setupForToolTest(t)
	defer cleanup()
	reg := task.GlobalRegistry()
	child := task.NewTaskID(task.KindBash)
	_ = reg.Register(&task.Task{ID: child, SessionID: "child", ParentSessionID: "s1", Kind: task.KindBash})
	sibling := task.NewTaskID(task.KindBash)
	_ = reg.Register(&task.Task{ID: sibling, SessionID: "s2", ParentSessionID: "root", Kind: task.KindBash})
	// No process behind the fixture task: mark it notified so the kill's
	// completion wait returns at once (the kill path itself is covered by
	// the taskstop tests above).
	if tk, ok := reg.Get(child); ok {
		tk.Notified.Store(true)
	}

	tool := &taskstopTool{permissions: &allowAllPerms{}, registry: allowAllAgentRegistry{}}
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "s1")
	resp, err := tool.Run(ctx, ToolCall{Input: `{"task_id":"` + sibling + `"}`})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.Content, "does not belong to this session") {
		t.Errorf("sibling kill must be refused: %q", resp.Content)
	}
	resp, err = tool.Run(ctx, ToolCall{Input: `{"task_id":"` + child + `"}`})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.Content, "killed") {
		t.Errorf("child-owned kill must proceed: %q", resp.Content)
	}
	if tk, _ := reg.Get(child); tk.State() != task.StateKilled {
		t.Errorf("child task state = %v, want killed", tk.State())
	}
}
