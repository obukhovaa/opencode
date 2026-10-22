package agent

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/provider"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/memory"
	"github.com/opencode-ai/opencode/internal/message"
)

const memoryLoopEntry = "MEMORY_LOOP_ENTRY"

// withProjectMemory enables memory for the loaded config with an index for
// project "project" and rebuilds the registry, so the coder agent the loop
// tests run resolves it.
func withProjectMemory(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "projects", memory.Key("project"), "memory")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "MEMORY.md"), []byte("- [Topic](topic.md) — "+memoryLoopEntry+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	config.Get().Memory = &config.MemoryConfig{Enabled: true, Directory: root, ProjectID: "project"}
	agentregistry.InvalidateRegistry()
	t.Cleanup(agentregistry.InvalidateRegistry)
}

func snapshotIndexes(msgs []message.Message) (idx []int) {
	for i, m := range msgs {
		if m.Role == message.User && strings.Contains(firstText(m), memoryLoopEntry) {
			idx = append(idx, i)
		}
	}
	return idx
}

func userTurnIndex(msgs []message.Message, text string) int {
	return slices.IndexFunc(msgs, func(m message.Message) bool {
		return m.Role == message.User && !m.Synthetic && firstText(m) == text
	})
}

// A pre-turn compaction summarises the snapshot of the previous turn away;
// processGeneration must put it back, once, ahead of the new user turn.
func TestProcessGeneration_MemoryIndexRestoredAfterPreTurnCompaction(t *testing.T) {
	withFreshTaskRegistry(t)
	withAutoCompact(t, true)
	withProjectMemory(t)
	a, main, _, log := newCompactingAgent(t, 960, nil)
	ctx := context.Background()
	const sess = "sess-memory-preturn"

	for _, content := range []string{"first", "second"} {
		if res := a.processGeneration(ctx, sess, content, 0, nil, RunOptions{}); res.Error != nil {
			t.Fatalf("processGeneration(%q) error: %v", content, res.Error)
		}
	}
	if want := []string{"main", "summarizer", "main"}; !slices.Equal(*log, want) {
		t.Fatalf("call order = %v, want %v — one compaction before the second turn", *log, want)
	}
	for i, content := range []string{"first", "second"} {
		req := main.requests[i]
		snaps := snapshotIndexes(req)
		if len(snaps) != 1 {
			t.Fatalf("request #%d carries %d memory snapshots, want 1", i+1, len(snaps))
		}
		if user := userTurnIndex(req, content); user < 0 || snaps[0] > user {
			t.Errorf("request #%d: snapshot at %d, user turn at %d; want the snapshot first", i+1, snaps[0], user)
		}
	}
	if firstText(main.requests[1][0]) != testSummaryText {
		t.Fatalf("second request does not start at the summary: %v", texts(main.requests[1]))
	}
}

// A compaction between two model calls of one turn drops the snapshot too;
// the turn's next call must carry it again, once, and a later turn must not
// add another.
func TestProcessGeneration_MemoryIndexRestoredAfterInLoopCompaction(t *testing.T) {
	withFreshTaskRegistry(t)
	withAutoCompact(t, true)
	withProjectMemory(t)
	a, main, _, log := newCompactingAgent(t, 960, []tools.BaseTool{noopTool{}})
	calls := 0
	main.respond = func([]message.Message) *provider.ProviderResponse {
		calls++
		if calls == 1 {
			return &provider.ProviderResponse{
				ToolCalls:    []message.ToolCall{{ID: "call-1", Name: "noop", Input: "{}", Finished: true}},
				FinishReason: message.FinishReasonToolUse,
			}
		}
		return &provider.ProviderResponse{Content: "done", FinishReason: message.FinishReasonEndTurn}
	}
	ctx := context.Background()
	const sess = "sess-memory-loop"

	for _, content := range []string{"do the task", "next"} {
		if res := a.processGeneration(ctx, sess, content, 0, nil, RunOptions{}); res.Error != nil {
			t.Fatalf("processGeneration(%q) error: %v", content, res.Error)
		}
	}
	if want := []string{"main", "summarizer", "main", "main"}; !slices.Equal(*log, want) {
		t.Fatalf("call order = %v, want %v — one compaction between the turn's two calls", *log, want)
	}
	for i, req := range main.requests {
		if n := len(snapshotIndexes(req)); n != 1 {
			t.Errorf("model request #%d carries %d memory snapshots, want 1", i+1, n)
		}
	}
	if snaps, user := snapshotIndexes(main.requests[0]), userTurnIndex(main.requests[0], "do the task"); len(snaps) == 1 && (user < 0 || snaps[0] > user) {
		t.Errorf("first request: snapshot at %d, user turn at %d; want the snapshot first", snaps[0], user)
	}
}
