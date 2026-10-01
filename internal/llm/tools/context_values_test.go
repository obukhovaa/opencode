package tools

import (
	"context"
	"testing"
	"time"
)

// TestIsNonInteractive pins the tool-ctx marker contract: set by
// agent.processGeneration for RunOptions{NonInteractive: true} runs,
// absent (or false) everywhere else.
func TestIsNonInteractive(t *testing.T) {
	tests := []struct {
		name string
		ctx  context.Context
		want bool
	}{
		{
			name: "marker set true (non-interactive run)",
			ctx:  context.WithValue(context.Background(), NonInteractiveContextKey, true),
			want: true,
		},
		{
			name: "marker set false (interactive run)",
			ctx:  context.WithValue(context.Background(), NonInteractiveContextKey, false),
			want: false,
		},
		{
			name: "marker absent",
			ctx:  context.Background(),
			want: false,
		},
		{
			name: "marker wrong type",
			ctx:  context.WithValue(context.Background(), NonInteractiveContextKey, "yes"),
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsNonInteractive(tt.ctx); got != tt.want {
				t.Errorf("IsNonInteractive() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestIsNonInteractive_CoexistsWithSessionValues verifies the marker rides
// the same ctx chain as sessionID/messageID without disturbing them.
func TestIsNonInteractive_CoexistsWithSessionValues(t *testing.T) {
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "SESS")
	ctx = context.WithValue(ctx, MessageIDContextKey, "MSG")
	ctx = context.WithValue(ctx, NonInteractiveContextKey, true)

	sessionID, messageID := GetContextValues(ctx)
	if sessionID != "SESS" || messageID != "MSG" {
		t.Errorf("GetContextValues() = (%q, %q), want (SESS, MSG)", sessionID, messageID)
	}
	if !IsNonInteractive(ctx) {
		t.Error("IsNonInteractive() = false, want true")
	}
}

// TestStepScopedContext pins the step-scoped ctx accessor contract used by
// the async task spawn path (see agent-tool-async.go).
func TestStepScopedContext(t *testing.T) {
	t.Run("absent returns nil", func(t *testing.T) {
		if got := StepScopedContext(context.Background()); got != nil {
			t.Errorf("StepScopedContext() = %v, want nil", got)
		}
	})

	t.Run("installed ctx is returned through derived children", func(t *testing.T) {
		stepCtx, cancelStep := context.WithTimeout(context.Background(), time.Hour)
		defer cancelStep()

		// Mirrors the flow runner: install the step ctx as a value, then
		// derive the per-run ctx the way agent.RunWith does.
		carrier := context.WithValue(stepCtx, StepScopedContextKey, stepCtx)
		perRun, cancelRun := context.WithCancel(carrier)

		got := StepScopedContext(perRun)
		if got == nil {
			t.Fatal("StepScopedContext() = nil, want the installed step ctx")
		}
		if _, ok := got.Deadline(); !ok {
			t.Error("retrieved step ctx lost its deadline")
		}

		// Turn end (per-run cancel) must NOT cancel the retrieved step ctx.
		cancelRun()
		if got.Err() != nil {
			t.Errorf("step ctx cancelled by per-run cancel: %v", got.Err())
		}

		// Step cancellation MUST propagate to contexts derived from it.
		derived, cancelDerived := context.WithCancel(got)
		defer cancelDerived()
		cancelStep()
		select {
		case <-derived.Done():
		case <-time.After(time.Second):
			t.Error("derived subagent ctx not cancelled when step ctx was cancelled")
		}
	})

	t.Run("wrong type returns nil", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), StepScopedContextKey, "not-a-ctx")
		if got := StepScopedContext(ctx); got != nil {
			t.Errorf("StepScopedContext() = %v, want nil", got)
		}
	})
}

// TestTurnRequester pins the order detached work (title generation, cron
// jobs) inherits a turn's requester in: a non-blank `requester` flow arg,
// then the per-turn requester.
func TestTurnRequester(t *testing.T) {
	withFlowArgs := func(ctx context.Context, args map[string]string) context.Context {
		return context.WithValue(ctx, FlowArgsContextKey, args)
	}
	turnCtx := WithRequester(context.Background(), "author@example.com")
	tests := []struct {
		name string
		ctx  context.Context
		want string
	}{
		{name: "nil ctx", ctx: nil, want: ""},
		{name: "nothing known", ctx: context.Background(), want: ""},
		{name: "per-turn requester", ctx: turnCtx, want: "author@example.com"},
		{name: "flow arg without a per-turn requester", ctx: withFlowArgs(context.Background(), map[string]string{"requester": "flow@example.com"}), want: "flow@example.com"},
		{name: "flow arg beats the per-turn requester", ctx: withFlowArgs(turnCtx, map[string]string{"requester": "flow@example.com"}), want: "flow@example.com"},
		{name: "blank flow arg falls through", ctx: withFlowArgs(turnCtx, map[string]string{"requester": " \t"}), want: "author@example.com"},
		{name: "flow args without requester fall through", ctx: withFlowArgs(turnCtx, map[string]string{"ticket": "X-1"}), want: "author@example.com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := TurnRequester(tt.ctx); got != tt.want {
				t.Errorf("TurnRequester() = %q, want %q", got, tt.want)
			}
		})
	}
}
