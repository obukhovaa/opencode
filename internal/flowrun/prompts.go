package flowrun

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// PutPrompt pins the first resolved content/version; retries receive that value
// even if a remote prompt changed between resolution attempts.
func (s *Store) PutPrompt(ctx context.Context, l Lease, stepID string, payload json.RawMessage) (json.RawMessage, error) {
	if stepID == "" || len(stepID) > 64 {
		return nil, fmt.Errorf("invalid prompt step id")
	}
	encoded, err := canonical(payload)
	if err != nil {
		return nil, err
	}
	err = s.transaction(ctx, func(tx *sql.Tx) error {
		if err := s.fence(ctx, tx, l); err != nil {
			return err
		}
		var old string
		err := tx.QueryRowContext(ctx, "SELECT payload FROM conversation_flow_prompts WHERE run_id=? AND step_id=?", l.RunID, stepID).Scan(&old)
		if err == nil {
			encoded = old
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO conversation_flow_prompts(run_id,step_id,payload) VALUES(?,?,?)", l.RunID, stepID, encoded)
		return err
	})
	return json.RawMessage(encoded), err
}
func (s *Store) GetPrompt(ctx context.Context, runID, stepID string) (json.RawMessage, error) {
	if _, err := s.Get(ctx, runID); err != nil {
		return nil, err
	}
	var payload string
	err := s.conn.QueryRowContext(ctx, "SELECT payload FROM conversation_flow_prompts WHERE run_id=? AND step_id=?", runID, stepID).Scan(&payload)
	return json.RawMessage(payload), err
}
