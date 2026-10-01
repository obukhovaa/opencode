package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/opencode-ai/opencode/internal/db"
	mysqldb "github.com/opencode-ai/opencode/internal/db/mysql"
	"github.com/opencode-ai/opencode/internal/heartbeat"
)

// Heartbeat is the bridge-domain projection of a bridge_heartbeats row: a
// binding key, its heartbeat settings and bookkeeping, and when it was last
// sent the setup reminder.
type Heartbeat struct {
	ProjectID  string
	Channel    string
	IdentityID string
	PeerID     string
	heartbeat.Record
	RemindedAt time.Time
}

// Peer returns the heartbeat's binding key.
func (h Heartbeat) Peer() Peer {
	return Peer{Channel: h.Channel, IdentityID: h.IdentityID, PeerID: h.PeerID}
}

// heartbeatColumns is the column-level form shared by both dialects.
type heartbeatColumns struct {
	state                  string
	everySeconds           sql.NullInt64
	windowStart, windowEnd sql.NullInt64
	weekdaysOnly           bool
	model, agendaFile      sql.NullString
	nextBeatAt, lastBeatAt sql.NullInt64
	lastStatus, lastError  sql.NullString
	remindedAt             sql.NullInt64
}

func (h Heartbeat) columns() heartbeatColumns {
	c := heartbeatColumns{
		state:        string(h.State),
		weekdaysOnly: h.WeekdaysOnly,
		model:        nullString(h.Model),
		agendaFile:   nullString(h.AgendaFile),
		nextBeatAt:   nullInt64(unixMilli(h.NextBeatAt)),
		lastBeatAt:   nullInt64(unixMilli(h.LastBeatAt)),
		lastStatus:   nullString(h.LastStatus),
		lastError:    nullString(h.LastError),
		remindedAt:   nullInt64(unixMilli(h.RemindedAt)),
	}
	if c.state == "" {
		c.state = string(heartbeat.StateUnset)
	}
	if h.Every > 0 {
		c.everySeconds = sql.NullInt64{Int64: int64(h.Every / time.Second), Valid: true}
	}
	if h.Window != nil {
		c.windowStart = sql.NullInt64{Int64: int64(h.Window.Start), Valid: true}
		c.windowEnd = sql.NullInt64{Int64: int64(h.Window.End), Valid: true}
	}
	return c
}

func (c heartbeatColumns) heartbeat(projectID, channel, identityID, peerID string) Heartbeat {
	h := Heartbeat{
		ProjectID:  projectID,
		Channel:    channel,
		IdentityID: identityID,
		PeerID:     peerID,
		RemindedAt: fromUnixMilli(c.remindedAt),
	}
	h.State = heartbeat.State(c.state)
	if c.everySeconds.Valid {
		h.Every = time.Duration(c.everySeconds.Int64) * time.Second
	}
	if c.windowStart.Valid && c.windowEnd.Valid {
		h.Window = &heartbeat.Window{Start: int(c.windowStart.Int64), End: int(c.windowEnd.Int64)}
	}
	h.WeekdaysOnly = c.weekdaysOnly
	h.Model = strFromNullString(c.model)
	h.AgendaFile = strFromNullString(c.agendaFile)
	h.NextBeatAt = fromUnixMilli(c.nextBeatAt)
	h.LastBeatAt = fromUnixMilli(c.lastBeatAt)
	h.LastStatus = strFromNullString(c.lastStatus)
	h.LastError = strFromNullString(c.lastError)
	return h
}

