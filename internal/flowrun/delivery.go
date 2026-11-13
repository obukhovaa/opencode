package flowrun

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/opencode-ai/opencode/internal/db"
)

func (s *Store) lockRun(ctx context.Context, tx *sql.Tx, runID string) error {
	res, err := tx.ExecContext(ctx, "UPDATE conversation_flow_runs SET guard_serial=guard_serial+1 WHERE project_id=? AND id=?", s.projectID, runID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrNotFound
	}
	return nil
}
func validInboxItem(runID string, item Inbox) (string, error) {
	if item.ID == "" || len(item.ID) > 255 || item.RunID != "" && item.RunID != runID {
		return "", fmt.Errorf("invalid inbox identity")
	}
	return canonical(item.Payload)
}
func (s *Store) queueInboxTx(ctx context.Context, tx *sql.Tx, runID, id, encoded string) (Inbox, bool, error) {
	out := Inbox{RunID: runID, ID: id, Payload: json.RawMessage(encoded)}
	var previous string
	err := tx.QueryRowContext(ctx, "SELECT sequence,payload FROM conversation_flow_inbox WHERE run_id=? AND id=?", runID, id).Scan(&out.Sequence, &previous)
	if err == nil {
		if previous != encoded {
			return Inbox{}, false, ErrConflict
		}
		return out, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Inbox{}, false, err
	}
	var status string
	var deadline int64
	if err = tx.QueryRowContext(ctx, "SELECT status,deadline FROM conversation_flow_runs WHERE project_id=? AND id=?", s.projectID, runID).Scan(&status, &deadline); err != nil {
		return Inbox{}, false, err
	}
	now, err := s.clock(ctx, tx)
	if err != nil {
		return Inbox{}, false, err
	}
	if terminal(status) || deadline > 0 && deadline <= now {
		return Inbox{}, false, ErrConflict
	}
	if err = tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(sequence),0)+1 FROM conversation_flow_inbox WHERE run_id=?", runID).Scan(&out.Sequence); err != nil {
		return Inbox{}, false, err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO conversation_flow_inbox(run_id,id,sequence,payload,delivered) VALUES(?,?,?,?,0)", runID, id, out.Sequence, encoded)
	return out, err == nil, err
}
func (s *Store) QueueInbox(ctx context.Context, runID, id string, payload json.RawMessage) (Inbox, bool, error) {
	encoded, err := validInboxItem(runID, Inbox{ID: id, Payload: payload})
	if err != nil {
		return Inbox{}, false, err
	}
	var out Inbox
	var created bool
	err = s.transaction(ctx, func(tx *sql.Tx) error {
		if err := s.lockRun(ctx, tx, runID); err != nil {
			return err
		}
		var err error
		out, created, err = s.queueInboxTx(ctx, tx, runID, id, encoded)
		return err
	})
	return out, created, err
}

// QueueInboxBatch serializes a whole externally identified batch so no other
// input interleaves between its new messages. Any conflict rolls it all back.
func (s *Store) QueueInboxBatch(ctx context.Context, runID string, items []Inbox) ([]Inbox, error) {
	if len(items) == 0 || len(items) > 1000 {
		return nil, fmt.Errorf("inbox batch must contain 1..1000 messages")
	}
	encoded := make([]string, len(items))
	seen := map[string]bool{}
	for i, item := range items {
		var err error
		encoded[i], err = validInboxItem(runID, item)
		if err != nil {
			return nil, err
		}
		if seen[item.ID] {
			return nil, ErrConflict
		}
		seen[item.ID] = true
	}
	var out []Inbox
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		if err := s.lockRun(ctx, tx, runID); err != nil {
			return err
		}
		out = make([]Inbox, 0, len(items))
		for i, item := range items {
			stored, _, err := s.queueInboxTx(ctx, tx, runID, item.ID, encoded[i])
			if err != nil {
				return err
			}
			out = append(out, stored)
		}
		return nil
	})
	return out, err
}

