package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/opencode-ai/opencode/internal/db"
)

// sqliteStore implements Store against opencode's SQLite-generated queries.
type sqliteStore struct {
	queries *db.Queries
	db      *sql.DB
}

func (s *sqliteStore) UpsertBinding(ctx context.Context, b Binding) (Binding, error) {
	row, err := s.queries.UpsertBridgeSession(ctx, db.UpsertBridgeSessionParams{
		ProjectID:     b.ProjectID,
		Channel:       b.Channel,
		IdentityID:    b.IdentityID,
		PeerID:        b.PeerID,
		SessionID:     nullString(b.SessionID),
		MentionHandle: nullString(b.MentionHandle),
	})
	if err != nil {
		return Binding{}, errWithContext("UpsertBinding", bindingKey(b.ProjectID, b.Channel, b.IdentityID, b.PeerID), err)
	}
	return bindingFromSQLite(row), nil
}

func (s *sqliteStore) GetBinding(ctx context.Context, projectID, channel, identityID, peerID string) (Binding, error) {
	row, err := s.queries.GetBridgeSession(ctx, db.GetBridgeSessionParams{
		ProjectID:  projectID,
		Channel:    channel,
		IdentityID: identityID,
		PeerID:     peerID,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Binding{}, ErrNotFound
		}
		return Binding{}, errWithContext("GetBinding", bindingKey(projectID, channel, identityID, peerID), err)
	}
	return bindingFromSQLite(row), nil
}

func (s *sqliteStore) ListBindingsBySession(ctx context.Context, projectID, sessionID string) ([]Binding, error) {
	rows, err := s.queries.ListBridgeSessionsBySession(ctx, db.ListBridgeSessionsBySessionParams{
		ProjectID: projectID,
		SessionID: nullString(sessionID),
	})
	if err != nil {
		return nil, errWithContext("ListBindingsBySession", projectID+"|"+sessionID, err)
	}
	out := make([]Binding, len(rows))
	for i := range rows {
		out[i] = bindingFromSQLite(rows[i])
	}
	return out, nil
}

func (s *sqliteStore) ListBindingsByIdentity(ctx context.Context, projectID, channel, identityID string) ([]Binding, error) {
	rows, err := s.queries.ListBridgeSessionsByIdentity(ctx, db.ListBridgeSessionsByIdentityParams{
		ProjectID:  projectID,
		Channel:    channel,
		IdentityID: identityID,
	})
	if err != nil {
		return nil, errWithContext("ListBindingsByIdentity", bindingKey(projectID, channel, identityID, "*"), err)
	}
	out := make([]Binding, len(rows))
	for i := range rows {
		out[i] = bindingFromSQLite(rows[i])
	}
	return out, nil
}

func (s *sqliteStore) UpdateBindingPeerID(ctx context.Context, projectID, channel, identityID, oldPeerID, newPeerID string) error {
	err := s.queries.UpdateBridgeSessionPeerID(ctx, db.UpdateBridgeSessionPeerIDParams{
		PeerID:     newPeerID,
		ProjectID:  projectID,
		Channel:    channel,
		IdentityID: identityID,
		PeerID_2:   oldPeerID,
	})
	return errWithContext("UpdateBindingPeerID", bindingKey(projectID, channel, identityID, oldPeerID), err)
}

func (s *sqliteStore) UpdateBindingSessionID(ctx context.Context, projectID, channel, identityID, peerID, sessionID string) error {
	err := s.queries.UpdateBridgeSessionSessionID(ctx, db.UpdateBridgeSessionSessionIDParams{
		SessionID:  nullString(sessionID),
		ProjectID:  projectID,
		Channel:    channel,
		IdentityID: identityID,
		PeerID:     peerID,
	})
	return errWithContext("UpdateBindingSessionID", bindingKey(projectID, channel, identityID, peerID), err)
}

func (s *sqliteStore) MarkMentionConsumed(ctx context.Context, projectID, channel, identityID, peerID string) error {
	err := s.queries.MarkBridgeSessionMentionConsumed(ctx, db.MarkBridgeSessionMentionConsumedParams{
		ProjectID:  projectID,
		Channel:    channel,
		IdentityID: identityID,
		PeerID:     peerID,
	})
	return errWithContext("MarkMentionConsumed", bindingKey(projectID, channel, identityID, peerID), err)
}

