//go:build windows

package shell

import "os/exec"

// detachFromTerminal is a no-op on Windows: there is no POSIX session and no
// /dev/tty, so a child process cannot reach opencode's console the way a POSIX
// child can reach its controlling terminal. The stub exists so the spawn path
// compiles cross-platform.
func detachFromTerminal(_ *exec.Cmd) {}
