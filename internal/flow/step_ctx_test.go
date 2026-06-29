package flow

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestStepCtx_StepTimeoutWins pins the precedence chain:
// Step.Timeout > OPENCODE_NON_INTERACTIVE_TASK_WAIT_TIMEOUT > parent ctx.
func TestStepCtx_StepTimeoutWins(t *testing.T) {
	t.Setenv("OPENCODE_NON_INTERACTIVE_TASK_WAIT_TIMEOUT", "1h")
	resetEnvCacheForTest()

	step := Step{ID: "s1", Timeout: "5s"}
	ctx, cancel := stepCtx(context.Background(), step)
	defer cancel()

	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("expected a deadline; got none")
	}
	remaining := time.Until(deadline)
	if remaining > 6*time.Second || remaining < 4*time.Second {
		t.Errorf("expected deadline ~5s from now; remaining=%v", remaining)
	}
}

// TestStepCtx_EnvFallback: no Step.Timeout, env var IS set → env value used.
func TestStepCtx_EnvFallback(t *testing.T) {
	t.Setenv("OPENCODE_NON_INTERACTIVE_TASK_WAIT_TIMEOUT", "2s")
	resetEnvCacheForTest()

	step := Step{ID: "s1"}
	ctx, cancel := stepCtx(context.Background(), step)
	defer cancel()

	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("expected a deadline from env var; got none")
	}
	remaining := time.Until(deadline)
	if remaining > 3*time.Second || remaining < time.Second {
		t.Errorf("expected env deadline ~2s; remaining=%v", remaining)
	}
}

// TestStepCtx_Unbounded: neither set → parent ctx is unwrapped (no deadline).
func TestStepCtx_Unbounded(t *testing.T) {
	t.Setenv("OPENCODE_NON_INTERACTIVE_TASK_WAIT_TIMEOUT", "")
	resetEnvCacheForTest()

	step := Step{ID: "s1"}
	ctx, cancel := stepCtx(context.Background(), step)
	defer cancel()

	if _, ok := ctx.Deadline(); ok {
		t.Error("expected no deadline; got one")
	}
}

// TestStepCtx_BadEnvIgnored: malformed env value falls back to unbounded.
func TestStepCtx_BadEnvIgnored(t *testing.T) {
	t.Setenv("OPENCODE_NON_INTERACTIVE_TASK_WAIT_TIMEOUT", "not-a-duration")
	resetEnvCacheForTest()

	step := Step{ID: "s1"}
	ctx, cancel := stepCtx(context.Background(), step)
	defer cancel()

	if _, ok := ctx.Deadline(); ok {
		t.Error("malformed env should be ignored; expected no deadline, got one")
	}
}

// TestStepCtx_BadStepTimeoutFallsBack: an unparseable Step.Timeout falls
// through to env / parent at runtime (validateFlow rejects it at load time,
// but the runtime is resilient if a Step is built programmatically).
func TestStepCtx_BadStepTimeoutFallsBack(t *testing.T) {
	t.Setenv("OPENCODE_NON_INTERACTIVE_TASK_WAIT_TIMEOUT", "")
	resetEnvCacheForTest()

	step := Step{ID: "s1", Timeout: "bogus"}
	ctx, cancel := stepCtx(context.Background(), step)
	defer cancel()

	if _, ok := ctx.Deadline(); ok {
		t.Error("unparseable timeout should fall through; expected no deadline")
	}
}

// resetEnvCacheForTest forces envTaskWaitTimeout to re-parse on the next
// call by replacing the sync.Once with a fresh zero-value instance.
func resetEnvCacheForTest() {
	envNonInteractiveTaskWaitTimeoutOnce = sync.Once{}
	envNonInteractiveTaskWaitTimeoutVal = 0
}
