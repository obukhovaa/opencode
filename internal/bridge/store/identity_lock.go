package store

import (
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/fileutil"
)

// IdentityLockManager arbitrates single-writer access to a chat-platform
// identity across multiple opencode processes pointed at the same database.
//
// Without this, two opencode serves against the same SQLite (or the same
// MySQL schema + project_id) would race on chat-platform credentials —
// Telegram's getUpdates offset would be trampled, Mattermost's WebSocket
// would deliver the same event to both, and Slack Socket Mode would behave
// unpredictably. The bridge MUST refuse to start an adapter for an
// identity it cannot lock.
//
// SQLite uses a process-scoped file lock on <Data.Directory>/bridge.lock
// (a single lock for the whole bridge — one local-dev opencode at a time).
// MySQL uses a per-identity GET_LOCK on a dedicated *sql.Conn so that
// distinct identities can be owned by different opencode processes
// concurrently.
type IdentityLockManager interface {
	// Lock attempts to acquire the single-writer lock for the given
	// identity. Returns a Handle on success, ErrIdentityLocked when
	// another process holds the lock, or another error on transport
	// failure.
	Lock(ctx context.Context, projectID, channel, identityID string) (LockHandle, error)

	// Close releases any process-wide resources (e.g. the SQLite file
	// lock). Per-identity locks are released via their LockHandle.
	Close() error
}

// LockHandle is the per-identity lock returned by Lock. Release MUST be
// called when the adapter shuts down or is reconfigured; the OS-/server-
// level lock is otherwise tied to process exit (SQLite) or *sql.Conn
// closure (MySQL) and will not free up promptly.
type LockHandle interface {
	// Release drops the lock. Subsequent calls are no-ops.
	Release() error

	// Status reports whether the lock is currently held and the last
	// error observed (e.g. ping failure for MySQL). Used by /router/health
	// to surface "degraded" identities.
	Status() (held bool, lastErr error)

	// Ping checks whether the underlying lock is still valid (for MySQL,
	// pings the dedicated *sql.Conn and reacquires GET_LOCK on failure).
	// SQLite locks are stable as long as the process is alive — Ping is
	// a no-op.
	Ping(ctx context.Context) error
}

// ErrIdentityLocked indicates another opencode process owns the identity.
// The bridge MUST surface this in /router/health as the disabled reason.
var ErrIdentityLocked = errors.New("identity is locked by another opencode process")

// NewIdentityLockManager constructs the appropriate manager for the
// provider. For SQLite the dataDir argument is required (the file lock
// lives at <dataDir>/bridge.lock). For MySQL the *sql.DB argument is
// required (per-identity GET_LOCK is acquired on a dedicated *sql.Conn).
func NewIdentityLockManager(providerType config.ProviderType, dataDir string, mysqlDB *sql.DB) (IdentityLockManager, error) {
	if providerType == config.ProviderMySQL {
		if mysqlDB == nil {
			return nil, errors.New("bridge.store: mysqlDB is required for MySQL lock manager")
		}
		return &mysqlLockManager{db: mysqlDB, holders: make(map[string]*mysqlLockHandle)}, nil
	}
	if dataDir == "" {
		return nil, errors.New("bridge.store: dataDir is required for SQLite lock manager")
	}
	return &sqliteLockManager{dataDir: dataDir}, nil
}

// --- SQLite implementation ----------------------------------------------

type sqliteLockManager struct {
	dataDir string
	mu      sync.Mutex
	lock    *fileutil.Lock
}

func (m *sqliteLockManager) Lock(_ context.Context, projectID, channel, identityID string) (LockHandle, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.lock == nil {
		path := filepath.Join(m.dataDir, "bridge.lock")
		lk, err := fileutil.Acquire(path)
		if err != nil {
			if errors.Is(err, fileutil.ErrLocked) {
				return nil, ErrIdentityLocked
			}
			return nil, fmt.Errorf("bridge.store.sqlite: acquire bridge.lock: %w", err)
		}
		m.lock = lk
	}

	// The SQLite implementation is process-scoped: every identity owned
	// by this opencode shares the one bridge.lock. The handle is a thin
	// reference that does NOT release the file lock when the per-identity
	// handle is Released — the file lock is freed only when the
	// IdentityLockManager itself is Closed.
	return &sqliteLockHandle{key: identityLockKey(projectID, channel, identityID)}, nil
}

func (m *sqliteLockManager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lock == nil {
		return nil
	}
	err := m.lock.Release()
	m.lock = nil
	return err
}

type sqliteLockHandle struct {
	key      string
	released atomic.Bool
}

func (h *sqliteLockHandle) Release() error             { h.released.Store(true); return nil }
func (h *sqliteLockHandle) Status() (bool, error)      { return !h.released.Load(), nil }
func (h *sqliteLockHandle) Ping(context.Context) error { return nil }

// --- MySQL implementation -----------------------------------------------

