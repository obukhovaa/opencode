package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/llm/provider"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/permission"
)

// deniedTool fails every call with err: a permission.RuleDeniedError for a
// config rule, permission.ErrorPermissionDenied for a rejected prompt.
type deniedTool struct {
	name string
	err  error
}

func (d deniedTool) Info() tools.ToolInfo { return tools.ToolInfo{Name: d.name, Description: "denied"} }
func (d deniedTool) Run(context.Context, tools.ToolCall) (tools.ToolResponse, error) {
	return tools.NewEmptyResponse(), d.err
}
func (deniedTool) AllowParallelism(tools.ToolCall, []tools.ToolCall) bool { return true }
func (deniedTool) IsBaseline() bool                                       { return true }

// slowTool succeeds after a delay, long enough for a sibling's denial to
// land first when both run in parallel.
type slowTool struct{ ran atomic.Int32 }

func (*slowTool) Info() tools.ToolInfo { return tools.ToolInfo{Name: "slow", Description: "slow"} }
func (s *slowTool) Run(ctx context.Context, _ tools.ToolCall) (tools.ToolResponse, error) {
	select {
	case <-time.After(50 * time.Millisecond):
	case <-ctx.Done():
		return tools.NewEmptyResponse(), ctx.Err()
	}
	s.ran.Add(1)
	return tools.NewTextResponse("slow done"), nil
}
func (*slowTool) AllowParallelism(tools.ToolCall, []tools.ToolCall) bool { return true }
func (*slowTool) IsBaseline() bool                                       { return true }

func toolTurn(calls ...message.ToolCall) *provider.ProviderResponse {
	return &provider.ProviderResponse{ToolCalls: calls, FinishReason: message.FinishReasonToolUse}
}

// call builds a tool call whose input differs per n, so loop detection
// never fires.
func call(name string, n int) message.ToolCall {
	return message.ToolCall{ID: fmt.Sprintf("%s-%d", name, n), Name: name, Input: fmt.Sprintf(`{"n":%d}`, n), Finished: true}
}

func toolResultsOf(t *testing.T, a *agent, sessionID string) []message.ToolResult {
	t.Helper()
	msgs, err := a.messages.List(context.Background(), sessionID)
	if err != nil {
		t.Fatal(err)
	}
	var out []message.ToolResult
	for _, m := range msgs {
		out = append(out, m.ToolResults()...)
	}
	return out
}

func ruleDenied() error { return permission.DeniedByRule("bash", "rm -rf build") }

func TestRuleDeniedError_MatchesPermissionDenied(t *testing.T) {
	err := fmt.Errorf("wrapped: %w", ruleDenied())
	if !errors.Is(err, permission.ErrorPermissionDenied) {
		t.Error("a rule denial must still match ErrorPermissionDenied")
	}
	rd, ok := permission.AsRuleDenied(err)
	if !ok || rd.ToolName != "bash" || rd.Input != "rm -rf build" {
		t.Errorf("AsRuleDenied = %+v, %v", rd, ok)
	}
	if _, ok := permission.AsRuleDenied(permission.ErrorPermissionDenied); ok {
		t.Error("a rejected prompt is not a rule denial")
	}
}

// A config-rule deny is an error tool result the model reacts to; the run
// goes on to the model's next turn.
func TestRuleDenialDoesNotEndTheRun(t *testing.T) {
	withFreshTaskRegistry(t)
	p := &scriptedProvider{respond: func(n int, _ bool) *provider.ProviderResponse {
		if n == 1 {
			return toolTurn(call("bash", 1))
		}
		return endTurn()
	}}
	a := newLoopAgentWithTools(t, p, []tools.BaseTool{deniedTool{name: "bash", err: ruleDenied()}})

	res := a.processGeneration(context.Background(), "s-soft", "clean up", 0, nil, RunOptions{NonInteractive: true})

	if res.Error != nil {
		t.Fatalf("processGeneration: %v", res.Error)
	}
	if got := p.callCount(); got != 2 {
		t.Fatalf("provider calls = %d, want 2 (the model must get a turn after the denial)", got)
	}
	if got := res.Message.FinishReason(); got != message.FinishReasonEndTurn {
		t.Errorf("finish reason = %q, want end_turn", got)
	}
	results := toolResultsOf(t, a, "s-soft")
	if len(results) != 1 || !results[0].IsError {
		t.Fatalf("tool results = %+v, want one error result", results)
	}
	for _, want := range []string{"configured permission rule", `"rm -rf build"`, "do not reach the same effect another way"} {
		if !strings.Contains(results[0].Content, want) {
			t.Errorf("denial message missing %q: %s", want, results[0].Content)
		}
	}
}

// A human rejecting the prompt still ends the run, as before.
func TestRejectedPromptStillEndsTheRun(t *testing.T) {
	withFreshTaskRegistry(t)
	p := &scriptedProvider{respond: func(n int, _ bool) *provider.ProviderResponse {
		if n == 1 {
			return toolTurn(call("bash", 1), call("other", 2))
		}
		return endTurn()
	}}
	a := newLoopAgentWithTools(t, p, []tools.BaseTool{
		deniedTool{name: "bash", err: permission.ErrorPermissionDenied},
		noopTool{},
	})

	res := a.processGeneration(context.Background(), "s-hard", "clean up", 0, nil, RunOptions{NonInteractive: true})

	if res.Error != nil {
		t.Fatalf("processGeneration: %v", res.Error)
	}
	if got := p.callCount(); got != 1 {
		t.Fatalf("provider calls = %d, want 1", got)
	}
	if got := res.Message.FinishReason(); got != message.FinishReasonPermissionDenied {
		t.Errorf("finish reason = %q, want permission_denied", got)
	}
}

