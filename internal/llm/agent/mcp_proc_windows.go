//go:build windows

package agent

import (
	"os/exec"
	"syscall"
)

// setMCPProcessGroup is a no-op on Windows, which has no POSIX process
// groups; only the server process itself can be stopped.
func setMCPProcessGroup(_ *exec.Cmd) {}

// signalMCPProcessGroup kills the server process: Windows cannot deliver
// SIGTERM, so both escalation steps end it.
func signalMCPProcessGroup(cmd *exec.Cmd, _ syscall.Signal) {
	_ = cmd.Process.Kill()
}
