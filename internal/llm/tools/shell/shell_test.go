package shell

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// newTestShell builds a shell instance directly rather than through
// GetPersistentShell, so tests never share the package-global instance map.
func newTestShell(t *testing.T) *PersistentShell {
	t.Helper()
	sh := newPersistentShell(t.TempDir())
	if sh == nil {
		t.Fatal("failed to start persistent shell")
	}
	t.Cleanup(sh.Close)
	return sh
}

func exec1(t *testing.T, sh *PersistentShell, command string) (stdout, stderr string, exitCode int) {
	t.Helper()
	stdout, stderr, exitCode, _, err := sh.Exec(context.Background(), command, 30_000)
	if err != nil {
		t.Fatalf("Exec(%q) returned error: %v", command, err)
	}
	return stdout, stderr, exitCode
}

// TestShellRunsInItsOwnSession is the direct assertion of the isolation
// invariant: a different session id means no controlling terminal, which is
// what stops a descendant from writing onto the terminal opencode renders on.
// Asserting the session id rather than a failed /dev/tty open makes the test
// meaningful even when the test process itself has no terminal.
func TestShellRunsInItsOwnSession(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX sessions only")
	}
	sh := newTestShell(t)

	ours, err := syscall.Getsid(0)
	if err != nil {
		t.Fatalf("Getsid(self): %v", err)
	}
	theirs, err := syscall.Getsid(sh.cmd.Process.Pid)
	if err != nil {
		t.Fatalf("Getsid(shell): %v", err)
	}
	if theirs == ours {
		t.Fatalf("shell shares our session (%d); it must be detached so /dev/tty cannot resolve", ours)
	}
	if theirs != sh.cmd.Process.Pid {
		t.Errorf("shell session id = %d, want it to be the session leader (%d)", theirs, sh.cmd.Process.Pid)
	}
}

// TestShellCannotOpenControllingTerminal exercises the behavior the session
// isolation buys, but only when there is a terminal to leak in the first place:
// under a CI runner with no tty the open would fail regardless and the test
// would prove nothing.
func TestShellCannotOpenControllingTerminal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no /dev/tty on Windows")
	}
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		t.Skip("test process has no controlling terminal; nothing could leak")
	}
	f.Close()

	sh := newTestShell(t)
	_, _, exitCode := exec1(t, sh, "echo probe > /dev/tty")
	if exitCode == 0 {
		t.Fatal("command wrote to /dev/tty successfully; the shell is still attached to our terminal")
	}
}

// TestShellCapturedContractUnchanged pins the behavior the agent's bash tool
// depends on. Terminal isolation must be invisible to any command that does not
// touch a terminal.
func TestShellCapturedContractUnchanged(t *testing.T) {
	sh := newTestShell(t)

	t.Run("stdout and exit code", func(t *testing.T) {
		stdout, stderr, code := exec1(t, sh, "echo hello")
		if strings.TrimSpace(stdout) != "hello" {
			t.Errorf("stdout = %q, want %q", stdout, "hello")
		}
		if stderr != "" {
			t.Errorf("stderr = %q, want empty", stderr)
		}
		if code != 0 {
			t.Errorf("exit code = %d, want 0", code)
		}
	})

	t.Run("stderr and non-zero exit", func(t *testing.T) {
		// Run in a subshell: commands are eval'd in the persistent shell, so a
		// bare `exit 3` would exit the shell itself rather than report a status.
		_, stderr, code := exec1(t, sh, "sh -c 'echo oops >&2; exit 3'")
		if strings.TrimSpace(stderr) != "oops" {
			t.Errorf("stderr = %q, want %q", stderr, "oops")
		}
		if code != 3 {
			t.Errorf("exit code = %d, want 3", code)
		}
	})

	t.Run("cd persists across commands", func(t *testing.T) {
		dir := t.TempDir()
		// macOS reports /var/... as /private/var/...; resolve so the comparison
		// is against the path the shell will actually print.
		resolved, err := filepath.EvalSymlinks(dir)
		if err != nil {
			t.Fatalf("EvalSymlinks: %v", err)
		}
		if _, _, code := exec1(t, sh, "cd "+resolved); code != 0 {
			t.Fatalf("cd failed with exit code %d", code)
		}
		stdout, _, _ := exec1(t, sh, "pwd")
		if got := strings.TrimSpace(stdout); got != resolved {
			t.Errorf("pwd after cd = %q, want %q", got, resolved)
		}
		if got := sh.Cwd(); got != resolved {
			t.Errorf("Cwd() = %q, want %q", got, resolved)
		}
	})
}

// TestShellEnvironment covers both halves of the prompting policy: the variable
// that disables git's prompts is present, and a variable the policy does not
// name is passed through untouched.
func TestShellEnvironment(t *testing.T) {
	t.Setenv("OPENCODE_SHELL_ENV_PROBE", "carried-through")
	sh := newTestShell(t)

	stdout, _, _ := exec1(t, sh, "echo $GIT_TERMINAL_PROMPT")
	if got := strings.TrimSpace(stdout); got != "0" {
		t.Errorf("GIT_TERMINAL_PROMPT = %q, want %q", got, "0")
	}

	stdout, _, _ = exec1(t, sh, "echo $OPENCODE_SHELL_ENV_PROBE")
	if got := strings.TrimSpace(stdout); got != "carried-through" {
		t.Errorf("caller env var = %q, want it preserved", got)
	}
}

// TestCancelTerminatesGrandchildren is the regression test for the old
// killChildren, which signalled only direct children and left the whole tree
// below them running.
func TestCancelTerminatesGrandchildren(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("pgrep-based process walk is POSIX-only")
	}
	sh := newTestShell(t)

	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		// persistent shell -> sh (child) -> sleep (grandchild)
		_, _, _, _, _ = sh.Exec(ctx, "sh -c 'sleep 60 & echo $! > "+pidFile+"; wait'", 60_000)
	}()

	grandchild := waitForPIDFile(t, pidFile)
	if !processAlive(grandchild) {
		t.Fatalf("grandchild %d was not running before cancel", grandchild)
	}

	cancel()
	<-done

	deadline := time.Now().Add(descendantGracePeriod + 3*time.Second)
	for time.Now().Before(deadline) {
		if !processAlive(grandchild) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	// Do not leak the process if the assertion fails.
	if proc, err := os.FindProcess(grandchild); err == nil {
		_ = proc.Kill()
	}
	t.Fatalf("grandchild %d survived cancellation", grandchild)
}

// TestTerminateDescendantsSpareTheShell guards the reason kill(-pgid) is not
// used: after Setsid the shell leads its own group, so a group signal would
// take it down with its children.
func TestTerminateDescendantsSpareTheShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("pgrep-based process walk is POSIX-only")
	}
	sh := newTestShell(t)
	exec1(t, sh, "true")

	sh.terminateDescendants()

	if !sh.isAlive {
		t.Fatal("terminateDescendants killed the persistent shell")
	}
	stdout, _, code := exec1(t, sh, "echo still-here")
	if code != 0 || strings.TrimSpace(stdout) != "still-here" {
		t.Fatalf("shell unusable after terminateDescendants: stdout=%q code=%d", stdout, code)
	}
}

func waitForPIDFile(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("pid file %s never appeared", path)
	return 0
}

func processAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}