// In a parallel group a rule denial leaves its siblings running, where a
// rejected prompt cancels them.
func TestRuleDenialDoesNotCancelParallelSiblings(t *testing.T) {
	withFreshTaskRegistry(t)
	p := &scriptedProvider{respond: func(n int, _ bool) *provider.ProviderResponse {
		if n == 1 {
			return toolTurn(call("bash", 1), call("slow", 2))
		}
		return endTurn()
	}}
	slow := &slowTool{}
	a := newLoopAgentWithTools(t, p, []tools.BaseTool{deniedTool{name: "bash", err: ruleDenied()}, slow})
	a.allowParallelism = true

	res := a.processGeneration(context.Background(), "s-par", "clean up", 0, nil, RunOptions{NonInteractive: true})

	if res.Error != nil {
		t.Fatalf("processGeneration: %v", res.Error)
	}
	if slow.ran.Load() != 1 {
		t.Fatal("the sibling call was cancelled by a rule denial")
	}
	results := toolResultsOf(t, a, "s-par")
	if len(results) != 2 || results[1].Content != "slow done" {
		t.Fatalf("tool results = %+v", results)
	}
	if p.callCount() != 2 {
		t.Errorf("provider calls = %d, want 2", p.callCount())
	}
}

// A model that does nothing but call denied tools is stopped after
// maxConsecutiveRuleDeniedTurns turns.
func TestRuleDenialBreakerConsecutiveTurns(t *testing.T) {
	withFreshTaskRegistry(t)
	p := &scriptedProvider{respond: func(n int, _ bool) *provider.ProviderResponse {
		return toolTurn(call("bash", n))
	}}
	a := newLoopAgentWithTools(t, p, []tools.BaseTool{deniedTool{name: "bash", err: ruleDenied()}})

	res := a.processGeneration(context.Background(), "s-consec", "clean up", 0, nil, RunOptions{NonInteractive: true})

	if res.Error != nil {
		t.Fatalf("processGeneration: %v", res.Error)
	}
	if got := p.callCount(); got != maxConsecutiveRuleDeniedTurns {
		t.Fatalf("provider calls = %d, want %d", got, maxConsecutiveRuleDeniedTurns)
	}
	if got := res.Message.FinishReason(); got != message.FinishReasonPermissionDenied {
		t.Errorf("finish reason = %q, want permission_denied", got)
	}
}

// Denials mixed with progress never trip the consecutive bound, but the run
// total does.
func TestRuleDenialBreakerRunTotal(t *testing.T) {
	withFreshTaskRegistry(t)
	p := &scriptedProvider{respond: func(n int, _ bool) *provider.ProviderResponse {
		return toolTurn(call("bash", n), call("noop", n))
	}}
	a := newLoopAgentWithTools(t, p, []tools.BaseTool{deniedTool{name: "bash", err: ruleDenied()}, noopTool{}})

	res := a.processGeneration(context.Background(), "s-total", "clean up", 0, nil, RunOptions{NonInteractive: true})

	if res.Error != nil {
		t.Fatalf("processGeneration: %v", res.Error)
	}
	if got := p.callCount(); got != maxRuleDenialsPerRun {
		t.Fatalf("provider calls = %d, want %d", got, maxRuleDenialsPerRun)
	}
	if got := res.Message.FinishReason(); got != message.FinishReasonPermissionDenied {
		t.Errorf("finish reason = %q, want permission_denied", got)
	}
}

func TestRuleDenialBreakerObserveTurn(t *testing.T) {
	type turn struct{ denied, results int }
	tests := []struct {
		name      string
		turns     []turn
		wantTrip  int // index of the turn that trips, -1 for none
		wantWhy   string
		wantTotal int
	}{
		{name: "no denials", turns: []turn{{0, 2}, {0, 1}}, wantTrip: -1},
		{name: "three denied-only turns", turns: []turn{{1, 1}, {2, 2}, {1, 1}}, wantTrip: 2, wantWhy: "consecutive"},
		{name: "progress resets the streak", turns: []turn{{1, 1}, {1, 1}, {0, 1}, {1, 1}, {1, 1}}, wantTrip: -1, wantTotal: 4},
		{name: "mixed turn resets the streak", turns: []turn{{1, 1}, {1, 1}, {1, 2}, {1, 1}}, wantTrip: -1, wantTotal: 4},
		{name: "run total", turns: []turn{{4, 5}, {4, 5}, {2, 3}}, wantTrip: 2, wantWhy: "denied in this run"},
		{name: "one parallel turn is not three turns", turns: []turn{{3, 3}}, wantTrip: -1, wantTotal: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b ruleDenialBreaker
			trippedAt := -1
			var why string
			for i, tr := range tt.turns {
				if tripped, reason := b.observeTurn(tr.denied, tr.results); tripped {
					trippedAt, why = i, reason
					break
				}
			}
			if trippedAt != tt.wantTrip {
				t.Fatalf("tripped at turn %d, want %d (%s)", trippedAt, tt.wantTrip, why)
			}
			if tt.wantWhy != "" && !strings.Contains(why, tt.wantWhy) {
				t.Errorf("reason = %q, want it to mention %q", why, tt.wantWhy)
			}
			if tt.wantTrip == -1 && b.total != tt.wantTotal {
				t.Errorf("total = %d, want %d", b.total, tt.wantTotal)
			}
		})
	}
}