func (s *sqliteStore) DeleteBindingByPeer(ctx context.Context, projectID, channel, identityID, peerID string) error {
	err := s.queries.DeleteBridgeSessionByPeer(ctx, db.DeleteBridgeSessionByPeerParams{
		ProjectID:  projectID,
		Channel:    channel,
		IdentityID: identityID,
		PeerID:     peerID,
	})
	return errWithContext("DeleteBindingByPeer", bindingKey(projectID, channel, identityID, peerID), err)
}

func (s *sqliteStore) DeleteBindingsBySession(ctx context.Context, projectID, sessionID string) error {
	err := s.queries.DeleteBridgeSessionsBySession(ctx, db.DeleteBridgeSessionsBySessionParams{
		ProjectID: projectID,
		SessionID: nullString(sessionID),
	})
	return errWithContext("DeleteBindingsBySession", projectID+"|"+sessionID, err)
}

func (s *sqliteStore) DeleteBindingsByIdentity(ctx context.Context, projectID, channel, identityID string) error {
	err := s.queries.DeleteBridgeSessionsByIdentity(ctx, db.DeleteBridgeSessionsByIdentityParams{
		ProjectID:  projectID,
		Channel:    channel,
		IdentityID: identityID,
	})
	return errWithContext("DeleteBindingsByIdentity", bindingKey(projectID, channel, identityID, "*"), err)
}

func (s *sqliteStore) CountBindingsByIdentity(ctx context.Context, projectID, channel, identityID string) (int, error) {
	n, err := s.queries.CountBridgeSessionsByIdentity(ctx, db.CountBridgeSessionsByIdentityParams{
		ProjectID:  projectID,
		Channel:    channel,
		IdentityID: identityID,
	})
	if err != nil {
		return 0, errWithContext("CountBindingsByIdentity", bindingKey(projectID, channel, identityID, "*"), err)
	}
	return int(n), nil
}

func (s *sqliteStore) AddAllowlistEntry(ctx context.Context, projectID, channel, identityID, peerID string) error {
	err := s.queries.AddBridgeAllowlistEntry(ctx, db.AddBridgeAllowlistEntryParams{
		ProjectID:  projectID,
		Channel:    channel,
		IdentityID: identityID,
		PeerID:     peerID,
	})
	return errWithContext("AddAllowlistEntry", bindingKey(projectID, channel, identityID, peerID), err)
}

func (s *sqliteStore) IsAllowlisted(ctx context.Context, projectID, channel, identityID, peerID string) (bool, error) {
	v, err := s.queries.IsBridgeAllowlisted(ctx, db.IsBridgeAllowlistedParams{
		ProjectID:  projectID,
		Channel:    channel,
		IdentityID: identityID,
		PeerID:     peerID,
	})
	if err != nil {
		return false, errWithContext("IsAllowlisted", bindingKey(projectID, channel, identityID, peerID), err)
	}
	return v != 0, nil
}

func (s *sqliteStore) ListAllowlist(ctx context.Context, projectID, channel, identityID string) ([]AllowlistEntry, error) {
	rows, err := s.queries.ListBridgeAllowlist(ctx, db.ListBridgeAllowlistParams{
		ProjectID:  projectID,
		Channel:    channel,
		IdentityID: identityID,
	})
	if err != nil {
		return nil, errWithContext("ListAllowlist", bindingKey(projectID, channel, identityID, "*"), err)
	}
	out := make([]AllowlistEntry, len(rows))
	for i, r := range rows {
		out[i] = AllowlistEntry{
			ProjectID:  r.ProjectID,
			Channel:    r.Channel,
			IdentityID: r.IdentityID,
			PeerID:     r.PeerID,
			CreatedAt:  r.CreatedAt,
		}
	}
	return out, nil
}

func (s *sqliteStore) RemoveAllowlistEntry(ctx context.Context, projectID, channel, identityID, peerID string) error {
	err := s.queries.RemoveBridgeAllowlistEntry(ctx, db.RemoveBridgeAllowlistEntryParams{
		ProjectID:  projectID,
		Channel:    channel,
		IdentityID: identityID,
		PeerID:     peerID,
	})
	return errWithContext("RemoveAllowlistEntry", bindingKey(projectID, channel, identityID, peerID), err)
}

func bindingFromSQLite(r db.BridgeSession) Binding {
	return Binding{
		ProjectID:         r.ProjectID,
		Channel:           r.Channel,
		IdentityID:        r.IdentityID,
		PeerID:            r.PeerID,
		SessionID:         strFromNullString(r.SessionID),
		MentionHandle:     strFromNullString(r.MentionHandle),
		MentionConsumedAt: intFromNullInt64(r.MentionConsumedAt),
		CreatedAt:         r.CreatedAt,
		UpdatedAt:         r.UpdatedAt,
	}
}
