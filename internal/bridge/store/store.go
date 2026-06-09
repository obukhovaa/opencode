// Package store is the bridge's persistence layer. It multiplexes between
// opencode's two sqlc-generated query packages (db for SQLite, mysqldb for
// MySQL) and exposes a single domain-shaped Store interface to the rest of
// the bridge.
//
// Why a separate sub-package: internal/config imports internal/bridge for the
// bridge.Config type used under .opencode.json's "router" key, and
// internal/db transitively imports internal/config. Putting persistence in a
// sub-package keeps the top-level bridge package's import graph clean — only
// internal/bridge/store and the orchestrator package import internal/db.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/db"
	mysqldb "github.com/opencode-ai/opencode/internal/db/mysql"
)

// Binding is the bridge-domain projection of a bridge_sessions row.
type Binding struct {
	ProjectID         string
	Channel           string
	IdentityID        string
	PeerID            string
	SessionID         string // empty when NULL (orphaned binding)
	MentionHandle     string
	MentionConsumedAt int64 // 0 when NULL
	CreatedAt         int64
	UpdatedAt         int64
}

// Peer reduces a Binding to just its identity coordinates. Convenient for
// lookups and HTTP request bodies.
type Peer struct {
	Channel    string
	IdentityID string
	PeerID     string
}

// AllowlistEntry is the bridge-domain projection of a bridge_allowlist row.
type AllowlistEntry struct {
	ProjectID  string
	Channel    string
	IdentityID string
	PeerID     string
	CreatedAt  int64
}

// ErrNotFound is returned by Store.GetBinding (and similar one-row reads)
// when no row matches the requested key.
var ErrNotFound = errors.New("bridge: binding not found")

// Store is the bridge-side persistence interface. Bridge code (orchestrator,
// HTTP handlers, agent tool) calls into this rather than directly into
// db.Queries / mysqldb.Queries.
//
// The Store does NOT own the connection — callers pass a context but the
// underlying *sql.DB is held by the implementation. Callers MUST treat
// returned Binding values as snapshots; they reflect the row at the time of
// read, not live state.
type Store interface {
	// UpsertBinding creates or updates a bridge_sessions row. The
	// (project_id, channel, identity_id, peer_id) tuple acts as the PK.
	// If a row exists with a different session_id it is replaced;
	// mention_consumed_at is reset to NULL on upsert so the next outbound
	// uses the mention prefix again (per the chat-bridge-router-initiated
	// spec "Re-bind resets mention semantics" scenario).
	UpsertBinding(ctx context.Context, b Binding) (Binding, error)

	// GetBinding fetches the binding for the given peer key. Returns
	// ErrNotFound when the row does not exist.
	GetBinding(ctx context.Context, projectID, channel, identityID, peerID string) (Binding, error)

	// ListBindingsBySession returns all bindings pointing at the given
	// session. The result drives outbound fan-out — each entry is a
	// destination the orchestrator dispatches in parallel.
	ListBindingsBySession(ctx context.Context, projectID, sessionID string) ([]Binding, error)

	// ListBindingsByIdentity returns all bindings for a single identity.
	// Used by adapter status reporting (boundSessions count) and by
	// identity DELETE for cascade cleanup.
	ListBindingsByIdentity(ctx context.Context, projectID, channel, identityID string) ([]Binding, error)

	// UpdateBindingPeerID rewrites a binding's peer_id (e.g. Slack
	// channel→thread mutation after the first outbound captures the ts).
	// Caller passes the current key in oldPeerID and the new value in
	// newPeerID. If the row does not exist this is a no-op.
	UpdateBindingPeerID(ctx context.Context, projectID, channel, identityID, oldPeerID, newPeerID string) error

	// UpdateBindingSessionID rewrites a binding's session_id (e.g. when
	// the prior session was garbage-collected and the next inbound
	// allocates a fresh session).
	UpdateBindingSessionID(ctx context.Context, projectID, channel, identityID, peerID, sessionID string) error

	// MarkMentionConsumed records that the first-message mention prefix
	// has been delivered to this binding. Subsequent outbound for this
	// binding will not include the prefix.
	MarkMentionConsumed(ctx context.Context, projectID, channel, identityID, peerID string) error

	// DeleteBindingByPeer removes a single binding.
	DeleteBindingByPeer(ctx context.Context, projectID, channel, identityID, peerID string) error

	// DeleteBindingsBySession removes all bindings pointing at the given
	// session. Used by Unbind without explicit peer list.
	DeleteBindingsBySession(ctx context.Context, projectID, sessionID string) error

	// DeleteBindingsByIdentity removes all bindings tied to the given
	// identity. Used by identity DELETE cascade cleanup.
	DeleteBindingsByIdentity(ctx context.Context, projectID, channel, identityID string) error

	// CountBindingsByIdentity reports the number of active bindings for
	// a single identity (drives the boundSessions field on
	// /router/health).
	CountBindingsByIdentity(ctx context.Context, projectID, channel, identityID string) (int, error)

	// AddAllowlistEntry inserts a (project_id, channel, identity_id,
	// peer_id) tuple into the allowlist. Idempotent.
	AddAllowlistEntry(ctx context.Context, projectID, channel, identityID, peerID string) error

	// IsAllowlisted reports whether the given peer is on the allowlist
	// for the given identity.
	IsAllowlisted(ctx context.Context, projectID, channel, identityID, peerID string) (bool, error)

	// ListAllowlist returns all peers on the allowlist for a single
	// identity.
	ListAllowlist(ctx context.Context, projectID, channel, identityID string) ([]AllowlistEntry, error)

	// RemoveAllowlistEntry removes a single peer from the allowlist.
	RemoveAllowlistEntry(ctx context.Context, projectID, channel, identityID, peerID string) error
}

