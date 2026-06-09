package fileutil

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAcquireAndRelease(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.lock")

	lk, err := Acquire(path)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if got := lk.Path(); got != path {
		t.Errorf("Path() = %q, want %q", got, path)
	}
	if err := lk.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}

	// After release, re-acquire MUST succeed in the same process.
	lk2, err := Acquire(path)
	if err != nil {
		t.Fatalf("second acquire after release: %v", err)
	}
	_ = lk2.Release()
}

func TestSecondAcquireFails(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "contended.lock")

	first, err := Acquire(path)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	defer first.Release()

	// Same process, same file. The OS lock is per-descriptor — opening a
	// second descriptor and trying to flock(LOCK_EX|LOCK_NB) it must fail.
	second, err := Acquire(path)
	if err == nil {
		_ = second.Release()
		t.Fatalf("second Acquire succeeded; expected lock contention")
	}
	if !errors.Is(err, ErrLocked) {
		t.Errorf("err = %v, want wrapped ErrLocked", err)
	}
}

func TestReleaseNilLockIsNoop(t *testing.T) {
	t.Parallel()
	var lk *Lock
	if err := lk.Release(); err != nil {
		t.Errorf("Release on nil Lock = %v, want nil", err)
	}
}

func TestLockFileCreatedWithSafeMode(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "modecheck.lock")

	lk, err := Acquire(path)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer lk.Release()

	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	mode := st.Mode().Perm()
	// On Unix the umask will narrow the mode further; on Windows the test
	// is moot because windows ACLs don't map to POSIX perm bits. We just
	// assert the file is not world-writable — narrower modes (e.g. 0o600
	// when umask is 0o022) are fine.
	if mode&0o002 != 0 {
		t.Errorf("mode %v is world-writable; expected lock file to be created with 0o600 source mode", mode)
	}
}
