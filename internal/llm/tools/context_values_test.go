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
