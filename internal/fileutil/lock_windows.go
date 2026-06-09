//go:build windows

package fileutil

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// ErrLocked indicates another process already holds the lock. Acquire
// wraps this with context; callers comparing errors use errors.Is.
var ErrLocked = errors.New("file is locked by another process")

func lockFile(f *os.File) error {
	// LOCKFILE_EXCLUSIVE_LOCK + LOCKFILE_FAIL_IMMEDIATELY is the Windows
	// equivalent of LOCK_EX|LOCK_NB. The lock spans the whole file.
	ol := new(windows.Overlapped)
	err := windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0,
		^uint32(0), ^uint32(0),
		ol,
	)
	if err != nil {
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_IO_PENDING) {
			return ErrLocked
		}
		return err
	}
	return nil
}

func unlockFile(f *os.File) error {
	ol := new(windows.Overlapped)
	return windows.UnlockFileEx(
		windows.Handle(f.Fd()),
		0,
		^uint32(0), ^uint32(0),
		ol,
	)
}
