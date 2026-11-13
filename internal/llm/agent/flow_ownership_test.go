package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/llm/tools"
)

func TestFlowOwnershipLossBlocksToolBeforeDispatch(t *testing.T) {
	lost := errors.New("lease superseded")
	ctx := tools.WithFlowOwnershipGuard(context.Background(), func(context.Context) error { return lost })
	child := tools.InheritFlowLimits(context.Background(), ctx)
	for _, limit := range []time.Duration{0, time.Second} {
		tool := &stuckTool{name: "must-not-run", release: make(chan struct{})}
		response, err := runToolBounded(child, limit, tool, tools.ToolCall{Name: tool.name})
		close(tool.release)
		if !errors.Is(err, lost) || !response.IsError {
			t.Fatalf("limit=%s response=%+v err=%v", limit, response, err)
		}
	}
}
func TestFlowOwnershipLossBlocksHooksAndModel(t *testing.T) {
	lost := errors.New("lease expired")
	ctx := tools.WithFlowOwnershipGuard(context.Background(), func(context.Context) error { return lost })
	a := &agent{} // any access to a provider or hook factory would fail this test.
	_, hook := a.firePreTool(ctx, "session", "bash", `{}`)
	if !hook.decision.Block {
		t.Fatal("lost owner reached hook dispatch")
	}
	if _, _, err := a.streamAndHandleEvents(ctx, "session", nil, nil, nil); !errors.Is(err, lost) {
		t.Fatalf("model guard: %v", err)
	}
	if event := a.processGeneration(ctx, "session", "work", 1, nil, RunOptions{}); !errors.Is(event.Error, lost) {
		t.Fatalf("turn guard: %v", event.Error)
	}
}
