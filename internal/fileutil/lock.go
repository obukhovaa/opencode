package fileutil

import (
	"fmt"
	"os"
)

// Lock represents an exclusive non-blocking OS-level file lock obtained via
// Acquire. Lock.Release() drops the lock and removes the lock file.
//
// The lock is held by the underlying *os.File; closing the file releases
// the lock at the OS level even if the process exits abruptly without
// calling Release. The lock file itself remains on disk after release
// (callers can choose to remove it if desired) — orphan files do not
// block re-acquisition because the lock is on the open file descriptor,
// not the inode.
//
// Used by the bridge orchestrator to enforce single-writer semantics for
// SQLite deployments (see internal/bridge — only one opencode process per
// Data.Directory may consume chat-platform credentials at a time).
type Lock struct {
	f    *os.File
	path string
}

// Path returns the filesystem path the lock is held on.
func (l *Lock) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

// Acquire attempts to obtain an exclusive non-blocking lock on path. The
// file is created if it does not exist (mode 0o600). If the lock is held
// by another process, Acquire returns an error rather than blocking — the
// caller is expected to handle contention by reporting it (e.g. surface
// in /health as "another opencode instance owns this database").
//
// On success the returned *Lock holds the lock for the lifetime of the
// process or until Release is called. The OS will release the lock if the
// process exits for any reason — there is no need for a panic-handler to
// clean up.
func Acquire(path string) (*Lock, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("fileutil.Acquire: open %s: %w", path, err)
	}
	if err := lockFile(f); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("fileutil.Acquire: lock %s: %w", path, err)
	}
	return &Lock{f: f, path: path}, nil
}

// Release drops the lock and closes the underlying file descriptor. After
// Release the Lock value is no longer usable. Release is safe to call on
// a nil receiver (no-op).
func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	// Best-effort unlock; closing the descriptor also releases the OS lock.
	_ = unlockFile(l.f)
	err := l.f.Close()
	l.f = nil
	return err
}