type mysqlLockManager struct {
	db      *sql.DB
	mu      sync.Mutex
	holders map[string]*mysqlLockHandle
}

func (m *mysqlLockManager) Lock(ctx context.Context, projectID, channel, identityID string) (LockHandle, error) {
	key := identityLockKey(projectID, channel, identityID)
	name := MySQLLockName(projectID, channel, identityID)

	m.mu.Lock()
	if existing, ok := m.holders[key]; ok && !existing.released.Load() {
		m.mu.Unlock()
		return nil, fmt.Errorf("bridge.store.mysql: identity %s already locked by this process", key)
	}
	m.mu.Unlock()

	conn, err := m.db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("bridge.store.mysql: get dedicated conn: %w", err)
	}

	var got sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, 0)", name).Scan(&got); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("bridge.store.mysql: GET_LOCK %s: %w", name, err)
	}
	if !got.Valid || got.Int64 != 1 {
		_ = conn.Close()
		return nil, ErrIdentityLocked
	}

	h := &mysqlLockHandle{
		mgr:  m,
		key:  key,
		name: name,
		conn: conn,
	}

	m.mu.Lock()
	m.holders[key] = h
	m.mu.Unlock()
	return h, nil
}

func (m *mysqlLockManager) Close() error {
	m.mu.Lock()
	holders := m.holders
	m.holders = make(map[string]*mysqlLockHandle)
	m.mu.Unlock()

	var firstErr error
	for _, h := range holders {
		if err := h.Release(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

type mysqlLockHandle struct {
	mgr      *mysqlLockManager
	key      string
	name     string
	mu       sync.Mutex
	conn     *sql.Conn
	lastErr  atomic.Value // stores error
	released atomic.Bool
}

func (h *mysqlLockHandle) Release() error {
	if !h.released.CompareAndSwap(false, true) {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()

	var err error
	if h.conn != nil {
		// RELEASE_LOCK is best-effort: even if it fails (connection
		// already dead), closing the *sql.Conn frees the GET_LOCK
		// because the server-side lock is per-connection.
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, _ = h.conn.ExecContext(ctx, "DO RELEASE_LOCK(?)", h.name)
		cancel()
		err = h.conn.Close()
		h.conn = nil
	}
	h.mgr.mu.Lock()
	delete(h.mgr.holders, h.key)
	h.mgr.mu.Unlock()
	return err
}

func (h *mysqlLockHandle) Status() (bool, error) {
	if h.released.Load() {
		return false, nil
	}
	v := h.lastErr.Load()
	if v == nil {
		return true, nil
	}
	return true, v.(error)
}

// Ping verifies the dedicated *sql.Conn still owns the GET_LOCK. On a
// connection drop (network blip, MySQL restart), Ping attempts to obtain a
// new conn and re-acquire the lock. While unreacquired the handle reports
// the failure via Status (so /router/health can mark the adapter
// "degraded"). Callers should invoke Ping periodically (e.g. every 30s
// from the adapter loop).
func (h *mysqlLockHandle) Ping(ctx context.Context) error {
	if h.released.Load() {
		return errors.New("bridge.store.mysql: lock already released")
	}
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.conn != nil {
		if err := h.conn.PingContext(ctx); err == nil {
			h.lastErr.Store(error(nil))
			return nil
		}
		_ = h.conn.Close()
		h.conn = nil
	}

	// Reconnect + reacquire.
	conn, err := h.mgr.db.Conn(ctx)
	if err != nil {
		h.lastErr.Store(err)
		return fmt.Errorf("bridge.store.mysql: reconnect: %w", err)
	}
	var got sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, 0)", h.name).Scan(&got); err != nil {
		_ = conn.Close()
		h.lastErr.Store(err)
		return fmt.Errorf("bridge.store.mysql: reacquire GET_LOCK %s: %w", h.name, err)
	}
	if !got.Valid || got.Int64 != 1 {
		_ = conn.Close()
		h.lastErr.Store(ErrIdentityLocked)
		return ErrIdentityLocked
	}
	h.conn = conn
	h.lastErr.Store(error(nil))
	return nil
}

// MySQLLockName returns the GET_LOCK name for an identity. Exported for
// testing — the name is constructed as
//
//	"opencode_bridge:" + sha1_hex(project_id + ":" + channel + ":" + identity_id)
//
// Length is 16 + 40 = 56 characters, comfortably under MySQL 8's 64-char
// limit. project_id is included so two opencode deployments against the
// same MySQL server in different schemas do not collide on the
// server-wide (case-insensitive) lock namespace.
func MySQLLockName(projectID, channel, identityID string) string {
	h := sha1.Sum([]byte(projectID + ":" + channel + ":" + identityID))
	return "opencode_bridge:" + hex.EncodeToString(h[:])
}

func identityLockKey(projectID, channel, identityID string) string {
	return projectID + "|" + channel + "|" + identityID
}
