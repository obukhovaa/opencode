package tools

import (
	"context"
	"errors"
	"fmt"
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
	requesters  []string // requester passed to each ResumeSession
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
func (c *captureDeps) ResumeSession(_, requester string) {
	c.mu.Lock()
	c.resumes++
	c.requesters = append(c.requesters, requester)
	c.mu.Unlock()
}

// waitForResume returns the requester of the first ResumeSession call. The
// resume follows the pair write, so waitForPair alone does not cover it.
func waitForResume(t *testing.T, deps *captureDeps, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		deps.mu.Lock()
		if len(deps.requesters) > 0 {
			r := deps.requesters[0]
			deps.mu.Unlock()
			return r
		}
		deps.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no ResumeSession call after %v", timeout)
	return ""
}

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

// A background task's completion auto-resumes the idle session; that turn
// works for whoever spawned the task, so the resume must carry the
// spawning turn's requester (the resumed run has no other source for it).
func TestBackgroundTaskResumeCarriesRequester(t *testing.T) {
	tests := []struct {
		name  string
		spawn func(ctx context.Context, dir string) error
	}{
		{
			name: "bash run_in_background",
			spawn: func(ctx context.Context, dir string) error {
				_, err := (&bashTool{}).runBackground(ctx, ToolCall{ID: "call-req"}, BashParams{
					Command:         "echo done",
					Description:     "bg requester",
					RunInBackground: true,
				}, dir, "s-req")
				return err
			},
		},
		{
			name: "monitor",
			spawn: func(ctx context.Context, dir string) error {
				resp, err := NewMonitorToolForTest(nil, &stubRegistry{}).Run(ctx, ToolCall{
					ID:    "call-req",
					Input: fmt.Sprintf(`{"cmd":"bash","args":["-c","echo done"],"pattern":"NEVER","cwd":%q}`, dir),
				})
				if err == nil && resp.IsError {
					err = errors.New(resp.Content)
				}
				return err
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps, cleanup := setupBashBgFixture(t)
			defer cleanup()

			ctx := context.WithValue(context.Background(), SessionIDContextKey, "s-req")
			ctx = context.WithValue(ctx, MessageIDContextKey, "msg-req")
			ctx = WithRequester(ctx, "alice@")
			if err := tt.spawn(ctx, t.TempDir()); err != nil {
				t.Fatalf("spawn: %v", err)
			}
			if got := waitForResume(t, deps, 5*time.Second); got != "alice@" {
				t.Fatalf("resume requester = %q, want alice@", got)
			}
		})
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
