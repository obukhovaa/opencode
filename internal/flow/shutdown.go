package flow

import (
	"context"
	"errors"
)

// ErrProcessShutdown is the cancellation cause of a run interrupted because
// its process is shutting down (SIGTERM / SIGINT on `opencode serve`). A step
// cancelled with it keeps its `running` row and emits no failed state, so the
// row stays resumable by a later run with RunOptions.RecoverRunning. Any other
// cancellation (DELETE /flow, a step timeout) still fails the step.
var ErrProcessShutdown = errors.New("process shutting down")

// interruptedByShutdown reports whether ctx was cancelled with
// ErrProcessShutdown. A context.WithoutCancel child drops the cause, so check
// the run's own ctx, not a detached write ctx derived from it.
func interruptedByShutdown(ctx context.Context) bool {
	return errors.Is(context.Cause(ctx), ErrProcessShutdown)
}
