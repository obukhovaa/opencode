package agent

import (
	"context"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/llm/tools"
)

// buildStepToolCtx mirrors the production ctx chain end-to-end:
// flow installs the step-scoped ctx as a value, agent.RunWith derives the
// per-run (turn) ctx from it, processGeneration adds tool values.
func buildStepToolCtx(stepCtx context.Context) (toolCtx context.Context, cancelTurn context.CancelFunc) {
	carrier := context.WithValue(stepCtx, tools.StepScopedContextKey, stepCtx)
	turnCtx, cancelTurn := context.WithCancel(carrier)
	toolCtx = context.WithValue(turnCtx, tools.SessionIDContextKey, "PARENT")
	return toolCtx, cancelTurn
}

// TestSubagentBaseContext_FallsBackToBackground: interactive callers (no
// step scope installed) keep today's unbounded background base.
func TestSubagentBaseContext_FallsBackToBackground(t *testing.T) {
	toolCtx, cancelTurn := context.WithCancel(context.Background())
	base := subagentBaseContext(toolCtx)
	if base != context.Background() {
		t.Fatalf("expected context.Background() fallback, got %v", base)
	}
	// Turn end must not affect the background base.
	cancelTurn()
	if base.Err() != nil {
		t.Fatalf("background base cancelled by turn end: %v", base.Err())
	}
}

// TestSubagentCtx_SurvivesTurnEnd_DiesWithStep pins the D4 contract using
// the exact derivation runAsync performs:
//   - the parent's per-turn ctx ending MUST NOT cancel the subagent, and
//   - the step-scoped ctx being cancelled (deadline or step completion)
//     MUST cancel the subagent.
func TestSubagentCtx_SurvivesTurnEnd_DiesWithStep(t *testing.T) {
	stepCtx, cancelStep := context.WithCancel(context.Background())
	toolCtx, cancelTurn := buildStepToolCtx(stepCtx)

	// Exact runAsync derivation.
	runCtx, cancelTask := context.WithCancel(subagentBaseContext(toolCtx))
	if stepScope := tools.StepScopedContext(toolCtx); stepScope != nil {
		runCtx = context.WithValue(runCtx, tools.StepScopedContextKey, stepScope)
	}
	defer cancelTask()

	// 1. Simulated turn end: the subagent must keep running.
	cancelTurn()
	select {
	case <-runCtx.Done():
		t.Fatal("subagent ctx cancelled by the parent's turn ending")
	case <-time.After(50 * time.Millisecond):
	}

	// 2. Nested spawns see the same step scope (bounded fan-out chains).
	if nested := tools.StepScopedContext(runCtx); nested == nil {
		t.Error("nested async spawn would lose the step scope")
	}

	// 3. Step cancellation (deadline elapsed or step completed) kills it.
	cancelStep()
	select {
	case <-runCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("subagent ctx not cancelled when the step-scoped ctx was cancelled")
	}
}

// TestSubagentCtx_StepDeadlinePropagates: a Step.Timeout deadline on the
// step ctx bounds the subagent's run ctx.
func TestSubagentCtx_StepDeadlinePropagates(t *testing.T) {
	stepCtx, cancelStep := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancelStep()
	toolCtx, cancelTurn := buildStepToolCtx(stepCtx)
	defer cancelTurn()

	runCtx, cancelTask := context.WithCancel(subagentBaseContext(toolCtx))
	defer cancelTask()

	deadline, ok := runCtx.Deadline()
	if !ok {
		t.Fatal("subagent ctx lost the step deadline")
	}
	if until := time.Until(deadline); until > 200*time.Millisecond {
		t.Fatalf("deadline too far out: %v", until)
	}
	select {
	case <-runCtx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("subagent ctx did not expire with the step deadline")
	}
}

// TestSubagentCtx_TaskstopCancelIsScoped: the per-task cancel (taskstop)
// kills only this subagent, not the step ctx or siblings derived from it.
func TestSubagentCtx_TaskstopCancelIsScoped(t *testing.T) {
	stepCtx, cancelStep := context.WithCancel(context.Background())
	defer cancelStep()
	toolCtx, cancelTurn := buildStepToolCtx(stepCtx)
	defer cancelTurn()

	runCtxA, cancelA := context.WithCancel(subagentBaseContext(toolCtx))
	runCtxB, cancelB := context.WithCancel(subagentBaseContext(toolCtx))
	defer cancelB()

	cancelA() // taskstop on A
	if runCtxA.Err() == nil {
		t.Fatal("cancelled subagent ctx should be done")
	}
	if stepCtx.Err() != nil {
		t.Fatal("taskstop must not cancel the step ctx")
	}
	if runCtxB.Err() != nil {
		t.Fatal("taskstop on one subagent must not cancel a sibling")
	}
}

// TestSubagentRunOptions_InheritsNonInteractive pins the GENAI-140 fix: a
// subagent launched from a non-interactive (flow step) turn runs
// non-interactive too — it drains its own background tasks and has its
// sleeps redirected — while an interactive caller's subagent stays
// interactive. The zero-value Run shim switched both off for every
// subagent, which is how a subagent came to busy-wait for 18 minutes.
func TestSubagentRunOptions_InheritsNonInteractive(t *testing.T) {
	flowCtx := context.WithValue(context.Background(), tools.NonInteractiveContextKey, true)
	if !subagentRunOptions(flowCtx).NonInteractive {
		t.Fatal("a flow step's subagent must inherit NonInteractive: true")
	}
	tuiCtx := context.WithValue(context.Background(), tools.NonInteractiveContextKey, false)
	if subagentRunOptions(tuiCtx).NonInteractive {
		t.Fatal("an interactive caller's subagent must stay interactive")
	}
	if subagentRunOptions(context.Background()).NonInteractive {
		t.Fatal("no marker means interactive")
	}
	// The async path reads the marker from the PARENT turn ctx; the detached
	// run ctx carries no values, so deriving from it would always be false.
	stepCtx := context.Background()
	toolCtx, cancel := buildStepToolCtx(stepCtx)
	defer cancel()
	toolCtx = context.WithValue(toolCtx, tools.NonInteractiveContextKey, true)
	runCtx := subagentBaseContext(toolCtx)
	if tools.IsNonInteractive(runCtx) {
		t.Fatal("precondition: the detached base must not carry the marker")
	}
	if !subagentRunOptions(toolCtx).NonInteractive {
		t.Fatal("options must be derived from the caller ctx, not the detached base")
	}
}