func (s *Store) PeekInbox(ctx context.Context, runID string) ([]Inbox, error) {
	if _, err := s.Get(ctx, runID); err != nil {
		return nil, err
	}
	rows, err := s.conn.QueryContext(ctx, "SELECT id,sequence,payload FROM conversation_flow_inbox WHERE run_id=? AND delivered=0 ORDER BY sequence", runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Inbox{}
	for rows.Next() {
		item := Inbox{RunID: runID}
		var payload string
		if err = rows.Scan(&item.ID, &item.Sequence, &payload); err != nil {
			return nil, err
		}
		item.Payload = json.RawMessage(payload)
		out = append(out, item)
	}
	return out, rows.Err()
}
func (s *Store) ackInboxTx(ctx context.Context, tx *sql.Tx, l Lease, ids []string) error {
	for _, id := range ids {
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM conversation_flow_inbox WHERE run_id=? AND id=?", l.RunID, id).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			return ErrNotFound
		}
		if _, err := tx.ExecContext(ctx, "UPDATE conversation_flow_inbox SET delivered=1 WHERE run_id=? AND id=?", l.RunID, id); err != nil {
			return err
		}
	}
	return nil
}
func (s *Store) AckInbox(ctx context.Context, l Lease, ids []string) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		if err := s.fence(ctx, tx, l); err != nil {
			return err
		}
		return s.ackInboxTx(ctx, tx, l, ids)
	})
}
func (s *Store) appendEventTx(ctx context.Context, tx *sql.Tx, l Lease, id, kind string, payload json.RawMessage) (Event, error) {
	if id == "" || len(id) > 255 || kind == "" || len(kind) > 64 {
		return Event{}, fmt.Errorf("invalid event identity")
	}
	encoded, err := canonical(payload)
	if err != nil {
		return Event{}, err
	}
	out := Event{ID: id, RunID: l.RunID, Kind: kind, Payload: json.RawMessage(encoded)}
	var priorKind, priorPayload string
	err = tx.QueryRowContext(ctx, "SELECT sequence,kind,payload FROM conversation_flow_events WHERE run_id=? AND id=?", l.RunID, id).Scan(&out.Sequence, &priorKind, &priorPayload)
	if err == nil {
		if priorKind != kind || priorPayload != encoded {
			return Event{}, ErrConflict
		}
		return out, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Event{}, err
	}
	if err = tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(sequence),0)+1 FROM conversation_flow_events WHERE run_id=?", l.RunID).Scan(&out.Sequence); err != nil {
		return Event{}, err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO conversation_flow_events(run_id,id,sequence,kind,payload) VALUES(?,?,?,?,?)", l.RunID, id, out.Sequence, kind, encoded)
	return out, err
}
func (s *Store) AppendEvent(ctx context.Context, l Lease, id, kind string, payload json.RawMessage) (Event, error) {
	var out Event
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		if err := s.fence(ctx, tx, l); err != nil {
			return err
		}
		var err error
		out, err = s.appendEventTx(ctx, tx, l, id, kind, payload)
		return err
	})
	return out, err
}

