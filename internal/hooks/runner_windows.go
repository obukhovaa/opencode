//go:build windows

package hooks

import (
	"os/exec"
	"syscall"
)

// setProcessGroup is a no-op on Windows. The runner is documented
// POSIX-only in v1; this stub exists so the package compiles cross-
// platform but offers no process-group containment for the few
// scenarios that might still try to run hooks on Windows.
func setProcessGroup(_ *exec.Cmd) {}

// terminateGroup falls back to a direct Process.Signal on Windows since
// there is no equivalent of POSIX process groups in the same shape.
func terminateGroup(cmd *exec.Cmd, sig syscall.Signal) {
	if cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(sig)
}
