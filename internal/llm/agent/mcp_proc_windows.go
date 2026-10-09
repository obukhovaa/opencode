//go:build windows

package agent

import (
	"errors"
	"os/exec"
	"syscall"
)

// errNoData is ERROR_NO_DATA ("The pipe is being closed"), what a write to a
// pipe whose reader has exited fails with on Windows.
const errNoData = syscall.Errno(232)

// detachMCPProcess is a no-op on Windows, which has no POSIX sessions or
// process groups; only the server process itself can be stopped, so the
// children of a wrapper (`npx.cmd`) outlive a kill.
func detachMCPProcess(_ *exec.Cmd) {}

// signalMCPProcessGroup kills the server process: Windows cannot deliver
// SIGTERM, so both escalation steps end it.
func signalMCPProcessGroup(cmd *exec.Cmd, _ syscall.Signal) {
	_ = cmd.Process.Kill()
}

// isBrokenPipe reports a write to a pipe whose reader has exited. Windows
// Errno never matches syscall.EPIPE.
func isBrokenPipe(err error) bool {
	return errors.Is(err, syscall.ERROR_BROKEN_PIPE) || errors.Is(err, errNoData)
}
