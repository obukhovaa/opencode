//go:build !windows

package main

import "syscall"

// sessionIsolated reports whether pid is in a different POSIX session than this
// process. A different session means no shared controlling terminal, which is
// the invariant the whole isolation change exists to establish — and unlike a
// /dev/tty probe it is answerable whether or not a terminal is present.
func sessionIsolated(pid int) bool {
	if pid <= 0 {
		return false
	}
	ours, err := syscall.Getsid(0)
	if err != nil {
		return false
	}
	theirs, err := syscall.Getsid(pid)
	if err != nil {
		return false
	}
	return ours != theirs
}
