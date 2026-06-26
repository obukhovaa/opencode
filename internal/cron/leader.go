package cron

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
	"github.com/opencode-ai/opencode/internal/logging"
)

// ErrLeaderHeld indicates another opencode process owns the cron leader lock.
var ErrLeaderHeld = errors.New("cron leader lock is held by another opencode process")

// LeaderLock arbitrates which opencode process runs the cron scheduler when
// multiple processes share a database. Only the holder claims and fires
// jobs; followers stay dormant and periodically retry to acquire the lock.
//
// Without this, two opencode processes against the same DB both tick at 1Hz
// and race on ClaimForFiring. The loser of the race is fine; the danger is
// that the WINNER may be a process without the right permission resolver
// for the job's session — e.g. a TUI process winning a cron whose session
// is bridge-bound, which then defers the job 60s (no bridge resolver) or
// pops a permission dialog the chat user can never see. Pinning leadership
// to a single process moves the resolver decision from "whichever ticker
// raced fastest" to "whichever process started first".
//
// The lock is best-effort and non-blocking: TryAcquire returns ErrLeaderHeld
// immediately when another process owns it. The holder keeps the lock until
// Release is called (clean shutdown) or the process exits — the OS releases
// the file lock on exit, and MySQL releases GET_LOCK on connection close.
type LeaderLock interface {
	// TryAcquire attempts to acquire the lock without blocking. Returns
	// nil on success, ErrLeaderHeld when another process owns it, or a
	// transport-level error otherwise. Idempotent: re-calling after a
	// successful acquire returns nil immediately. Honours ctx cancellation
	// so a slow disk / DB cannot wedge scheduler Stop().
	TryAcquire(ctx context.Context) error

	// Held reports whether this process currently owns the lock from the
	// scheduler's point of view. For MySQL this is the cached state from
	// the last Ping/TryAcquire — a connection drop between pings is
	// invisible here, so callers MUST run Ping periodically to keep it
	// honest.
	Held() bool

	// Ping verifies the lock is still live and reacquires it on transient
	// failure (relevant for MySQL — the dedicated *sql.Conn can be killed
	// by the server or a network blip, releasing GET_LOCK server-side
	// while Held() still reports true). Returns nil if the lock is held
	// and confirmed; ErrLeaderHeld if the lock was lost and another peer
	// has taken it; a transport error otherwise. After ErrLeaderHeld or a
	// transport error Held() returns false so the scheduler downgrades to
	// follower and retries via the normal retry ticker. Safe to call on
	// SQLite (no-op — file locks die with the process).
	Ping(ctx context.Context) error

	// Release drops the lock. Safe to call when not held (no-op).
	Release() error
}

// NewLeaderLock constructs the appropriate lock for the configured DB
// provider. SQLite uses an OS file lock at <dataDir>/cron.lock; MySQL
// uses a per-project GET_LOCK on a dedicated *sql.Conn. The two share
// no state, so a SQLite deployment and a MySQL deployment against the
// same machine do not collide.
func NewLeaderLock(providerType config.ProviderType, dataDir, projectID string, mysqlDB *sql.DB) (LeaderLock, error) {
	if providerType == config.ProviderMySQL {
		if mysqlDB == nil {
			return nil, errors.New("cron.NewLeaderLock: mysqlDB required for MySQL provider")
		}
		return &mysqlLeader{db: mysqlDB, name: mysqlLeaderLockName(projectID)}, nil
	}
	if dataDir == "" {
		return nil, errors.New("cron.NewLeaderLock: dataDir required for SQLite provider")
	}
	return &sqliteLeader{path: filepath.Join(dataDir, "cron.lock")}, nil
}

// --- SQLite implementation ----------------------------------------------

type sqliteLeader struct {
	path string
	mu   sync.Mutex
	lock *fileutil.Lock
}

func (s *sqliteLeader) TryAcquire(ctx context.Context) error {
	// Check ctx before touching the filesystem so Stop() cancels cleanly
	// on a hung / slow disk. fileutil.Acquire is non-blocking but the
	// underlying os.OpenFile can still hang on NFS or a stalled volume.
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lock != nil {
		return nil
	}
	lk, err := fileutil.Acquire(s.path)
	if err != nil {
		if errors.Is(err, fileutil.ErrLocked) {
			return ErrLeaderHeld
		}
		return fmt.Errorf("cron leader: acquire %s: %w", s.path, err)
	}
	s.lock = lk
	return nil
}

