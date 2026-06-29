//go:build !windows

package task

import (
	"os"
	"os/exec"
	"syscall"
)

// SetProcessGroupAttr makes the spawned process its own process-group leader
// so SignalProcessGroup can later target the whole descendant tree. Spawn
// paths for KindBash/KindMonitor MUST call this BEFORE cmd.Start. On Windows
// this is a no-op (no POSIX process groups; group-targeted termination is
// not supported in v1).
func SetProcessGroupAttr(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// SignalProcessGroup sends sig to the process group led by proc (-proc.Pid).
// Spawn paths must have set Setpgid:true via SetProcessGroupAttr; otherwise
// proc is in the parent's group and signalling the group would also hit the
// parent (catastrophic for opencode itself). Falls back to a leaf-only
// signal if the group call fails (e.g. the group was reaped first).
func SignalProcessGroup(proc *os.Process, sig os.Signal) {
	if proc == nil {
		return
	}
	if sysSig, ok := sig.(syscall.Signal); ok {
		if err := syscall.Kill(-proc.Pid, sysSig); err == nil {
			return
		}
	}
	_ = proc.Signal(sig)
}

// TerminateSignal returns the platform's terminate signal. On POSIX this is
// SIGTERM; on Windows there is no real signal equivalent (Process.Signal
// rejects most signals) and os.Interrupt is the closest analogue.
func TerminateSignal() os.Signal { return syscall.SIGTERM }

// KillSignal returns the platform's force-kill signal. On POSIX this is
// SIGKILL; on Windows this is os.Kill (TerminateProcess equivalent).
func KillSignal() os.Signal { return syscall.SIGKILL }

// terminateSignal is the internal alias used by registry.Kill.
func terminateSignal() os.Signal { return TerminateSignal() }
