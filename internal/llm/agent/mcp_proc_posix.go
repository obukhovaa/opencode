//go:build !windows

package agent

import (
	"os/exec"
	"syscall"
)

// setMCPProcessGroup makes a stdio MCP server a process-group leader, so a
// server launched through a wrapper (`npx`, `uvx`, a shell script) can be
// signalled together with the children that do the real work.
func setMCPProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// signalMCPProcessGroup signals the group rooted at cmd's process, falling
// back to the process alone if the group call fails.
func signalMCPProcessGroup(cmd *exec.Cmd, sig syscall.Signal) {
	if err := syscall.Kill(-cmd.Process.Pid, sig); err != nil {
		_ = cmd.Process.Signal(sig)
	}
}
