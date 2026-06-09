package store

import (
	"context"
	"database/sql"
	"errors"

	mysqldb "github.com/opencode-ai/opencode/internal/db/mysql"
)

// mysqlStore implements Store against opencode's MySQL-generated queries.
//
// Two shape differences from sqliteStore that the MySQL impl handles inline:
//
//   - Upsert returns sql.Result (not the inserted row), so we follow with a
//     GET round-trip to materialize the same Binding the SQLite path returns.
//   - IsBridgeAllowlisted is generated to return int64 for both providers in
//     this codebase, but we coerce to bool here for the Store contract.
type mysqlStore struct {
	queries *mysqldb.Queries
	db      *sql.DB
}

func (s *mysqlStore) UpsertBinding(ctx context.Context, b Binding) (Binding, error) {
	_, err := s.queries.UpsertBridgeSession(ctx, mysqldb.UpsertBridgeSessionParams{
		ProjectID:     b.ProjectID,
		Channel:       b.Channel,
		IdentityID:    b.IdentityID,
		PeerID:        b.PeerID,
		SessionID:     nullString(b.SessionID),
		MentionHandle: nullString(b.MentionHandle),
	})
	key := bindingKey(b.ProjectID, b.Channel, b.IdentityID, b.PeerID)
	if err != nil {
		return Binding{}, errWithContext("UpsertBinding", key, err)
	}
	row, err := s.queries.GetBridgeSession(ctx, mysqldb.GetBridgeSessionParams{
		ProjectID:  b.ProjectID,
		Channel:    b.Channel,
		IdentityID: b.IdentityID,
		PeerID:     b.PeerID,
	})
	if err != nil {
		return Binding{}, errWithContext("UpsertBinding/Get", key, err)
	}
	return bindingFromMySQL(row), nil
}

