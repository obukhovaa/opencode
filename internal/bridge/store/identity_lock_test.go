package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/config"
)

func TestSQLiteLockManager(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	mgr, err := NewIdentityLockManager(config.ProviderSQLite, dir, nil)
	if err != nil {
		t.Fatalf("NewIdentityLockManager: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Close() })

	ctx := context.Background()
	h1, err := mgr.Lock(ctx, "proj", "slack", "default")
	if err != nil {
		t.Fatalf("Lock 1: %v", err)
	}
	t.Cleanup(func() { _ = h1.Release() })

	// SQLite mode shares one file lock across all identities — a second
	// identity from the SAME manager should succeed (the file lock is
	// already held).
	h2, err := mgr.Lock(ctx, "proj", "telegram", "default")
	if err != nil {
		t.Fatalf("Lock 2 same process: %v", err)
	}
	_ = h2.Release()

	// A second SQLite lock manager pointed at the same directory must
	// fail with ErrIdentityLocked (the file lock is taken).
	mgr2, err := NewIdentityLockManager(config.ProviderSQLite, dir, nil)
	if err != nil {
		t.Fatalf("NewIdentityLockManager 2: %v", err)
	}
	_, err = mgr2.Lock(ctx, "proj", "slack", "default")
	if !errors.Is(err, ErrIdentityLocked) {
		t.Errorf("second manager Lock = %v, want ErrIdentityLocked", err)
	}

	// After the first manager releases, the second must be able to take it.
	if err := mgr.Close(); err != nil {
		t.Fatalf("Close mgr1: %v", err)
	}
	h, err := mgr2.Lock(ctx, "proj", "slack", "default")
	if err != nil {
		t.Fatalf("Lock after first manager close: %v", err)
	}
	_ = h.Release()
	_ = mgr2.Close()
}

func TestSQLiteLockManagerNoDataDir(t *testing.T) {
	t.Parallel()
	_, err := NewIdentityLockManager(config.ProviderSQLite, "", nil)
	if err == nil {
		t.Errorf("expected error when dataDir is empty")
	}
}

func TestSQLiteHandleStatusReportsHeld(t *testing.T) {
	t.Parallel()
	mgr, err := NewIdentityLockManager(config.ProviderSQLite, t.TempDir(), nil)
	if err != nil {
		t.Fatalf("NewIdentityLockManager: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Close() })

	h, err := mgr.Lock(context.Background(), "proj", "slack", "default")
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	if held, _ := h.Status(); !held {
		t.Errorf("Status held=false right after Lock; want true")
	}
	_ = h.Release()
	if held, _ := h.Status(); held {
		t.Errorf("Status held=true after Release; want false")
	}
}

func TestMySQLLockNameShape(t *testing.T) {
	t.Parallel()
	name := MySQLLockName("github.com/foo/bar", "slack", "default")
	if !strings.HasPrefix(name, "opencode_bridge:") {
		t.Errorf("prefix wrong: %s", name)
	}
	if len(name) != 16+40 {
		t.Errorf("len = %d, want 56 (16 prefix + 40 sha1 hex)", len(name))
	}
	// Different identity must yield a different name (basic determinism +
	// distinctness check).
	other := MySQLLockName("github.com/foo/bar", "slack", "secondary")
	if name == other {
		t.Errorf("different identityID produced same lock name")
	}
	// Different project_id must yield a different name (per the spec
	// scenario: two opencode deployments against the same MySQL with
	// different working directories MUST not collide).
	otherProj := MySQLLockName("github.com/baz/qux", "slack", "default")
	if name == otherProj {
		t.Errorf("different project_id produced same lock name")
	}
}

func TestMySQLLockNameStability(t *testing.T) {
	t.Parallel()
	got := MySQLLockName("proj", "slack", "default")
	// Pin one known case so refactors don't accidentally change the hash
	// function. SHA1 of "proj:slack:default" = the suffix below.
	want := "opencode_bridge:" + "7fb828fe457915375bf2b4d9d2750456f28eca62" // sha1("proj:slack:default")
	if got != want {
		// Recompute and fail with diagnostic info — refactors that
		// intentionally change the hash should update this test.
		t.Errorf("name = %q\nwant %q (refactor must update this pin if hash function changes)", got, want)
	}
}

func TestNewIdentityLockManagerRequiresMySQLDB(t *testing.T) {
	t.Parallel()
	_, err := NewIdentityLockManager(config.ProviderMySQL, "/tmp", nil)
	if err == nil {
		t.Errorf("expected error when MySQL provider has nil *sql.DB")
	}
}
