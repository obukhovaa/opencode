//go:build !windows

package agent

import (
	"errors"
	"os/exec"
	"syscall"
)

// detachMCPProcess starts a stdio MCP server in a session of its own. That
// makes it a process-group leader, so a server launched through a wrapper
// (`npx`, `uvx`, a shell script) can be signalled together with the children
// that do the real work. Setsid rather than just Setpgid: with no controlling
// terminal, a server that tries to prompt on /dev/tty (ssh, git credentials)
// fails at once instead of being stopped by SIGTTIN as a background job and
// hanging its call. The terminal's own signals (Ctrl-C, hangup) no longer
// reach it; opencode stops it on shutdown instead.
func detachMCPProcess(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
}

// signalMCPProcessGroup signals the group rooted at cmd's process, falling
// back to the process alone if the group call fails.
func signalMCPProcessGroup(cmd *exec.Cmd, sig syscall.Signal) {
	if err := syscall.Kill(-cmd.Process.Pid, sig); err != nil {
		_ = cmd.Process.Signal(sig)
	}
}

// isBrokenPipe reports a write to a pipe whose reader has exited.
func isBrokenPipe(err error) bool {
	return errors.Is(err, syscall.EPIPE)
}
