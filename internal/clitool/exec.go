package clitool

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/opencode-ai/opencode/internal/task"
)

// maxCaptureBytes bounds what the executor keeps in memory per stream. It
// matches tools.MaxPersistBytes so a spilled file can hold everything kept.
const maxCaptureBytes = 100 * 1024 * 1024

// ExecResult is the outcome of one process run.
type ExecResult struct {
	Command  string // absolute path of the binary
	Args     []string
	Stdout   string
	Stderr   string
	ExitCode int
	TimedOut bool
	Timeout  time.Duration
	Duration time.Duration
	// StartErr is set when the process could not be started (missing
	// binary, bad cwd); ExitCode is -1 then.
	StartErr error
	// Truncated reports that a stream exceeded maxCaptureBytes.
	Truncated bool
}

// Failed reports whether the run should be returned as an error result.
func (r ExecResult) Failed() bool {
	return r.StartErr != nil || r.TimedOut || r.ExitCode != 0
}

// Exec runs the binary with prefixArgs + inv.Args under the manifest's
// confinement. It never returns a Go error for process outcomes: everything
// the model needs is in ExecResult.
func (m *Manifest) Exec(ctx context.Context, inv *Invocation) ExecResult {
	res := ExecResult{Args: append(append([]string{}, m.PrefixArgs...), inv.Args...), ExitCode: -1}

	path := m.ResolveCommand()
	if path == "" {
		res.StartErr = fmt.Errorf("command %q not found (looked on PATH and relative to %s); the tool is declared in %s but its binary is not installed in this runtime", m.Command, m.WorkingDir, m.Location)
		return res
	}
	res.Command = path

	timeout := m.timeout
	if inv.Timeout > 0 {
		timeout = time.Duration(inv.Timeout) * time.Second
	}
	if timeout > m.maxTimeout {
		timeout = m.maxTimeout
	}
	res.Timeout = timeout

	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, path, res.Args...)
	cmd.Dir = m.ResolvedCwd
	cmd.Env = m.BuildEnv(os.Environ())
	if inv.Stdin != "" {
		cmd.Stdin = strings.NewReader(inv.Stdin)
	} else {
		cmd.Stdin = nil // /dev/null: a CLI that waits for a terminal must not hang
	}
	stdout := &limitedBuffer{limit: maxCaptureBytes}
	stderr := &limitedBuffer{limit: maxCaptureBytes}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	// Own process group so a timeout kills the whole descendant tree, not
	// just the leader (a wrapper script would otherwise orphan its child).
	task.SetProcessGroupAttr(cmd)
	cmd.Cancel = func() error {
		task.SignalProcessGroup(cmd.Process, task.KillSignal())
		return nil
	}
	cmd.WaitDelay = 2 * time.Second

	start := time.Now()
	if err := cmd.Start(); err != nil {
		res.StartErr = fmt.Errorf("start %s: %w", path, err)
		return res
	}
	waitErr := cmd.Wait()
	res.Duration = time.Since(start)
	res.Stdout = stdout.String()
	res.Stderr = stderr.String()
	res.Truncated = stdout.truncated || stderr.truncated

	if errors.Is(runCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
		res.TimedOut = true
	}
	var exitErr *exec.ExitError
	switch {
	case waitErr == nil:
		res.ExitCode = 0
	case errors.As(waitErr, &exitErr):
		res.ExitCode = exitErr.ExitCode()
		if res.ExitCode == -1 && res.TimedOut {
			res.ExitCode = 137 // killed
		}
	default:
		// I/O error around the process, not an exit status.
		res.StartErr = waitErr
	}
	return res
}

// BuildEnv shapes the child environment from the parent one according to
// the manifest's env policy.
func (m *Manifest) BuildEnv(parent []string) []string {
	parentMap := make(map[string]string, len(parent))
	var order []string
	for _, kv := range parent {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if _, seen := parentMap[k]; !seen {
			order = append(order, k)
		}
		parentMap[k] = v
	}
	var out []string
	if m.EnvInherit() {
		for _, k := range order {
			out = append(out, k+"="+parentMap[k])
		}
	} else {
		keep := map[string]bool{"PATH": true, "HOME": true, "TMPDIR": true}
		for _, k := range m.Env.Pass {
			keep[k] = true
		}
		for _, k := range order {
			if keep[k] {
				out = append(out, k+"="+parentMap[k])
			}
		}
	}
	if len(m.Env.Set) > 0 {
		keys := make([]string, 0, len(m.Env.Set))
		for k := range m.Env.Set {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			out = append(out, k+"="+expandEnvTokens(m.Env.Set[k], parentMap))
		}
	}
	return out
}

var envTokenRe = regexp.MustCompile(`\$\{env\.([A-Za-z_][A-Za-z0-9_]*)\}`)

// expandEnvTokens replaces `${env.NAME}` with the parent value (or "").
func expandEnvTokens(s string, parent map[string]string) string {
	return envTokenRe.ReplaceAllStringFunc(s, func(tok string) string {
		name := tok[len("${env.") : len(tok)-1]
		return parent[name]
	})
}

// limitedBuffer keeps at most limit bytes and records overflow.
type limitedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	room := b.limit - b.buf.Len()
	if room <= 0 {
		b.truncated = true
		return len(p), nil
	}
	if len(p) > room {
		b.truncated = true
		b.buf.Write(p[:room])
		return len(p), nil
	}
	return b.buf.Write(p)
}

func (b *limitedBuffer) String() string { return b.buf.String() }

var _ io.Writer = (*limitedBuffer)(nil)

// FormatResult renders an ExecResult the way the model sees it: stdout, a
// labelled stderr block when non-empty, and the exit status line. isError
// says whether the call should be reported as a failed tool call.
func FormatResult(res ExecResult) (text string, isError bool) {
	var sb strings.Builder
	if res.StartErr != nil {
		sb.WriteString(res.StartErr.Error())
		return sb.String(), true
	}
	out := strings.TrimRight(res.Stdout, "\n")
	if out != "" {
		sb.WriteString(out)
		sb.WriteString("\n")
	}
	if errOut := strings.TrimRight(res.Stderr, "\n"); errOut != "" {
		sb.WriteString("--- stderr ---\n")
		sb.WriteString(errOut)
		sb.WriteString("\n")
	}
	if res.Truncated {
		sb.WriteString("(output exceeded the capture limit and was cut)\n")
	}
	switch {
	case res.TimedOut:
		fmt.Fprintf(&sb, "timed out after %s; the process group was killed (exit status %d)", res.Timeout, res.ExitCode)
	case out == "" && strings.TrimSpace(res.Stderr) == "":
		fmt.Fprintf(&sb, "exit status %d (no output)", res.ExitCode)
	default:
		fmt.Fprintf(&sb, "exit status %d", res.ExitCode)
	}
	return sb.String(), res.Failed()
}
