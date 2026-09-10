//go:build !windows

package shell

import (
	"os"
	"os/exec"
	"syscall"
)

// detachFromTerminal starts the persistent shell in its own session, leaving it
// with no controlling terminal. Callers MUST invoke it before cmd.Start.
//
// Setsid, not Setpgid. A controlling terminal is a property of the *session*,
// not of the process group: a child with Setpgid:true still shares opencode's
// session, so /dev/tty still resolves to the terminal opencode renders on.
// Setsid makes the child a session leader with no controlling terminal, so
// open("/dev/tty") fails with ENXIO for it and every descendant.
//
// That is the whole fix for the TUI-corruption bug. Redirecting a command's
// stdin from /dev/null is not enough, because sudo, ssh, gpg and friends
// deliberately fall back to opening /dev/tty *because* stdin is not a terminal.
// Isolation has to be a property of how the shell is spawned, not of how each
// command is invoked — otherwise the guarantee is a list of known tools rather
// than an invariant.
//
// Setsid also makes the shell a process-group leader (pgid == pid). Termination
// deliberately does not use that group: the shell itself is in it and must
// survive to run the next command. See terminateDescendants.
func detachFromTerminal(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
}

// exitStatusOf turns a finished process's state into a shell-style exit code.
//
// os.ProcessState.ExitCode reports -1 when the process was terminated by a
// signal, which is meaningless to surface to a user or an agent ("Exit code
// -1"). Shells report 128+N for signal N, and that is the number a user
// comparing against their own terminal expects to see.
func exitStatusOf(ps *os.ProcessState) int {
	if ps == nil {
		return 1
	}
	if code := ps.ExitCode(); code >= 0 {
		return code
	}
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return 1
}
