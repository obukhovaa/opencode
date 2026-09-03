//go:build !windows

package contextfile

import "syscall"

// openBeneathExtraFlags hardens OpenBeneath on Unix: O_NOFOLLOW refuses a
// symlink swapped into the final path component, and O_NONBLOCK keeps a
// swapped-in FIFO from blocking the open. Both are no-ops for regular
// files.
const openBeneathExtraFlags = syscall.O_NOFOLLOW | syscall.O_NONBLOCK
