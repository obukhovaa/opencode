//go:build windows

package shell

import (
	"os"
	"os/exec"
)

// detachFromTerminal is a no-op on Windows: there is no POSIX session and no
// /dev/tty, so a child process cannot reach opencode's console the way a POSIX
// child can reach its controlling terminal. The stub exists so the spawn path
// compiles cross-platform.
func detachFromTerminal(_ *exec.Cmd) {}

// exitStatusOf turns a finished process's state into an exit code. Windows has
// no signals, so ExitCode is always meaningful; the negative case can only mean
// the process has not exited, which callers here never ask about.
func exitStatusOf(ps *os.ProcessState) int {
	if ps == nil {
		return 1
	}
	if code := ps.ExitCode(); code >= 0 {
		return code
	}
	return 1
}
