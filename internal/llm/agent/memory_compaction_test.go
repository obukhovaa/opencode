package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/memory"
	"github.com/opencode-ai/opencode/internal/message"
)

// Exercise the real turn and post-turn compaction paths together: the next
// provider request must get a fresh disk snapshot after the old one is hidden
// by the summary boundary, then reuse it on subsequent turns.
func TestSelfCompaction_RefreshesMemoryIndexBeforeNextModelCall(t *testing.T) {
	t.Setenv(config.MemoryHomeEnv, "")
	withFreshTaskRegistry(t)
	withAutoCompact(t, false)
	root := t.TempDir()
	const projectID = "test:memory-compaction"
	cfg := config.Get()
	cfg.Memory = &config.MemoryConfig{Enabled: true, Directory: root, ProjectID: projectID}
	coder := cfg.Agents[config.AgentCoder]
	coder.Tools = nil
	coder.AllowTools = []string{"read", "compact_session"}
	coder.Permission = map[string]any{"read": "allow"}
	cfg.Agents[config.AgentCoder] = coder
	agentregistry.InvalidateRegistry()
	t.Cleanup(agentregistry.InvalidateRegistry)

	index := filepath.Join(root, "projects", memory.Key(projectID), "memory", "MEMORY.md")
	if err := os.MkdirAll(filepath.Dir(index), 0700); err != nil {
		t.Fatal(err)
	}
	writeIndex := func(text string) {
		t.Helper()
		if err := os.WriteFile(index, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	const oldEntry, newEntry = "OLD_MEMORY_ENTRY", "UPDATED_MEMORY_ENTRY"
	writeIndex(oldEntry)
	a, main, summarizer, _ := newSelfCompactingAgent(t, 150, compactSessionCall("compact-memory", "keep the completed task"))
	const sessionID = "memory-self-compaction"
	runTurn := func(text string) {
		t.Helper()
		events, err := a.RunWith(context.Background(), sessionID, text, 0, RunOptions{})
		if err != nil {
			t.Fatal(err)
		}
		for event := range events {
			if event.Error != nil {
				t.Fatal(event.Error)
			}
		}
		waitUntil(t, "session release", func() bool { return !a.IsSessionBusy(sessionID) })
	}
	snapshots := func(msgs []message.Message) []string {
		var found []string
		for _, msg := range msgs {
			if msg.Synthetic && strings.HasPrefix(firstText(msg), "<system-reminder>\n<project-memory-index ") {
				found = append(found, firstText(msg))
			}
		}
		return found
	}
	assertRequestIndex := func(request int, want, absent string) {
		t.Helper()
		main.mu.Lock()
		defer main.mu.Unlock()
		if request >= len(main.requests) {
			t.Fatalf("missing model request %d", request)
		}
		got := snapshots(main.requests[request])
		if len(got) != 1 || !strings.Contains(got[0], want) || strings.Contains(got[0], absent) {
			t.Fatalf("request %d snapshots = %q; want exactly one containing %q and excluding %q", request, got, want, absent)
		}
	}

	runTurn("finish and compact this task")
	if requestCount(main) != 2 || requestCount(summarizer) != 1 {
		t.Fatalf("calls: main=%d summarizer=%d; want 2 and 1", requestCount(main), requestCount(summarizer))
	}
	assertRequestIndex(0, oldEntry, newEntry)
	assertRequestIndex(1, oldEntry, newEntry)
	history, sess, err := a.historyAfterCompaction(context.Background(), sessionID)
	if err != nil || sess.SummaryMessageID == "" || len(snapshots(history)) != 0 {
		t.Fatalf("self-compaction did not consume the old snapshot: summary=%q snapshots=%q error=%v", sess.SummaryMessageID, snapshots(history), err)
	}

	writeIndex(newEntry)
	runTurn("continue after compaction")
	assertRequestIndex(2, newEntry, oldEntry)
	runTurn("continue the same history")
	assertRequestIndex(3, newEntry, oldEntry)
	if requestCount(main) != 4 || requestCount(summarizer) != 1 {
		t.Fatalf("unexpected extra calls: main=%d summarizer=%d", requestCount(main), requestCount(summarizer))
	}
}