// AckInboxEvent makes input delivery and its replayable receipt indivisible.
func (s *Store) AckInboxEvent(ctx context.Context, l Lease, ids []string, id, kind string, payload json.RawMessage, mutations ...func(db.QuerierWithTx) error) (Event, error) {
	var out Event
	encoded, err := canonical(payload)
	if err != nil {
		return out, err
	}
	err = s.transaction(ctx, func(tx *sql.Tx) error {
		if err := s.fence(ctx, tx, l); err != nil {
			return err
		}
		var priorKind, priorPayload string
		var sequence int64
		err := tx.QueryRowContext(ctx, "SELECT sequence,kind,payload FROM conversation_flow_events WHERE run_id=? AND id=?", l.RunID, id).Scan(&sequence, &priorKind, &priorPayload)
		if err == nil {
			if priorKind != kind || priorPayload != encoded {
				return ErrConflict
			}
			out = Event{ID: id, RunID: l.RunID, Kind: kind, Sequence: sequence, Payload: json.RawMessage(encoded)}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if len(mutations) > 0 {
			var status string
			if err = tx.QueryRowContext(ctx, "SELECT status FROM conversation_flow_runs WHERE project_id=? AND id=?", s.projectID, l.RunID).Scan(&status); err != nil {
				return err
			}
			if terminal(status) {
				return ErrLeaseLost
			}
		}
		for _, mutate := range mutations {
			if mutate != nil {
				if err = mutate(s.queries.WithTx(tx)); err != nil {
					return err
				}
			}
		}
		if err = s.ackInboxTx(ctx, tx, l, ids); err != nil {
			return err
		}
		out, err = s.appendEventTx(ctx, tx, l, id, kind, payload)
		return err
	})
	return out, err
}

func (s *Store) Events(ctx context.Context, runID string, after int64, limit int) ([]Event, error) {
	if _, err := s.Get(ctx, runID); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := s.conn.QueryContext(ctx, "SELECT id,sequence,kind,payload FROM conversation_flow_events WHERE run_id=? AND sequence>? ORDER BY sequence LIMIT ?", runID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		item := Event{RunID: runID}
		var payload string
		if err = rows.Scan(&item.ID, &item.Sequence, &item.Kind, &payload); err != nil {
			return nil, err
		}
		item.Payload = json.RawMessage(payload)
		out = append(out, item)
	}
	return out, rows.Err()
}
func (s *Store) AckEvent(ctx context.Context, l Lease, eventID, recipient string) error {
	if recipient == "" || len(recipient) > 255 {
		return fmt.Errorf("invalid event recipient")
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		if err := s.fence(ctx, tx, l); err != nil {
			return err
		}
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM conversation_flow_events WHERE run_id=? AND id=?", l.RunID, eventID).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			return ErrNotFound
		}
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM conversation_flow_receipts WHERE run_id=? AND event_id=? AND recipient=?", l.RunID, eventID, recipient).Scan(&count); err != nil {
			return err
		}
		if count > 0 {
			return nil
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO conversation_flow_receipts(run_id,event_id,recipient) VALUES(?,?,?)", l.RunID, eventID, recipient)
		return err
	})
}
func (s *Store) Delivered(ctx context.Context, runID, eventID, recipient string) (bool, error) {
	if _, err := s.Get(ctx, runID); err != nil {
		return false, err
	}
	var count int
	err := s.conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM conversation_flow_receipts WHERE run_id=? AND event_id=? AND recipient=?", runID, eventID, recipient).Scan(&count)
	return count > 0, err
}