func (s *sqliteLeader) Held() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lock != nil
}

// Ping verifies the lock is still held. The SQLite file lock is held by
// the open file descriptor and only released by Release() or process
// exit — there is no transient-loss case to recover from, so Ping
// always returns nil while the lock is held. The not-held branch is
// unreachable from the scheduler (which gates on Held() first) but
// preserved for interface symmetry: callers that ignore Held() and
// poll Ping directly still get a consistent "lock no longer ours"
// signal.
func (s *sqliteLeader) Ping(_ context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lock == nil {
		return ErrLeaderHeld
	}
	return nil
}

func (s *sqliteLeader) Release() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lock == nil {
		return nil
	}
	err := s.lock.Release()
	s.lock = nil
	return err
}

// --- MySQL implementation -----------------------------------------------

type mysqlLeader struct {
	db     *sql.DB
	name   string
	mu     sync.Mutex
	conn   *sql.Conn
	connID int64 // CONNECTION_ID() captured at TryAcquire; the GET_LOCK holder we expect
	held   atomic.Bool
}

func (m *mysqlLeader) TryAcquire(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.conn != nil {
		return nil
	}
	// All MySQL queries here go through *sql.Conn (NOT *sql.DB). *sql.Conn
	// is pinned to a single underlying server connection for its
	// lifetime: if MySQL kills that conn between two queries the second
	// query surfaces driver.ErrBadConn rather than silently rebinding to
	// a fresh conn. That guarantee is what makes "GET_LOCK then
	// CONNECTION_ID() then later IS_USED_LOCK on the same *sql.Conn"
	// race-free. Refactoring any of these to use *sql.DB.QueryRowContext
	// would silently break the invariant — the GET_LOCK and the
	// CONNECTION_ID could land on different server connections and the
	// later IS_USED_LOCK check would compare against the wrong ID.
	conn, err := m.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("cron leader: get dedicated conn: %w", err)
	}
	var got sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, 0)", m.name).Scan(&got); err != nil {
		_ = conn.Close()
		return fmt.Errorf("cron leader: GET_LOCK %s: %w", m.name, err)
	}
	if !got.Valid || got.Int64 != 1 {
		_ = conn.Close()
		return ErrLeaderHeld
	}
	// Capture the dedicated conn's CONNECTION_ID so Ping can later
	// verify IS_USED_LOCK still points at *this* conn (not another conn
	// from the pool, and not no one). Without this, a server-side conn
	// kill (idle timeout, server restart, KILL CONNECTION) plus a
	// driver-level reconnect would leave us believing we still hold the
	// lock when the server-side GET_LOCK has already been released to
	// a peer.
	var cid sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&cid); err != nil {
		// Release what we just grabbed before bailing — otherwise the
		// next acquire on this DB would see GET_LOCK held with nothing
		// owning it (until the conn returns to the pool and gets reused).
		releaseCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, _ = conn.ExecContext(releaseCtx, "DO RELEASE_LOCK(?)", m.name)
		cancel()
		_ = conn.Close()
		return fmt.Errorf("cron leader: CONNECTION_ID: %w", err)
	}
	if !cid.Valid {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, _ = conn.ExecContext(releaseCtx, "DO RELEASE_LOCK(?)", m.name)
		cancel()
		_ = conn.Close()
		return errors.New("cron leader: CONNECTION_ID returned NULL")
	}
	m.conn = conn
	m.connID = cid.Int64
	m.held.Store(true)
	return nil
}

func (m *mysqlLeader) Held() bool { return m.held.Load() }