func unixMilli(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func fromUnixMilli(n sql.NullInt64) time.Time {
	if !n.Valid || n.Int64 == 0 {
		return time.Time{}
	}
	return time.UnixMilli(n.Int64).UTC()
}

// --- SQLite ---------------------------------------------------------------

func heartbeatFromSQLite(r db.BridgeHeartbeat) Heartbeat {
	return heartbeatColumns{
		state:        r.State,
		everySeconds: r.EverySeconds,
		windowStart:  r.WindowStart,
		windowEnd:    r.WindowEnd,
		weekdaysOnly: r.WeekdaysOnly != 0,
		model:        r.Model,
		agendaFile:   r.AgendaFile,
		nextBeatAt:   r.NextBeatAt,
		lastBeatAt:   r.LastBeatAt,
		lastStatus:   r.LastStatus,
		lastError:    r.LastError,
		remindedAt:   r.RemindedAt,
	}.heartbeat(r.ProjectID, r.Channel, r.IdentityID, r.PeerID)
}

func (s *sqliteStore) GetHeartbeat(ctx context.Context, projectID, channel, identityID, peerID string) (Heartbeat, error) {
	row, err := s.queries.GetBridgeHeartbeat(ctx, db.GetBridgeHeartbeatParams{
		ProjectID:  projectID,
		Channel:    channel,
		IdentityID: identityID,
		PeerID:     peerID,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Heartbeat{}, ErrNotFound
		}
		return Heartbeat{}, errWithContext("GetHeartbeat", bindingKey(projectID, channel, identityID, peerID), err)
	}
	return heartbeatFromSQLite(row), nil
}

func (s *sqliteStore) PutHeartbeat(ctx context.Context, h Heartbeat) error {
	c := h.columns()
	weekdays := int64(0)
	if c.weekdaysOnly {
		weekdays = 1
	}
	err := s.queries.UpsertBridgeHeartbeat(ctx, db.UpsertBridgeHeartbeatParams{
		ProjectID:    h.ProjectID,
		Channel:      h.Channel,
		IdentityID:   h.IdentityID,
		PeerID:       h.PeerID,
		State:        c.state,
		EverySeconds: c.everySeconds,
		WindowStart:  c.windowStart,
		WindowEnd:    c.windowEnd,
		WeekdaysOnly: weekdays,
		Model:        c.model,
		AgendaFile:   c.agendaFile,
		NextBeatAt:   c.nextBeatAt,
		LastBeatAt:   c.lastBeatAt,
		LastStatus:   c.lastStatus,
		LastError:    c.lastError,
		RemindedAt:   c.remindedAt,
		UpdatedAt:    time.Now().UnixMilli(),
	})
	return errWithContext("PutHeartbeat", bindingKey(h.ProjectID, h.Channel, h.IdentityID, h.PeerID), err)
}

func (s *sqliteStore) ListHeartbeats(ctx context.Context, projectID string) ([]Heartbeat, error) {
	rows, err := s.queries.ListBridgeHeartbeats(ctx, projectID)
	if err != nil {
		return nil, errWithContext("ListHeartbeats", projectID, err)
	}
	out := make([]Heartbeat, len(rows))
	for i := range rows {
		out[i] = heartbeatFromSQLite(rows[i])
	}
	return out, nil
}

// --- MySQL ----------------------------------------------------------------

func nullInt32(n sql.NullInt64) sql.NullInt32 {
	return sql.NullInt32{Int32: int32(n.Int64), Valid: n.Valid}
}

func widen(n sql.NullInt32) sql.NullInt64 {
	return sql.NullInt64{Int64: int64(n.Int32), Valid: n.Valid}
}

func heartbeatFromMySQL(r mysqldb.BridgeHeartbeat) Heartbeat {
	return heartbeatColumns{
		state:        r.State,
		everySeconds: widen(r.EverySeconds),
		windowStart:  widen(r.WindowStart),
		windowEnd:    widen(r.WindowEnd),
		weekdaysOnly: r.WeekdaysOnly,
		model:        r.Model,
		agendaFile:   r.AgendaFile,
		nextBeatAt:   r.NextBeatAt,
		lastBeatAt:   r.LastBeatAt,
		lastStatus:   r.LastStatus,
		lastError:    r.LastError,
		remindedAt:   r.RemindedAt,
	}.heartbeat(r.ProjectID, r.Channel, r.IdentityID, r.PeerID)
}

func (s *mysqlStore) GetHeartbeat(ctx context.Context, projectID, channel, identityID, peerID string) (Heartbeat, error) {
	row, err := s.queries.GetBridgeHeartbeat(ctx, mysqldb.GetBridgeHeartbeatParams{
		ProjectID:  projectID,
		Channel:    channel,
		IdentityID: identityID,
		PeerID:     peerID,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Heartbeat{}, ErrNotFound
		}
		return Heartbeat{}, errWithContext("GetHeartbeat", bindingKey(projectID, channel, identityID, peerID), err)
	}
	return heartbeatFromMySQL(row), nil
}

func (s *mysqlStore) PutHeartbeat(ctx context.Context, h Heartbeat) error {
	c := h.columns()
	err := s.queries.UpsertBridgeHeartbeat(ctx, mysqldb.UpsertBridgeHeartbeatParams{
		ProjectID:    h.ProjectID,
		Channel:      h.Channel,
		IdentityID:   h.IdentityID,
		PeerID:       h.PeerID,
		State:        c.state,
		EverySeconds: nullInt32(c.everySeconds),
		WindowStart:  nullInt32(c.windowStart),
		WindowEnd:    nullInt32(c.windowEnd),
		WeekdaysOnly: c.weekdaysOnly,
		Model:        c.model,
		AgendaFile:   c.agendaFile,
		NextBeatAt:   c.nextBeatAt,
		LastBeatAt:   c.lastBeatAt,
		LastStatus:   c.lastStatus,
		LastError:    c.lastError,
		RemindedAt:   c.remindedAt,
		UpdatedAt:    time.Now().UnixMilli(),
	})
	return errWithContext("PutHeartbeat", bindingKey(h.ProjectID, h.Channel, h.IdentityID, h.PeerID), err)
}

func (s *mysqlStore) ListHeartbeats(ctx context.Context, projectID string) ([]Heartbeat, error) {
	rows, err := s.queries.ListBridgeHeartbeats(ctx, projectID)
	if err != nil {
		return nil, errWithContext("ListHeartbeats", projectID, err)
	}
	out := make([]Heartbeat, len(rows))
	for i := range rows {
		out[i] = heartbeatFromMySQL(rows[i])
	}
	return out, nil
}
