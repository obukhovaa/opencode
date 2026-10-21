//go:build unix

package memory

import "syscall"

const readFlags = syscall.O_NOFOLLOW | syscall.O_NONBLOCK
