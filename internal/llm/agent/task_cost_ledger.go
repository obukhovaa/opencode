package agent

import (
	"context"
	"sync"

	"github.com/opencode-ai/opencode/internal/session"
)

// A task keeps its slot until its parent has recorded the run's cost. The
// agent's own busy slot can be released before that accounting finishes.
var activeTaskRuns sync.Map

func (b *agentTool) beginTaskRun(ctx context.Context, child session.Session, resumed bool) (float64, error) {
	if _, busy := activeTaskRuns.LoadOrStore(child.ID, struct{}{}); busy {
		return 0, ErrSessionBusy
	}
	if !resumed {
		return child.Cost, nil
	}
	// Read after acquiring the slot. Another worker may have finished and
	// accounted for an earlier run, even if this process has seen the task.
	current, err := b.sessions.Get(ctx, child.ID)
	if err != nil {
		activeTaskRuns.Delete(child.ID)
		return 0, err
	}
	return current.Cost, nil
}

// RunWith sends one result, then closes the channel after any post-turn
// compaction has recorded its usage. Take the cost snapshot after the close.
func completedTaskRun(done <-chan AgentEvent) AgentEvent {
	result := <-done
	for range done {
	}
	return result
}