// New constructs a Store appropriate for the running session provider. The
// caller passes an already-connected *sql.DB; the Store does NOT own it and
// does NOT close it.
func New(database *sql.DB, providerType config.ProviderType) Store {
	if providerType == config.ProviderMySQL {
		return &mysqlStore{queries: mysqldb.New(database), db: database}
	}
	return &sqliteStore{queries: db.New(database), db: database}
}

// AsPeerRef converts a Binding to the chat-bridge peer reference used in
// adapter calls and outbound fan-out. The Mention field carries the binding's
// mention_handle (empty when NULL) so the orchestrator can decide whether to
// prepend a first-message prefix.
func (b Binding) AsPeerRef() bridge.PeerRef {
	return bridge.PeerRef{
		Channel:  b.Channel,
		Identity: b.IdentityID,
		PeerID:   b.PeerID,
		Mention:  b.MentionHandle,
	}
}

// nullString collapses an empty string to sql.NullString{Valid: false}.
// Bridge bindings allow NULL session_id (FK ON DELETE SET NULL) and
// mention_handle, so the bridge-domain Binding's empty-string convention
// translates to NULL at the sqlc layer.
func nullString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

// strFromNullString is the inverse of nullString.
func strFromNullString(n sql.NullString) string {
	if n.Valid {
		return n.String
	}
	return ""
}

// nullInt64 collapses a zero int64 to sql.NullInt64{Valid: false}.
// mention_consumed_at is the only nullable timestamp on bridge_sessions;
// 0 in the domain Binding means "not yet consumed".
func nullInt64(n int64) sql.NullInt64 {
	if n == 0 {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: n, Valid: true}
}

// intFromNullInt64 is the inverse.
func intFromNullInt64(n sql.NullInt64) int64 {
	if n.Valid {
		return n.Int64
	}
	return 0
}

// errWithContext annotates a database error with the call-site information so
// the caller's error message points at the binding key that triggered it.
func errWithContext(op, key string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("bridge.store %s (%s): %w", op, key, err)
}

func bindingKey(projectID, channel, identityID, peerID string) string {
	return fmt.Sprintf("%s|%s|%s|%s", projectID, channel, identityID, peerID)
}
