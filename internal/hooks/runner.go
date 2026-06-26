package hooks

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"runtime"
	"syscall"
	"time"
)

// DefaultTimeout is the per-hook timeout used when settings.json omits one.
// Matches Claude Code's documented default (600s) so a slow plugin (e.g.
// running a large LLM-call sub-task) doesn't get prematurely killed by a
// fork that picked a tighter cap.
const DefaultTimeout = 600 * time.Second

// Bounded output: stdout 1 MiB, stderr 64 KiB. Excess is silently dropped
// with the cap byte boundary; the runner attaches a Truncated flag so the
// caller can log a WARN. Spec contract — keeps a runaway hook from OOMing
// the agent.
const (
	maxStdoutBytes = 1 << 20 // 1 MiB
	maxStderrBytes = 64 << 10
)

// killGrace is the time we wait after sending SIGTERM before escalating
// to SIGKILL on a timeout. Two seconds matches the spec.
const killGrace = 2 * time.Second

// Result is the per-hook outcome captured by the runner. The registry
// reads ExitCode + Stdout + Stderr to compute decisions, and reads Err
// to log spawn failures and timeouts as non-blocking warnings.
type Result struct {
	ExitCode int
	Stdout   []byte
	Stderr   []byte
	Err      error // non-nil for spawn / timeout / kill failures
	Duration time.Duration

	// Timeout is true if the subprocess was forcibly terminated because
	// it overran its budget. The decision-making code treats this as a
	// non-blocking error regardless of what Stdout contained.
	Timeout bool

	// TruncatedStdout / TruncatedStderr indicate the captured bytes hit
	// the byte cap and were silently terminated mid-stream. Logged at
	// WARN; the partial bytes are still passed through for inspection.
	TruncatedStdout bool
	TruncatedStderr bool
}

// runHook spawns a single hook entry, writes payload to its stdin,
// captures stdout/stderr (bounded), enforces the timeout, and returns
// the result. Panics inside are recovered into Err so a runner-side
// bug can never crash the agent loop.
func runHook(ctx context.Context, entry HookEntry, payload []byte, projectRoot string) (res Result) {
	start := time.Now()
	defer func() {
		res.Duration = time.Since(start)
		if r := recover(); r != nil {
			res.Err = errFromRecover(r)
		}
	}()

	timeout := DefaultTimeout
	if entry.Timeout > 0 {
		timeout = time.Duration(entry.Timeout) * time.Second
	}

	// Two spawn forms per spec D5 and Claude Code parity:
	//   args present → exec form (no shell, args are literal argv)
	//   args absent  → shell form (sh -c "<command>", standard tokenization)
	var cmd *exec.Cmd
	if len(entry.Args) > 0 {
		cmd = exec.Command(entry.Command, entry.Args...)
	} else {
		shell := pickShell(entry.Shell)
		cmd = exec.Command(shell, "-c", entry.Command)
	}

	// Env: inherit + OPENCODE_PROJECT_DIR + CLAUDE_PROJECT_DIR. The Claude
	// alias is the key piece of drop-in compat — RTK reads it without any
	// plugin-side change. We don't strip the parent env; hooks see the
	// same shell they'd see from a terminal.
	cmd.Env = append(cmd.Environ(),
		"OPENCODE_PROJECT_DIR="+projectRoot,
		"CLAUDE_PROJECT_DIR="+projectRoot,
	)
	cmd.Dir = projectRoot

	stdin := bytes.NewReader(payload)
	cmd.Stdin = stdin
	stdout := &cappedBuffer{cap: maxStdoutBytes}
	stderr := &cappedBuffer{cap: maxStderrBytes}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	// Process group so we can kill the shell's children on timeout.
	// `sh -c "sleep 10"` forks sleep; killing the shell alone leaves
	// sleep orphaned and the runner hangs to its end. Setpgid makes the
	// shell the leader; we then signal the negative pid (i.e. the whole
	// group) on timeout.
	setProcessGroup(cmd)

	if err := cmd.Start(); err != nil {
		res.Err = err
		return
	}

	// Wait with a deadline. On overrun we SIGTERM, then escalate to
	// SIGKILL after killGrace. The subprocess might still ignore SIGTERM
	// (eg uninterruptible syscall), so SIGKILL is the hard cutoff.
	//
	// Use NewTimer (not time.After) so a hook that completes well under
	// its timeout doesn't leak a runtime-internal timer until the full
	// deadline elapses. Per-hook savings are small but compound over
	// long sessions firing thousands of hooks.
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case err := <-done:
		res.ExitCode = exitCodeOf(err, cmd)
		if err != nil && !isExitError(err) {
			// e.g. a panic inside cmd.Wait wiring, or stdin pipe error.
			res.Err = err
		}
	case <-timer.C:
		res.Timeout = true
		terminateGroup(cmd, syscall.SIGTERM)
		grace := time.NewTimer(killGrace)
		select {
		case <-done:
			grace.Stop()
		case <-grace.C:
			terminateGroup(cmd, syscall.SIGKILL)
			<-done
		}
		res.ExitCode = -1
		res.Err = errTimeout
	case <-ctx.Done():
		// Parent ctx cancellation: kill aggressively, return promptly.
		terminateGroup(cmd, syscall.SIGKILL)
		<-done
		res.ExitCode = -1
		res.Err = ctx.Err()
	}

	res.Stdout = stdout.Bytes()
	res.TruncatedStdout = stdout.truncated
	res.Stderr = stderr.Bytes()
	res.TruncatedStderr = stderr.truncated
	return
}

// pickShell chooses the shell binary for shell-form invocations.
// Honors the entry's Shell field if set; otherwise prefers bash on POSIX
// (Linux/macOS), falling back to sh. On Windows we still emit "sh" — the
// runner is documented POSIX-only and Windows users get a spawn error
// they can read.
func pickShell(override string) string {
	if override != "" {
		return override
	}
	if runtime.GOOS == "windows" {
		return "sh"
	}
	if _, err := exec.LookPath("bash"); err == nil {
		return "bash"
	}
	return "sh"
}

// cappedBuffer is a tiny io.Writer that drops bytes past `cap` and sets
// a flag. Avoids allocating an unbounded slice when a hook misbehaves.
type cappedBuffer struct {
	buf       bytes.Buffer
	cap       int
	truncated bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	remaining := b.cap - b.buf.Len()
	if remaining <= 0 {
		b.truncated = true
		return len(p), nil
	}
	if len(p) > remaining {
		b.buf.Write(p[:remaining])
		b.truncated = true
		return len(p), nil
	}
	return b.buf.Write(p)
}

func (b *cappedBuffer) Bytes() []byte { return b.buf.Bytes() }

var errTimeout = errors.New("hook timed out")

func errFromRecover(r any) error {
	if e, ok := r.(error); ok {
		return e
	}
	return errors.New("hook runner panic")
}

func exitCodeOf(err error, cmd *exec.Cmd) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	if cmd.ProcessState != nil {
		return cmd.ProcessState.ExitCode()
	}
	return -1
}

func isExitError(err error) bool {
	var ee *exec.ExitError
	return errors.As(err, &ee)
}