func (s *mysqlStore) GetBinding(ctx context.Context, projectID, channel, identityID, peerID string) (Binding, error) {
	row, err := s.queries.GetBridgeSession(ctx, mysqldb.GetBridgeSessionParams{
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
	return bindingFromMySQL(row), nil
}

func (s *mysqlStore) ListBindingsBySession(ctx context.Context, projectID, sessionID string) ([]Binding, error) {
	rows, err := s.queries.ListBridgeSessionsBySession(ctx, mysqldb.ListBridgeSessionsBySessionParams{
		ProjectID: projectID,
		SessionID: nullString(sessionID),
	})
	if err != nil {
		return nil, errWithContext("ListBindingsBySession", projectID+"|"+sessionID, err)
	}
	out := make([]Binding, len(rows))
	for i := range rows {
		out[i] = bindingFromMySQL(rows[i])
	}
	return out, nil
}

func (s *mysqlStore) ListBindingsByIdentity(ctx context.Context, projectID, channel, identityID string) ([]Binding, error) {
	rows, err := s.queries.ListBridgeSessionsByIdentity(ctx, mysqldb.ListBridgeSessionsByIdentityParams{
		ProjectID:  projectID,
		Channel:    channel,
		IdentityID: identityID,
	})
	if err != nil {
		return nil, errWithContext("ListBindingsByIdentity", bindingKey(projectID, channel, identityID, "*"), err)
	}
	out := make([]Binding, len(rows))
	for i := range rows {
		out[i] = bindingFromMySQL(rows[i])
	}
	return out, nil
}

func (s *mysqlStore) UpdateBindingPeerID(ctx context.Context, projectID, channel, identityID, oldPeerID, newPeerID string) error {
	err := s.queries.UpdateBridgeSessionPeerID(ctx, mysqldb.UpdateBridgeSessionPeerIDParams{
		PeerID:     newPeerID,
		ProjectID:  projectID,
		Channel:    channel,
		IdentityID: identityID,
		PeerID_2:   oldPeerID,
	})
	return errWithContext("UpdateBindingPeerID", bindingKey(projectID, channel, identityID, oldPeerID), err)
}

func (s *mysqlStore) UpdateBindingSessionID(ctx context.Context, projectID, channel, identityID, peerID, sessionID string) error {
	err := s.queries.UpdateBridgeSessionSessionID(ctx, mysqldb.UpdateBridgeSessionSessionIDParams{
		SessionID:  nullString(sessionID),
		ProjectID:  projectID,
		Channel:    channel,
		IdentityID: identityID,
		PeerID:     peerID,
	})
	return errWithContext("UpdateBindingSessionID", bindingKey(projectID, channel, identityID, peerID), err)
}

func (s *mysqlStore) MarkMentionConsumed(ctx context.Context, projectID, channel, identityID, peerID string) error {
	err := s.queries.MarkBridgeSessionMentionConsumed(ctx, mysqldb.MarkBridgeSessionMentionConsumedParams{
		ProjectID:  projectID,
		Channel:    channel,
		IdentityID: identityID,
		PeerID:     peerID,
	})
	return errWithContext("MarkMentionConsumed", bindingKey(projectID, channel, identityID, peerID), err)
}

func (s *mysqlStore) DeleteBindingByPeer(ctx context.Context, projectID, channel, identityID, peerID string) error {
	err := s.queries.DeleteBridgeSessionByPeer(ctx, mysqldb.DeleteBridgeSessionByPeerParams{
		ProjectID:  projectID,
		Channel:    channel,
		IdentityID: identityID,
		PeerID:     peerID,
	})
	return errWithContext("DeleteBindingByPeer", bindingKey(projectID, channel, identityID, peerID), err)
}

func (s *mysqlStore) DeleteBindingsBySession(ctx context.Context, projectID, sessionID string) error {
	err := s.queries.DeleteBridgeSessionsBySession(ctx, mysqldb.DeleteBridgeSessionsBySessionParams{
		ProjectID: projectID,
		SessionID: nullString(sessionID),
	})
	return errWithContext("DeleteBindingsBySession", projectID+"|"+sessionID, err)
}

func (s *mysqlStore) DeleteBindingsByIdentity(ctx context.Context, projectID, channel, identityID string) error {
	err := s.queries.DeleteBridgeSessionsByIdentity(ctx, mysqldb.DeleteBridgeSessionsByIdentityParams{
		ProjectID:  projectID,
		Channel:    channel,
		IdentityID: identityID,
	})
	return errWithContext("DeleteBindingsByIdentity", bindingKey(projectID, channel, identityID, "*"), err)
}

func (s *mysqlStore) CountBindingsByIdentity(ctx context.Context, projectID, channel, identityID string) (int, error) {
	n, err := s.queries.CountBridgeSessionsByIdentity(ctx, mysqldb.CountBridgeSessionsByIdentityParams{
		ProjectID:  projectID,
		Channel:    channel,
		IdentityID: identityID,
	})
	if err != nil {
		return 0, errWithContext("CountBindingsByIdentity", bindingKey(projectID, channel, identityID, "*"), err)
	}
	return int(n), nil
}

func (s *mysqlStore) AddAllowlistEntry(ctx context.Context, projectID, channel, identityID, peerID string) error {
	_, err := s.queries.AddBridgeAllowlistEntry(ctx, mysqldb.AddBridgeAllowlistEntryParams{
		ProjectID:  projectID,
		Channel:    channel,
		IdentityID: identityID,
		PeerID:     peerID,
	})
	return errWithContext("AddAllowlistEntry", bindingKey(projectID, channel, identityID, peerID), err)
}

func (s *mysqlStore) IsAllowlisted(ctx context.Context, projectID, channel, identityID, peerID string) (bool, error) {
	v, err := s.queries.IsBridgeAllowlisted(ctx, mysqldb.IsBridgeAllowlistedParams{
		ProjectID:  projectID,
		Channel:    channel,
		IdentityID: identityID,
		PeerID:     peerID,
	})
	if err != nil {
		return false, errWithContext("IsAllowlisted", bindingKey(projectID, channel, identityID, peerID), err)
	}
	return v, nil
}

func (s *mysqlStore) ListAllowlist(ctx context.Context, projectID, channel, identityID string) ([]AllowlistEntry, error) {
	rows, err := s.queries.ListBridgeAllowlist(ctx, mysqldb.ListBridgeAllowlistParams{
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

func (s *mysqlStore) RemoveAllowlistEntry(ctx context.Context, projectID, channel, identityID, peerID string) error {
	err := s.queries.RemoveBridgeAllowlistEntry(ctx, mysqldb.RemoveBridgeAllowlistEntryParams{
		ProjectID:  projectID,
		Channel:    channel,
		IdentityID: identityID,
		PeerID:     peerID,
	})
	return errWithContext("RemoveAllowlistEntry", bindingKey(projectID, channel, identityID, peerID), err)
}

func bindingFromMySQL(r mysqldb.BridgeSession) Binding {
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