// Ping verifies the dedicated *sql.Conn still holds the GET_LOCK. The
// hazard this guards against: MySQL kills the conn (network blip, server
// restart, idle-timeout kill, KILL CONNECTION) → server-side GET_LOCK is
// released → another opencode process acquires it → this process still
// thinks it is leader and keeps claiming jobs from a database where it
// isn't.
//
// We use IS_USED_LOCK + the CONNECTION_ID captured at acquire to verify
// "this exact conn still owns the lock" rather than just "some conn
// answered" (which a transparently-reconnected pool conn would also
// satisfy). IS_USED_LOCK returns the connection_id of the lock holder
// or NULL when unheld: equal to m.connID → we still own it; anything
// else → we lost it.
//
// On any "lost it" outcome we tear down the conn (so the next
// TryAcquire opens a fresh one) and flip Held to false so the scheduler
// downgrades to follower. We do NOT attempt in-place re-acquisition
// here: if the conn died, the lock was released and almost certainly
// grabbed by a peer; trying to grab it back inside Ping would race with
// that peer. Letting the scheduler fall back to the regular
// tryAcquireLeadership keeps the state machine simple.
func (m *mysqlLeader) Ping(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.conn == nil {
		return ErrLeaderHeld
	}

	var holder sql.NullInt64
	err := m.conn.QueryRowContext(ctx, "SELECT IS_USED_LOCK(?)", m.name).Scan(&holder)
	if err != nil {
		// Conn is dead — the most likely cause of a query failure on a
		// previously-working dedicated conn. Log the underlying cause at
		// Debug so operators can tell network blips (use-after-close,
		// EOF) from authentication / server-side issues without log
		// spam; the scheduler will already emit a single Info on the
		// downgrade. Skip RELEASE_LOCK on this path — the conn answered
		// nothing, sending another query on it would block or error
		// again.
		logging.Debug("Cron leader Ping: query failed, downgrading", "error", err)
		m.tearDownLocked(false)
		return ErrLeaderHeld
	}
	if !holder.Valid || holder.Int64 != m.connID {
		// Conn answered but the server-side GET_LOCK is gone (NULL) or
		// held by someone else (different connection_id). On NULL the
		// driver may have transparently reset our session behind the
		// scenes — the ORIGINAL server connection that holds the lock
		// could still be alive and stuck holding it until wait_timeout
		// fires (default 8h). Send a best-effort DO RELEASE_LOCK on
		// this (live) conn so any session-level state the server can
		// still see gets dropped immediately. Harmless when the lock is
		// genuinely gone or owned by another conn — RELEASE_LOCK only
		// affects locks held by the issuing connection.
		logging.Debug("Cron leader Ping: IS_USED_LOCK mismatch, downgrading",
			"want_conn_id", m.connID, "got_holder", holder)
		m.tearDownLocked(true)
		return ErrLeaderHeld
	}
	return nil
}

// tearDownLocked releases the dedicated conn and resets state. Caller
// must hold m.mu. Used on any "lost the lock" outcome — we always close
// the conn rather than reuse it, because we cannot tell from the outside
// whether the server-side lock state is recoverable on this conn.
//
// tryRelease=true issues a best-effort DO RELEASE_LOCK before closing.
// Pass true ONLY when the conn just answered a query successfully (so we
// know it can still accept one more roundtrip without blocking forever);
// pass false on paths where the conn is presumed dead (query error). The
// release call uses context.Background() with a 2s timeout because the
// caller's ctx may already be cancelled by Stop(), but we still want
// best-effort cleanup before the conn is dropped.
func (m *mysqlLeader) tearDownLocked(tryRelease bool) {
	if m.conn != nil {
		if tryRelease {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_, _ = m.conn.ExecContext(ctx, "DO RELEASE_LOCK(?)", m.name)
			cancel()
		}
		_ = m.conn.Close()
		m.conn = nil
	}
	m.connID = 0
	m.held.Store(false)
}

func (m *mysqlLeader) Release() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.conn == nil {
		return nil
	}
	// RELEASE_LOCK is best-effort; closing the *sql.Conn frees the
	// server-side lock unconditionally (GET_LOCK is per-connection).
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	_, _ = m.conn.ExecContext(ctx, "DO RELEASE_LOCK(?)", m.name)
	cancel()
	err := m.conn.Close()
	m.conn = nil
	m.connID = 0
	m.held.Store(false)
	return err
}

// mysqlLeaderLockName returns a per-project GET_LOCK name under 64
// characters. The project ID is hashed so two opencode deployments on
// the same MySQL server in different schemas do not collide on the
// server-wide (case-insensitive) lock namespace.
func mysqlLeaderLockName(projectID string) string {
	h := sha1.Sum([]byte("cron:" + projectID))
	return "opencode_cron:" + hex.EncodeToString(h[:])
}
