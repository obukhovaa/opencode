package hooks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestRunner_SuccessWithJSONOutput(t *testing.T) {
	skipNonPosix(t)
	dir := t.TempDir()
	script := writeScript(t, dir, "ok.sh", `#!/bin/sh
cat > /dev/null
echo '{"hookSpecificOutput":{"hookEventName":"PreToolUse","updatedInput":{"command":"echo rewritten"}}}'
exit 0
`)
	res := runHook(context.Background(), HookEntry{Type: "command", Command: script}, []byte(`{}`), dir)
	if res.Err != nil {
		t.Fatalf("unexpected runner error: %v (stderr=%q)", res.Err, res.Stderr)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit = %d, want 0", res.ExitCode)
	}
	if !strings.Contains(string(res.Stdout), "rewritten") {
		t.Errorf("stdout missing rewritten content: %q", res.Stdout)
	}
}

func TestRunner_Exit2WithStderr(t *testing.T) {
	skipNonPosix(t)
	dir := t.TempDir()
	script := writeScript(t, dir, "block.sh", `#!/bin/sh
cat > /dev/null
echo 'Tool is forbidden' >&2
exit 2
`)
	res := runHook(context.Background(), HookEntry{Type: "command", Command: script}, []byte(`{}`), dir)
	if res.ExitCode != 2 {
		t.Fatalf("exit = %d, want 2", res.ExitCode)
	}
	if !strings.Contains(string(res.Stderr), "forbidden") {
		t.Errorf("stderr missing block reason: %q", res.Stderr)
	}
}

func TestRunner_NonZeroNonTwoIsNonBlocking(t *testing.T) {
	skipNonPosix(t)
	dir := t.TempDir()
	script := writeScript(t, dir, "fail.sh", `#!/bin/sh
cat > /dev/null
echo 'transient' >&2
exit 1
`)
	res := runHook(context.Background(), HookEntry{Type: "command", Command: script}, []byte(`{}`), dir)
	if res.ExitCode != 1 {
		t.Fatalf("exit = %d, want 1", res.ExitCode)
	}
	if res.Err != nil {
		// applyExit() treats non-2 non-zero as non-blocking and logs WARN —
		// the runner reports the exit cleanly without surfacing Err for it.
		t.Errorf("runner should not synthesize Err for exit 1; got %v", res.Err)
	}
}

func TestRunner_TimeoutIsKilled(t *testing.T) {
	skipNonPosix(t)
	dir := t.TempDir()
	script := writeScript(t, dir, "slow.sh", `#!/bin/sh
sleep 10
`)
	start := time.Now()
	// 1-second timeout — runner should kill the subprocess by ~3s
	// (timeout + SIGTERM/SIGKILL grace ≈ 2s). Generous bound for
	// CI slowness; if we hit 8s the timeout machinery is broken.
	res := runHook(context.Background(), HookEntry{Type: "command", Command: script, Timeout: 1}, []byte(`{}`), dir)
	elapsed := time.Since(start)
	if elapsed > 8*time.Second {
		t.Fatalf("subprocess not killed within 8s; elapsed=%v", elapsed)
	}
	if !res.Timeout {
		t.Errorf("expected Timeout=true; got false (Err=%v)", res.Err)
	}
	if !errors.Is(res.Err, errTimeout) {
		t.Errorf("expected errors.Is(res.Err, errTimeout); got %v", res.Err)
	}
}

func TestRunner_CommandNotFound(t *testing.T) {
	skipNonPosix(t)
	dir := t.TempDir()
	// args form bypasses the shell, so a non-existent binary surfaces
	// as a spawn-time error in res.Err. (shell form would surface a
	// 127 exit code via /bin/sh; we want the spawn-error path here.)
	res := runHook(context.Background(), HookEntry{
		Type:    "command",
		Command: "/this/binary/does/not/exist",
		Args:    []string{"--noop"},
	}, []byte(`{}`), dir)
	if res.Err == nil {
		t.Fatal("expected spawn error for nonexistent binary; got nil")
	}
}

func TestRunner_ArgsFormBypassesShellExpansion(t *testing.T) {
	skipNonPosix(t)
	dir := t.TempDir()
	// /bin/echo + literal "$HOME" must produce the literal `$HOME` in
	// stdout, not the expanded home directory. Verifies exec-form
	// bypasses tokenization.
	res := runHook(context.Background(), HookEntry{
		Type:    "command",
		Command: "/bin/echo",
		Args:    []string{"$HOME"},
	}, []byte(`{}`), dir)
	if res.Err != nil {
		t.Fatalf("unexpected error: %v", res.Err)
	}
	got := strings.TrimSpace(string(res.Stdout))
	if got != "$HOME" {
		t.Errorf("args form should bypass shell expansion; got %q, want literal $HOME", got)
	}
}

func TestRunner_EnvVarsInjected(t *testing.T) {
	skipNonPosix(t)
	dir := t.TempDir()
	script := writeScript(t, dir, "env.sh", `#!/bin/sh
cat > /dev/null
printf 'OPENCODE=%s\nCLAUDE=%s\n' "$OPENCODE_PROJECT_DIR" "$CLAUDE_PROJECT_DIR"
exit 0
`)
	// projectRoot must exist on disk — the runner sets it as cmd.Dir,
	// and Go's exec refuses to start a process whose Dir is missing.
	projectRoot := dir
	res := runHook(context.Background(), HookEntry{Type: "command", Command: script}, []byte(`{}`), projectRoot)
	if res.Err != nil {
		t.Fatalf("unexpected error: %v (stderr=%q)", res.Err, res.Stderr)
	}
	if !strings.Contains(string(res.Stdout), "OPENCODE="+projectRoot) {
		t.Errorf("OPENCODE_PROJECT_DIR not set; stdout=%q", res.Stdout)
	}
	if !strings.Contains(string(res.Stdout), "CLAUDE="+projectRoot) {
		t.Errorf("CLAUDE_PROJECT_DIR alias not set; stdout=%q", res.Stdout)
	}
}

func TestRunner_PayloadOnStdin(t *testing.T) {
	skipNonPosix(t)
	dir := t.TempDir()
	// Echo stdin verbatim to stdout. Confirms the runner writes the
	// payload then closes stdin so `cat` returns. A runner that fails
	// to close stdin would deadlock and trip the test's 30s default.
	script := writeScript(t, dir, "echo.sh", `#!/bin/sh
cat
`)
	payload := []byte(`{"tool_name":"bash","tool_input":{"command":"ls"}}`)
	res := runHook(context.Background(), HookEntry{Type: "command", Command: script}, payload, dir)
	if res.Err != nil {
		t.Fatalf("unexpected error: %v", res.Err)
	}
	if !strings.Contains(string(res.Stdout), `"tool_input":{"command":"ls"}`) {
		t.Errorf("payload not delivered to hook stdin; stdout=%q", res.Stdout)
	}
}

// --- helpers ------------------------------------------------------------

func skipNonPosix(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("runner is POSIX-only in v1; Windows support is a follow-up")
	}
}

func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	return path
}
