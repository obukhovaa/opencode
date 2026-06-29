package tools

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/task"
)

// captureDeps captures pairs and resume calls for verification.
type captureDeps struct {
	mu          sync.Mutex
	pairs       []task.SyntheticPair
	resumes     int
	notifyReady chan struct{}
}

func (c *captureDeps) WritePair(ctx context.Context, sessionID string, p task.SyntheticPair) error {
	c.mu.Lock()
	c.pairs = append(c.pairs, p)
	if c.notifyReady != nil {
		close(c.notifyReady)
		c.notifyReady = nil
	}
	c.mu.Unlock()
	return nil
}

func (c *captureDeps) IsSessionBusy(string) bool { return false }
func (c *captureDeps) ResumeSession(string)      { c.mu.Lock(); c.resumes++; c.mu.Unlock() }

func (c *captureDeps) collect() []task.SyntheticPair {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]task.SyntheticPair, len(c.pairs))
	copy(out, c.pairs)
	return out
}

func setupBashBgFixture(t *testing.T) (*captureDeps, func()) {
	t.Helper()
	task.ResetGlobalRegistry()
	dir := t.TempDir()
	reg := task.NewRegistry(func() string { return dir })
	task.SetGlobalRegistry(reg)
	deps := &captureDeps{notifyReady: make(chan struct{})}
	restore := task.SetDeps(deps)
	return deps, func() {
		restore()
		task.ResetGlobalRegistry()
	}
}

func waitForPair(t *testing.T, deps *captureDeps, timeout time.Duration) {
	t.Helper()
	deps.mu.Lock()
	ch := deps.notifyReady
	pairsLen := len(deps.pairs)
	deps.mu.Unlock()
	if pairsLen > 0 {
		return
	}
	if ch == nil {
		return
	}
	select {
	case <-ch:
	case <-time.After(timeout):
		t.Fatalf("no completion pair after %v", timeout)
	}
}

func TestBashBackground_AckThenCompletion(t *testing.T) {
	deps, cleanup := setupBashBgFixture(t)
	defer cleanup()

	tool := &bashTool{}
	params := BashParams{
		Command:         "sleep 0.3 && echo hello-bg",
		Description:     "bg test",
		RunInBackground: true,
	}
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "s-bg")
	ctx = context.WithValue(ctx, MessageIDContextKey, "msg-1")

	// Skip permission check by using a safe read-only command path? echo is
	// safe-read-only, but our combined command isn't. Inject the bash rule
	// path manually: bashTool.Run consults registry.EvaluatePermission only
	// when !isSafeReadOnly. We bypass by calling runBackground directly.
	resp, err := tool.runBackground(ctx, ToolCall{ID: "call-1"}, params, t.TempDir(), "s-bg")
	if err != nil {
		t.Fatalf("runBackground: %v", err)
	}
	if !strings.Contains(resp.Content, "Background task started.") {
		t.Errorf("ack missing header: %q", resp.Content)
	}
	if !strings.Contains(resp.Content, "task_id: shell_") {
		t.Errorf("ack missing task_id: %q", resp.Content)
	}
	// Wait for completion notification.
	waitForPair(t, deps, 5*time.Second)
	pairs := deps.collect()
	if len(pairs) != 1 {
		t.Fatalf("pairs: want 1, got %d", len(pairs))
	}
	if !strings.Contains(pairs[0].ToolContent, "hello-bg") {
		t.Errorf("tool content missing output: %q", pairs[0].ToolContent)
	}
	if pairs[0].AssistantToolName != BashToolName {
		t.Errorf("assistant name: want bash got %q", pairs[0].AssistantToolName)
	}
	// Synthetic input must strip run_in_background.
	if strings.Contains(pairs[0].AssistantInput, "run_in_background") {
		t.Errorf("synthetic input still contains run_in_background: %q", pairs[0].AssistantInput)
	}
}

func TestBashBackground_NonZeroExit(t *testing.T) {
	deps, cleanup := setupBashBgFixture(t)
	defer cleanup()

	tool := &bashTool{}
	params := BashParams{
		Command:         "exit 2",
		Description:     "bg fail",
		RunInBackground: true,
	}
	resp, err := tool.runBackground(context.Background(), ToolCall{ID: "call-fail"}, params, t.TempDir(), "s-bg")
	if err != nil {
		t.Fatalf("runBackground: %v", err)
	}
	if !strings.Contains(resp.Content, "task_id:") {
		t.Errorf("ack: %q", resp.Content)
	}
	waitForPair(t, deps, 5*time.Second)
	pairs := deps.collect()
	if len(pairs) != 1 {
		t.Fatalf("pairs: want 1, got %d", len(pairs))
	}
	if !strings.Contains(pairs[0].ToolContent, "Exit code 2") {
		t.Errorf("tool content missing exit code marker: %q", pairs[0].ToolContent)
	}
}

func TestBuildSyntheticBashInput_StripsBackgroundFlag(t *testing.T) {
	input := buildSyntheticBashInput(BashParams{
		Command:         "ls",
		Description:     "list",
		RunInBackground: true,
	})
	if strings.Contains(input, "run_in_background") {
		t.Errorf("input contains run_in_background: %s", input)
	}
	if !strings.Contains(input, "ls") {
		t.Errorf("input missing command: %s", input)
	}
}