// RunsWithEvents includes terminal runs whose delivery may need retry. Callers
// use recipient-specific receipts to decide whether an event is outstanding.
func (s *Store) RunsWithEvents(ctx context.Context, limit int) ([]Run, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := s.conn.QueryContext(ctx, "SELECT id FROM conversation_flow_runs WHERE project_id=? AND EXISTS (SELECT 1 FROM conversation_flow_events WHERE run_id=conversation_flow_runs.id) ORDER BY updated_at DESC LIMIT ?", s.projectID, limit)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := make([]Run, 0, len(ids))
	for _, id := range ids {
		r, err := s.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}
func (s *Store) ListActive(ctx context.Context) ([]Run, error) {
	rows, err := s.conn.QueryContext(ctx, "SELECT id FROM conversation_flow_runs WHERE project_id=? AND active_conversation IS NOT NULL ORDER BY created_at,id", s.projectID)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := make([]Run, 0, len(ids))
	for _, id := range ids {
		r, err := s.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// RunsWithEventsAfter supports fair replay sweeps without starving older runs
// when newer runs already have all recipient receipts. Empty afterID starts a
// sweep; after the final page, callers reset it for the next sweep.
func (s *Store) RunsWithEventsAfter(ctx context.Context, afterID string, limit int) ([]Run, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := s.conn.QueryContext(ctx, "SELECT id FROM conversation_flow_runs WHERE project_id=? AND id>? AND EXISTS (SELECT 1 FROM conversation_flow_events WHERE run_id=conversation_flow_runs.id) ORDER BY id LIMIT ?", s.projectID, afterID, limit)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := make([]Run, 0, len(ids))
	for _, id := range ids {
		r, err := s.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// TransitionEvent commits a state transition and its replayable lifecycle event
// together. A crash cannot leave a terminal run without a terminal outbox entry.
func (s *Store) TransitionEvent(ctx context.Context, l Lease, status, id, kind string, payload json.RawMessage, mutations ...func(db.QuerierWithTx) error) (Event, error) {
	var out Event
	encoded, err := canonical(payload)
	if err != nil {
		return out, err
	}
	err = s.transaction(ctx, func(tx *sql.Tx) error {
		if err := s.fence(ctx, tx, l); err != nil {
			return err
		}
		var priorKind, priorPayload string
		var sequence int64
		err := tx.QueryRowContext(ctx, "SELECT sequence,kind,payload FROM conversation_flow_events WHERE run_id=? AND id=?", l.RunID, id).Scan(&sequence, &priorKind, &priorPayload)
		if err == nil {
			var current string
			if err = tx.QueryRowContext(ctx, "SELECT status FROM conversation_flow_runs WHERE project_id=? AND id=?", s.projectID, l.RunID).Scan(&current); err != nil {
				return err
			}
			if current != status || priorKind != kind || priorPayload != encoded {
				return ErrConflict
			}
			out = Event{ID: id, RunID: l.RunID, Kind: kind, Sequence: sequence, Payload: json.RawMessage(encoded)}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		for _, mutate := range mutations {
			if mutate != nil {
				if err = mutate(s.queries.WithTx(tx)); err != nil {
					return err
				}
			}
		}
		if err = s.transition(ctx, tx, l, status, false); err != nil {
			return err
		}
		out, err = s.appendEventTx(ctx, tx, l, id, kind, payload)
		return err
	})
	return out, err
}

// TerminateExpired settles a hard-deadline run after its execution lease has
// expired. It never grants a runnable lease: ownership is revoked and the
// terminal event is committed with reservation release in one transaction.
func (s *Store) TerminateExpired(ctx context.Context, runID, id, kind string, payload json.RawMessage) (Event, error) {
	var out Event
	encoded, err := canonical(payload)
	if err != nil {
		return out, err
	}
	err = s.transaction(ctx, func(tx *sql.Tx) error {
		if err := s.lockRun(ctx, tx, runID); err != nil {
			return err
		}
		row, err := s.queries.WithTx(tx).GetConversationFlowRun(ctx, db.GetConversationFlowRunParams{ProjectID: s.projectID, ID: runID})
		if err != nil {
			return err
		}
		if terminal(row.Status) {
			var previousKind, previousPayload string
			var sequence int64
			if err = tx.QueryRowContext(ctx, "SELECT sequence,kind,payload FROM conversation_flow_events WHERE run_id=? AND id=?", runID, id).Scan(&sequence, &previousKind, &previousPayload); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return ErrConflict
				}
				return err
			}
			if previousKind != kind || previousPayload != encoded {
				return ErrConflict
			}
			out = Event{ID: id, RunID: runID, Kind: kind, Sequence: sequence, Payload: json.RawMessage(encoded)}
			return nil
		}
		now, err := s.clock(ctx, tx)
		if err != nil {
			return err
		}
		if row.Deadline == 0 || row.Deadline > now {
			return ErrConflict
		}
		if row.LeaseOwner != "" && row.LeaseExpiresAt > now {
			return ErrBusy
		}
		if _, err = tx.ExecContext(ctx, "UPDATE conversation_flow_runs SET status='terminated',active_conversation=NULL,lease_owner='',lease_expires_at=0,lease_token=lease_token+1,updated_at=? WHERE project_id=? AND id=?", now, s.projectID, runID); err != nil {
			return err
		}
		out, err = s.appendEventTx(ctx, tx, Lease{RunID: runID, Token: row.LeaseToken + 1}, id, kind, payload)
		return err
	})
	return out, err
}

// InboxHistory returns accepted messages in order, including delivered inputs,
// so the controller can materialize the root transcript at a safe boundary.
func (s *Store) InboxHistory(ctx context.Context, runID string) ([]Inbox, error) {
	if _, err := s.Get(ctx, runID); err != nil {
		return nil, err
	}
	rows, err := s.conn.QueryContext(ctx, "SELECT id,sequence,payload FROM conversation_flow_inbox WHERE run_id=? ORDER BY sequence", runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Inbox{}
	for rows.Next() {
		item := Inbox{RunID: runID}
		var payload string
		if err = rows.Scan(&item.ID, &item.Sequence, &payload); err != nil {
			return nil, err
		}
		item.Payload = json.RawMessage(payload)
		out = append(out, item)
	}
	return out, rows.Err()
}

// GetInbox retrieves the originally accepted host metadata for idempotent
// retries even after its target question closes or its delivery is acknowledged.
func (s *Store) GetInbox(ctx context.Context, runID, id string) (Inbox, error) {
	item := Inbox{RunID: runID, ID: id}
	var payload string
	err := s.conn.QueryRowContext(ctx, "SELECT i.sequence,i.payload FROM conversation_flow_inbox i JOIN conversation_flow_runs r ON r.id=i.run_id WHERE r.project_id=? AND i.run_id=? AND i.id=?", s.projectID, runID, id).Scan(&item.Sequence, &payload)
	item.Payload = json.RawMessage(payload)
	return item, err
}
