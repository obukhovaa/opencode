//go:build windows

package task

import (
	"os"
	"os/exec"
)

// SetProcessGroupAttr is a no-op on Windows. The background-task subsystem
// is documented as POSIX-first in v1; this stub exists so the package
// compiles cross-platform but offers no process-group containment for the
// few scenarios that might still try to run background tasks on Windows.
func SetProcessGroupAttr(_ *exec.Cmd) {}

// SignalProcessGroup falls back to a direct Process.Signal on Windows since
// there is no equivalent of POSIX process groups in the same shape.
// Windows-spawned children of a background bash/monitor will outlive a
// taskstop until they finish on their own.
func SignalProcessGroup(proc *os.Process, sig os.Signal) {
	if proc == nil {
		return
	}
	_ = proc.Signal(sig)
}

// TerminateSignal returns os.Interrupt on Windows — the closest analogue to
// a graceful POSIX SIGTERM that Process.Signal will accept.
func TerminateSignal() os.Signal { return os.Interrupt }

// KillSignal returns os.Kill on Windows (TerminateProcess equivalent).
func KillSignal() os.Signal { return os.Kill }

// terminateSignal is the internal alias used by registry.Kill.
func terminateSignal() os.Signal { return TerminateSignal() }
