//go:build !windows

package hooks

import (
	"os/exec"
	"syscall"
)

// setProcessGroup makes the spawned process a process-group leader so a
// shell-form invocation (`sh -c "sleep 10"`) can be terminated along
// with all its children by signalling the negative pid. Without this,
// killing the shell alone leaves orphans that delay the runner's return.
func setProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// terminateGroup signals the process group rooted at cmd.Process.Pid.
// Falls back to signalling the process directly if the group call fails
// (e.g. the process already exited and reaping happened first).
func terminateGroup(cmd *exec.Cmd, sig syscall.Signal) {
	if cmd.Process == nil {
		return
	}
	if err := syscall.Kill(-cmd.Process.Pid, sig); err != nil {
		_ = cmd.Process.Signal(sig)
	}
}
