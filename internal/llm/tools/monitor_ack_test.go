package tools

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/task"
)

// The monitor ack states the yield contract (do NOT sleep; a flow step
// holds the turn open) and a silent monitor's scanned-line count is
// visible through tasklist (openspec monitor-tool).
func TestMonitor_AckYieldContractAndScannedLines(t *testing.T) {
	_, cleanup := setupBashBgFixture(t)
	defer cleanup()
	reg := task.GlobalRegistry()

	tool := NewMonitorTool(&allowAllPerms{}, allowAllAgentRegistry{})
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "SESS")
	ctx = context.WithValue(ctx, MessageIDContextKey, "MSG")
	resp, err := tool.Run(ctx, ToolCall{ID: "mon-1", Input: `{"cmd":"sh","args":["-c","echo a; echo b; echo c; sleep 0.3"],"pattern":"zzz-never","description":"silent"}`})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"do NOT sleep", "holds the turn open", "ending your turn", "tasklist"} {
		if !strings.Contains(resp.Content, want) {
			t.Errorf("ack lacks %q:\n%s", want, resp.Content)
		}
	}
	m := regexp.MustCompile(`task_id: (monitor_[A-Z2-7]+)`).FindStringSubmatch(resp.Content)
	if m == nil {
		t.Fatalf("no task_id in ack: %s", resp.Content)
	}
	tk, ok := reg.Get(m[1])
	if !ok {
		t.Fatal("monitor not registered")
	}
	deadline := time.Now().Add(3 * time.Second)
	for tk.ScannedLines() < 3 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if tk.ScannedLines() < 3 {
		t.Fatalf("scanned lines = %d, want >= 3 for a monitor that matched nothing", tk.ScannedLines())
	}
	list, err := NewTaskListTool().Run(ctx, ToolCall{Input: `{"state":"all"}`})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(list.Content, "scanned_lines=") {
		t.Errorf("tasklist must show the scanned-line count: %s", list.Content)
	}
	// Observability only: the monitor was not killed by the counter.
	if tk.State() == task.StateKilled {
		t.Error("the scanned-line counter must never kill a monitor")
	}
	// Let the subprocess finish so the fixture's registry is quiet.
	for tk.State() == task.StateRunning && time.Now().Before(deadline.Add(3*time.Second)) {
		time.Sleep(20 * time.Millisecond)
	}
}
