package cron

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/opencode-ai/opencode/internal/config"
)

func TestSQLiteLeaderLock_SingleProcessLifecycle(t *testing.T) {
	dir := t.TempDir()
	l, err := NewLeaderLock(config.ProviderSQLite, dir, "p1", nil)
	if err != nil {
		t.Fatalf("NewLeaderLock: %v", err)
	}

	if l.Held() {
		t.Fatalf("Held() before TryAcquire = true, want false")
	}
	if err := l.TryAcquire(context.Background()); err != nil {
		t.Fatalf("TryAcquire #1: %v", err)
	}
	if !l.Held() {
		t.Fatalf("Held() after TryAcquire = false, want true")
	}

	// Idempotent: second TryAcquire on same holder is a no-op.
	if err := l.TryAcquire(context.Background()); err != nil {
		t.Fatalf("TryAcquire #2 (idempotent): %v", err)
	}

	if err := l.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if l.Held() {
		t.Fatalf("Held() after Release = true, want false")
	}
	// Idempotent Release.
	if err := l.Release(); err != nil {
		t.Fatalf("Release #2 (idempotent): %v", err)
	}
}

func TestSQLiteLeaderLock_TwoProcessesContend(t *testing.T) {
	dir := t.TempDir()

	// Two LeaderLock instances pointing at the same file simulate two
	// opencode processes against the same dataDir. The OS file lock is
	// process-scoped — within a single test binary that translates to
	// two separate Lock structs pointing at the same path; the second
	// Acquire from a different *os.File still contends with the first.
	a, err := NewLeaderLock(config.ProviderSQLite, dir, "p1", nil)
	if err != nil {
		t.Fatalf("NewLeaderLock A: %v", err)
	}
	b, err := NewLeaderLock(config.ProviderSQLite, dir, "p1", nil)
	if err != nil {
		t.Fatalf("NewLeaderLock B: %v", err)
	}

	if err := a.TryAcquire(context.Background()); err != nil {
		t.Fatalf("A TryAcquire: %v", err)
	}
	defer func() { _ = a.Release() }()

	err = b.TryAcquire(context.Background())
	if !errors.Is(err, ErrLeaderHeld) {
		t.Fatalf("B TryAcquire = %v, want ErrLeaderHeld", err)
	}
	if b.Held() {
		t.Fatalf("B Held() after contended TryAcquire = true, want false")
	}

	// After A releases, B can acquire.
	if err := a.Release(); err != nil {
		t.Fatalf("A Release: %v", err)
	}
	if err := b.TryAcquire(context.Background()); err != nil {
		t.Fatalf("B TryAcquire after A release: %v", err)
	}
	defer func() { _ = b.Release() }()
	if !b.Held() {
		t.Fatalf("B Held() after acquire = false, want true")
	}
}

func TestNewLeaderLock_MissingDeps(t *testing.T) {
	if _, err := NewLeaderLock(config.ProviderSQLite, "", "p1", nil); err == nil {
		t.Fatalf("NewLeaderLock(SQLite, dataDir=\"\") = nil err, want error")
	}
	if _, err := NewLeaderLock(config.ProviderMySQL, "", "p1", nil); err == nil {
		t.Fatalf("NewLeaderLock(MySQL, db=nil) = nil err, want error")
	}
}

func TestSQLiteLeaderLock_LockFilePath(t *testing.T) {
	dir := t.TempDir()
	l, err := NewLeaderLock(config.ProviderSQLite, dir, "p1", nil)
	if err != nil {
		t.Fatalf("NewLeaderLock: %v", err)
	}
	s, ok := l.(*sqliteLeader)
	if !ok {
		t.Fatalf("expected *sqliteLeader, got %T", l)
	}
	want := filepath.Join(dir, "cron.lock")
	if s.path != want {
		t.Errorf("lock path = %q, want %q", s.path, want)
	}
}

func TestSQLiteLeaderLock_PingMirrorsHeld(t *testing.T) {
	dir := t.TempDir()
	l, err := NewLeaderLock(config.ProviderSQLite, dir, "p1", nil)
	if err != nil {
		t.Fatalf("NewLeaderLock: %v", err)
	}

	// Not held → Ping reports ErrLeaderHeld.
	if err := l.Ping(context.Background()); !errors.Is(err, ErrLeaderHeld) {
		t.Errorf("Ping before acquire = %v, want ErrLeaderHeld", err)
	}

	if err := l.TryAcquire(context.Background()); err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	defer func() { _ = l.Release() }()

	// Held → Ping returns nil (no transient-loss case for SQLite).
	if err := l.Ping(context.Background()); err != nil {
		t.Errorf("Ping while held = %v, want nil", err)
	}
}

func TestSQLiteLeaderLock_TryAcquireHonoursContext(t *testing.T) {
	dir := t.TempDir()
	l, err := NewLeaderLock(config.ProviderSQLite, dir, "p1", nil)
	if err != nil {
		t.Fatalf("NewLeaderLock: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := l.TryAcquire(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("TryAcquire(cancelled ctx) = %v, want context.Canceled", err)
	}
	if l.Held() {
		t.Errorf("Held() after cancelled TryAcquire = true, want false")
	}
}

func TestMySQLLeaderLockName_StableAndScoped(t *testing.T) {
	a := mysqlLeaderLockName("project-a")
	b := mysqlLeaderLockName("project-b")
	if a == b {
		t.Fatalf("lock names collide across projects: %s", a)
	}
	if mysqlLeaderLockName("project-a") != a {
		t.Fatalf("lock name not stable for same project")
	}
	if len(a) > 64 {
		t.Fatalf("lock name %q exceeds MySQL 64-char limit (%d)", a, len(a))
	}
}
