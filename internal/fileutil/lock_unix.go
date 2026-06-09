//go:build unix

package fileutil

import (
	"errors"
	"os"
	"syscall"
)

// ErrLocked indicates another process already holds the lock. Acquire
// wraps this with context; callers comparing errors use errors.Is.
var ErrLocked = errors.New("file is locked by another process")

func lockFile(f *os.File) error {
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return ErrLocked
		}
		return err
	}
	return nil
}

func unlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
