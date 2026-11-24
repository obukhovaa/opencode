package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/opencode-ai/opencode/internal/session"
)

// A resumed task can return to a worker after another worker has already
// charged intervening runs to the parent in the shared session store.
func TestTaskCostBaselineAfterAnotherWorker(t *testing.T) {
	ctx := context.Background()
	sessions := &memSessions{}
	b := &agentTool{sessions: sessions}
	child := session.Session{ID: t.Name()}
	if _, err := b.beginTaskRun(ctx, child, false); err != nil {
		t.Fatal(err)
	}
	if _, err := b.beginTaskRun(ctx, child, true); !errors.Is(err, ErrSessionBusy) {
		t.Fatalf("overlapping run: got %v, want ErrSessionBusy", err)
	}
	activeTaskRuns.Delete(child.ID)
	// The tool already resolved the child ID before the other worker finished.
	// Both runs have now been recorded, despite the stale snapshot in child.
	if _, err := sessions.AddCost(ctx, child.ID, 3); err != nil {
		t.Fatal(err)
	}
	baseline, err := b.beginTaskRun(ctx, child, true)
	if err != nil {
		t.Fatal(err)
	}
	defer activeTaskRuns.Delete(child.ID)
	if baseline != 3 {
		t.Fatalf("resume baseline = %v, want persisted cost 3", baseline)
	}
}
