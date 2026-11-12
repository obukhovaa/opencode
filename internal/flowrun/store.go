package flowrun

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	sqlite3 "github.com/ncruces/go-sqlite3"

	"github.com/opencode-ai/opencode/internal/db"
)

func canonical(data json.RawMessage) (string, error) {
	if len(data) == 0 {
		return "{}", nil
	}
	if !json.Valid(data) {
		return "", fmt.Errorf("invalid JSON payload")
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return "", err
	}
	b, err := json.Marshal(value)
	return string(b), err
}
func validateOrigin(data string) error {
	var value any
	if err := json.Unmarshal([]byte(data), &value); err != nil {
		return err
	}
	var walk func(any) error
	walk = func(v any) error {
		switch v := v.(type) {
		case map[string]any:
			for k, item := range v {
				key := strings.NewReplacer("_", "", "-", "").Replace(strings.ToLower(k))
				switch key {
				case "token", "accesstoken", "refreshtoken", "apikey", "secret", "secretkey", "password", "authorization", "cookie", "privatekey":
					return fmt.Errorf("origin must contain references, not credentials (%s)", k)
				}
				if err := walk(item); err != nil {
					return err
				}
			}
		case []any:
			for _, item := range v {
				if err := walk(item); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(value)
}
func terminal(status string) bool { return status == "completed" || status == "terminated" }
func validStatus(status string) bool {
	switch status {
	case "planning", "ready", "running", "waiting_for_input", "suspended", "postponed", "failed", "interrupted", "completed", "terminated":
		return true
	}
	return false
}
func (s *Store) clockSQL() string {
	if s.dialect == "mysql" {
		return "CAST(UNIX_TIMESTAMP(CURRENT_TIMESTAMP(3))*1000 AS SIGNED)"
	}
	return "CAST((julianday('now')-2440587.5)*86400000 AS INTEGER)"
}
func (s *Store) clock(ctx context.Context, tx *sql.Tx) (int64, error) {
	var now int64
	err := tx.QueryRowContext(ctx, "SELECT "+s.clockSQL()).Scan(&now)
	return now, err
}
func (s *Store) transaction(ctx context.Context, fn func(*sql.Tx) error) error {
	for attempt := 0; ; attempt++ {
		err := db.WithTxRetry(ctx, func() error {
			tx, err := s.conn.BeginTx(ctx, nil)
			if err != nil {
				return err
			}
			defer tx.Rollback()
			if err = fn(tx); err != nil {
				return err
			}
			return tx.Commit()
		})
		if attempt >= 5 || (!errors.Is(err, sqlite3.BUSY) && !errors.Is(err, sqlite3.LOCKED)) {
			return err
		}
		timer := time.NewTimer(time.Duration(1<<attempt) * 5 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func fromRow(r db.ConversationFlowRun) Run {
	return Run{ID: r.ID, ProjectID: r.ProjectID, ConversationID: r.ConversationID, Kind: r.Kind, Status: r.Status, InvocationID: r.InvocationID, Snapshot: json.RawMessage(r.Snapshot), Origin: json.RawMessage(r.Origin), Version: r.Version, Deadline: r.Deadline, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
}
func (s *Store) Get(ctx context.Context, id string) (Run, error) {
	r, err := s.queries.GetConversationFlowRun(ctx, db.GetConversationFlowRunParams{ProjectID: s.projectID, ID: id})
	return fromRow(r), err
}
func (s *Store) Current(ctx context.Context, conversation string) (Run, error) {
	r, err := s.queries.CurrentConversationFlowRun(ctx, db.CurrentConversationFlowRunParams{ProjectID: s.projectID, ActiveConversation: sql.NullString{String: conversation, Valid: true}})
	return fromRow(r), err
}
func (s *Store) List(ctx context.Context, conversation string) ([]Run, error) {
	rows, err := s.queries.ListConversationFlowRuns(ctx, db.ListConversationFlowRunsParams{ProjectID: s.projectID, ConversationID: conversation})
	if err != nil {
		return nil, err
	}
	out := make([]Run, 0, len(rows))
	for _, r := range rows {
		out = append(out, fromRow(r))
	}
	return out, nil
}

func (s *Store) Create(ctx context.Context, r Run) (Run, bool, error) {
	if r.ConversationID == "" || r.InvocationID == "" {
		return Run{}, false, fmt.Errorf("conversation and invocation IDs are required")
	}
	if r.Kind != "generated" && r.Kind != "named" && r.Kind != "standalone" {
		return Run{}, false, fmt.Errorf("invalid run kind")
	}
	if r.Status == "" {
		r.Status = "ready"
	}
	if !validStatus(r.Status) {
		return Run{}, false, fmt.Errorf("invalid status")
	}
	if r.ID == "" {
		r.ID = uuid.NewString()
	}
	if len(r.ID) > 255 || len(r.ConversationID) > 255 || len(r.InvocationID) > 255 {
		return Run{}, false, fmt.Errorf("run identifier exceeds storage limit")
	}
	snapshot, err := canonical(r.Snapshot)
	if err != nil {
		return Run{}, false, err
	}
	origin, err := canonical(r.Origin)
	if err != nil {
		return Run{}, false, err
	}
	if err = validateOrigin(origin); err != nil {
		return Run{}, false, err
	}
	hash := sha256.Sum256([]byte(r.Kind + "\n" + snapshot + "\n" + origin + fmt.Sprint(r.Deadline)))
	invocationHash := hex.EncodeToString(hash[:])
	findExisting := func() (Run, error) {
		var id, storedHash string
		err := s.conn.QueryRowContext(ctx, "SELECT id,invocation_hash FROM conversation_flow_runs WHERE project_id=? AND conversation_id=? AND invocation_id=?", s.projectID, r.ConversationID, r.InvocationID).Scan(&id, &storedHash)
		if err != nil {
			return Run{}, err
		}
		if storedHash != invocationHash {
			return Run{}, ErrConflict
		}
		return s.Get(ctx, id)
	}
	if existing, err := findExisting(); err == nil {
		return existing, false, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Run{}, false, err
	}
	err = s.transaction(ctx, func(tx *sql.Tx) error {
		if err := s.lockConversation(ctx, tx, r.ConversationID); err != nil {
			return err
		}
		now, err := s.clock(ctx, tx)
		if err != nil {
			return err
		}
		var latest int64
		if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(created_at),0) FROM conversation_flow_runs WHERE project_id=? AND conversation_id=?", s.projectID, r.ConversationID).Scan(&latest); err != nil {
			return err
		}
		if latest >= now {
			now = latest + 1
		}
		var active any = r.ConversationID
		if terminal(r.Status) {
			active = nil
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO conversation_flow_runs(id,project_id,conversation_id,invocation_id,invocation_hash,active_conversation,kind,status,snapshot,origin,version,deadline,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,1,?,?,?)`, r.ID, s.projectID, r.ConversationID, r.InvocationID, invocationHash, active, r.Kind, r.Status, snapshot, origin, r.Deadline, now, now)
		return err
	})
	if err != nil {
		if existing, retryErr := findExisting(); retryErr == nil {
			return existing, false, nil
		} else if errors.Is(retryErr, ErrConflict) {
			return Run{}, false, retryErr
		}
		if active, activeErr := s.Current(ctx, r.ConversationID); activeErr == nil && active.ID != r.ID {
			return Run{}, false, ErrBusy
		}
		return Run{}, false, err
	}
	created, err := s.Get(ctx, r.ID)
	return created, true, err
}

func durationMillis(ttl time.Duration) (int64, error) {
	ms := ttl.Milliseconds()
	if ms <= 0 || ms > int64((24*time.Hour)/time.Millisecond) {
		return 0, fmt.Errorf("lease TTL must be between 1ms and 24h")
	}
	return ms, nil
}
func (s *Store) Claim(ctx context.Context, id, owner string, ttl time.Duration) (Lease, error) {
	return s.claim(ctx, id, owner, ttl, false)
}

// ClaimDelivery can claim a completed run for durable outbound receipt retries.
// It never enables writes to terminal flow-state rows through FencedQuerier.
func (s *Store) ClaimDelivery(ctx context.Context, id, owner string, ttl time.Duration) (Lease, error) {
	return s.claim(ctx, id, owner, ttl, true)
}
func (s *Store) claim(ctx context.Context, id, owner string, ttl time.Duration, delivery bool) (Lease, error) {
	ms, err := durationMillis(ttl)
	if err != nil {
		return Lease{}, err
	}
	if owner == "" || len(owner) > 255 {
		return Lease{}, fmt.Errorf("invalid lease owner")
	}
	var lease Lease
	err = s.transaction(ctx, func(tx *sql.Tx) error {
		// This first write serializes claimers before reading backend time/state.
		res, err := tx.ExecContext(ctx, "UPDATE conversation_flow_runs SET guard_serial=guard_serial+1 WHERE project_id=? AND id=?", s.projectID, id)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			return ErrNotFound
		}
		row, err := s.queries.WithTx(tx).GetConversationFlowRun(ctx, db.GetConversationFlowRunParams{ProjectID: s.projectID, ID: id})
		if err != nil {
			return err
		}
		now, err := s.clock(ctx, tx)
		if err != nil {
			return err
		}
		if !delivery && (terminal(row.Status) || row.Deadline > 0 && row.Deadline <= now) {
			return ErrLeaseLost
		}
		if row.LeaseOwner != "" && row.LeaseExpiresAt > now {
			return ErrBusy
		}
		expiry := now + ms
		if !delivery && row.Deadline > 0 && expiry > row.Deadline {
			expiry = row.Deadline
		}
		lease = Lease{RunID: id, Owner: owner, Token: row.LeaseToken + 1, ExpiresAt: expiry}
		_, err = tx.ExecContext(ctx, "UPDATE conversation_flow_runs SET lease_owner=?,lease_token=?,lease_expires_at=?,updated_at=? WHERE project_id=? AND id=?", owner, lease.Token, expiry, now, s.projectID, id)
		return err
	})
	return lease, err
}

// fence acquires the run row's write lock and checks the owner atomically.
// All protected effects execute in this same transaction before releasing it.
func (s *Store) fence(ctx context.Context, tx *sql.Tx, l Lease) error {
	if l.Owner == "" || l.Token <= 0 {
		return ErrLeaseLost
	}
	// Lock first, then read backend time in a new statement. MySQL NOW() is
	// statement-start time: checking it in an UPDATE that waits for this lock
	// could incorrectly accept a lease that expired during the wait.
	if err := s.lockRun(ctx, tx, l.RunID); err != nil {
		if errors.Is(err, ErrNotFound) {
			return ErrLeaseLost
		}
		return err
	}
	row, err := s.queries.WithTx(tx).GetConversationFlowRun(ctx, db.GetConversationFlowRunParams{ProjectID: s.projectID, ID: l.RunID})
	if err != nil {
		return err
	}
	now, err := s.clock(ctx, tx)
	if err != nil {
		return err
	}
	if row.LeaseOwner != l.Owner || row.LeaseToken != l.Token || row.LeaseExpiresAt <= now {
		return ErrLeaseLost
	}
	return nil
}

func (s *Store) Renew(ctx context.Context, l Lease, ttl time.Duration) (Lease, error) {
	ms, err := durationMillis(ttl)
	if err != nil {
		return Lease{}, err
	}
	err = s.transaction(ctx, func(tx *sql.Tx) error {
		if err := s.fence(ctx, tx, l); err != nil {
			return err
		}
		row, err := s.queries.WithTx(tx).GetConversationFlowRun(ctx, db.GetConversationFlowRunParams{ProjectID: s.projectID, ID: l.RunID})
		if err != nil {
			return err
		}
		now, err := s.clock(ctx, tx)
		if err != nil {
			return err
		}
		expiry := now + ms
		if !terminal(row.Status) && row.Deadline > 0 && expiry > row.Deadline {
			expiry = row.Deadline
		}
		if expiry <= now {
			return ErrLeaseLost
		}
		l.ExpiresAt = expiry
		_, err = tx.ExecContext(ctx, "UPDATE conversation_flow_runs SET lease_expires_at=?,updated_at=? WHERE project_id=? AND id=?", expiry, now, s.projectID, l.RunID)
		return err
	})
	return l, err
}
func (s *Store) transition(ctx context.Context, tx *sql.Tx, l Lease, status string, release bool) error {
	if status != "" && !validStatus(status) {
		return fmt.Errorf("invalid run status")
	}
	if err := s.fence(ctx, tx, l); err != nil {
		return err
	}
	var current string
	if err := tx.QueryRowContext(ctx, "SELECT status FROM conversation_flow_runs WHERE project_id=? AND id=?", s.projectID, l.RunID).Scan(&current); err != nil {
		return err
	}
	if status == "" {
		status = current
	}
	if terminal(current) && status != current {
		return ErrConflict
	}
	var active any
	if !terminal(status) {
		var conversation string
		if err := tx.QueryRowContext(ctx, "SELECT conversation_id FROM conversation_flow_runs WHERE project_id=? AND id=?", s.projectID, l.RunID).Scan(&conversation); err != nil {
			return err
		}
		active = conversation
	}
	query := "UPDATE conversation_flow_runs SET status=?,active_conversation=?,updated_at=" + s.clockSQL()
	if release {
		query += ",lease_owner='',lease_expires_at=0"
	}
	query += " WHERE project_id=? AND id=?"
	_, err := tx.ExecContext(ctx, query, status, active, s.projectID, l.RunID)
	return err
}
func (s *Store) Transition(ctx context.Context, l Lease, status string) error {
	return s.transaction(ctx, func(tx *sql.Tx) error { return s.transition(ctx, tx, l, status, false) })
}
func (s *Store) Release(ctx context.Context, l Lease, status string) error {
	return s.transaction(ctx, func(tx *sql.Tx) error { return s.transition(ctx, tx, l, status, true) })
}
func (s *Store) SaveSnapshot(ctx context.Context, l Lease, snapshot json.RawMessage, expectedVersion int64) (Run, error) {
	encoded, err := canonical(snapshot)
	if err != nil {
		return Run{}, err
	}
	err = s.transaction(ctx, func(tx *sql.Tx) error {
		if err := s.fence(ctx, tx, l); err != nil {
			return err
		}
		row, err := s.queries.WithTx(tx).GetConversationFlowRun(ctx, db.GetConversationFlowRunParams{ProjectID: s.projectID, ID: l.RunID})
		if err != nil {
			return err
		}
		if terminal(row.Status) || row.Kind != "generated" || row.Version != expectedVersion {
			return ErrConflict
		}
		_, err = tx.ExecContext(ctx, "UPDATE conversation_flow_runs SET snapshot=?,version=version+1,updated_at="+s.clockSQL()+" WHERE project_id=? AND id=?", encoded, s.projectID, l.RunID)
		return err
	})
	if err != nil {
		return Run{}, err
	}
	return s.Get(ctx, l.RunID)
}

// CheckLease is the runtime's pre-tool guard. External effects cannot be rolled
// back by SQL, but no new call should begin after ownership loss is observed.
func (s *Store) CheckLease(ctx context.Context, l Lease) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		if err := s.fence(ctx, tx, l); err != nil {
			return err
		}
		var status string
		if err := tx.QueryRowContext(ctx, "SELECT status FROM conversation_flow_runs WHERE project_id=? AND id=?", s.projectID, l.RunID).Scan(&status); err != nil {
			return err
		}
		if terminal(status) {
			return ErrLeaseLost
		}
		return nil
	})
}

func (s *Store) lockConversation(ctx context.Context, tx *sql.Tx, conversation string) error {
	// RowsAffected may be zero for a no-op MySQL update, but it still locks the
	// matching row; check existence separately within the acquired transaction.
	if _, err := tx.ExecContext(ctx, "UPDATE sessions SET updated_at=updated_at WHERE id=? AND project_id=?", conversation, s.projectID); err != nil {
		return err
	}
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM sessions WHERE id=? AND project_id=?", conversation, s.projectID).Scan(&count); err != nil {
		return err
	}
	if count != 1 {
		return ErrNotFound
	}
	return nil
}

// DeleteConversation atomically removes flow records and invokes the session
// deletion against the same transaction. The caller must cancel/wait for local
// runners first. A live remote owner is refused instead of silently orphaned.
func (s *Store) DeleteConversation(ctx context.Context, conversation string, deleteSession func(db.QuerierWithTx) error) error {
	if deleteSession == nil {
		return fmt.Errorf("session deletion callback is required")
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		if err := s.lockConversation(ctx, tx, conversation); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE conversation_flow_runs SET guard_serial=guard_serial+1 WHERE project_id=? AND conversation_id=?", s.projectID, conversation); err != nil {
			return err
		}
		now, err := s.clock(ctx, tx)
		if err != nil {
			return err
		}
		var live int
		if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM conversation_flow_runs WHERE project_id=? AND conversation_id=? AND lease_owner<>'' AND lease_expires_at>?", s.projectID, conversation, now).Scan(&live); err != nil {
			return err
		}
		if live > 0 {
			return ErrBusy
		}
		if _, err = tx.ExecContext(ctx, "DELETE FROM conversation_flow_runs WHERE project_id=? AND conversation_id=?", s.projectID, conversation); err != nil {
			return err
		}
		return deleteSession(s.queries.WithTx(tx))
	})
}

// CheckDeliveryLease permits terminal-result delivery while still refusing an
// expired or superseded owner. External delivery remains at-least-once: callers
// carry stable event IDs so receivers can deduplicate a crash after the send.
func (s *Store) CheckDeliveryLease(ctx context.Context, lease Lease) error {
	return s.transaction(ctx, func(tx *sql.Tx) error { return s.fence(ctx, tx, lease) })
}

// CheckLeaseTx is the message service's transactional ownership guard. The
// caller commits its message mutation before releasing this lease-row lock.
func (s *Store) CheckLeaseTx(ctx context.Context, tx *sql.Tx, lease Lease) error {
	if err := s.fence(ctx, tx, lease); err != nil {
		return err
	}
	var status string
	if err := tx.QueryRowContext(ctx, "SELECT status FROM conversation_flow_runs WHERE project_id=? AND id=?", s.projectID, lease.RunID).Scan(&status); err != nil {
		return err
	}
	if terminal(status) {
		return ErrLeaseLost
	}
	return nil
}

// Latest and ByInvocation avoid loading every historical definition into memory
// for status polling or idempotent submission on a long-lived conversation.
func (s *Store) Latest(ctx context.Context, conversation string) (Run, error) {
	var id string
	if err := s.conn.QueryRowContext(ctx, "SELECT id FROM conversation_flow_runs WHERE project_id=? AND conversation_id=? ORDER BY created_at DESC,id DESC LIMIT 1", s.projectID, conversation).Scan(&id); err != nil {
		return Run{}, err
	}
	return s.Get(ctx, id)
}
func (s *Store) ByInvocation(ctx context.Context, conversation, invocation string) (Run, error) {
	var id string
	if err := s.conn.QueryRowContext(ctx, "SELECT id FROM conversation_flow_runs WHERE project_id=? AND conversation_id=? AND invocation_id=?", s.projectID, conversation, invocation).Scan(&id); err != nil {
		return Run{}, err
	}
	return s.Get(ctx, id)
}
