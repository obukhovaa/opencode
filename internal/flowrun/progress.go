package flowrun

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"
)

const maxCardStateBytes = 256 * 1024

type Card struct {
	RunID        string
	Recipient    string
	MessageToken string
	RenderHash   string
	Sequence     int64
	State        json.RawMessage
	UpdatedAt    int64
}

func (s *Store) GetCard(ctx context.Context, runID, recipient string) (Card, error) {
	var card Card
	var state string
	err := s.conn.QueryRowContext(ctx, `SELECT c.run_id,c.recipient,c.message_token,c.render_hash,c.sequence,c.state,c.updated_at
FROM conversation_flow_cards c JOIN conversation_flow_runs r ON r.id=c.run_id
WHERE r.project_id=? AND c.run_id=? AND c.recipient=?`, s.projectID, runID, recipient).Scan(
		&card.RunID, &card.Recipient, &card.MessageToken, &card.RenderHash, &card.Sequence, &state, &card.UpdatedAt)
	if err != nil {
		return Card{}, err
	}
	card.State = json.RawMessage(state)
	return card, nil
}

func (s *Store) SaveCard(ctx context.Context, lease Lease, card Card) error {
	if card.RunID == "" || card.RunID != lease.RunID {
		return fmt.Errorf("card run does not match delivery lease: %w", ErrConflict)
	}
	if card.Recipient == "" || len(card.Recipient) > 255 || !utf8.ValidString(card.Recipient) {
		return errors.New("invalid card recipient")
	}
	if card.Sequence < 0 {
		return errors.New("invalid card sequence")
	}
	if len(card.MessageToken) > 4096 || !utf8.ValidString(card.MessageToken) || len(card.RenderHash) > 128 || !utf8.ValidString(card.RenderHash) {
		return errors.New("invalid card message reference")
	}
	if len(card.State) > maxCardStateBytes || !utf8.Valid(card.State) || !json.Valid(card.State) {
		return errors.New("invalid or oversized card state")
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		if err := s.fence(ctx, tx, lease); err != nil {
			return err
		}
		var previous int64
		err := tx.QueryRowContext(ctx, "SELECT sequence FROM conversation_flow_cards WHERE run_id=? AND recipient=?", card.RunID, card.Recipient).Scan(&previous)
		exists := err == nil
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if exists && card.Sequence < previous {
			return fmt.Errorf("card sequence would regress: %w", ErrConflict)
		}
		now, err := s.clock(ctx, tx)
		if err != nil {
			return err
		}
		// The run fence serializes inserts and updates across both backends. A
		// same-sequence update permits a verbosity change or missing-message repair.
		if exists {
			_, err = tx.ExecContext(ctx, `UPDATE conversation_flow_cards SET message_token=?,render_hash=?,sequence=?,state=?,updated_at=? WHERE run_id=? AND recipient=?`,
				card.MessageToken, card.RenderHash, card.Sequence, string(card.State), now, card.RunID, card.Recipient)
		} else {
			_, err = tx.ExecContext(ctx, `INSERT INTO conversation_flow_cards (run_id,recipient,message_token,render_hash,sequence,state,updated_at) VALUES (?,?,?,?,?,?,?)`,
				card.RunID, card.Recipient, card.MessageToken, card.RenderHash, card.Sequence, string(card.State), now)
		}
		return err
	})
}

func (s *Store) MoveCard(ctx context.Context, lease Lease, oldRecipient, newRecipient string) (Card, error) {
	if oldRecipient == "" || newRecipient == "" || oldRecipient == newRecipient || len(oldRecipient) > 255 || len(newRecipient) > 255 || !utf8.ValidString(oldRecipient) || !utf8.ValidString(newRecipient) {
		return Card{}, fmt.Errorf("invalid card recipient move: %w", ErrConflict)
	}
	var card Card
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		if err := s.fence(ctx, tx, lease); err != nil {
			return err
		}
		var state string
		if err := tx.QueryRowContext(ctx, "SELECT message_token,render_hash,sequence,state,updated_at FROM conversation_flow_cards WHERE run_id=? AND recipient=?", lease.RunID, oldRecipient).Scan(
			&card.MessageToken, &card.RenderHash, &card.Sequence, &state, &card.UpdatedAt); err != nil {
			return err
		}
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM conversation_flow_cards WHERE run_id=? AND recipient=?", lease.RunID, newRecipient).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			return ErrConflict
		}
		if _, err := tx.ExecContext(ctx, "UPDATE conversation_flow_cards SET recipient=? WHERE run_id=? AND recipient=?", newRecipient, lease.RunID, oldRecipient); err != nil {
			return err
		}
		card.RunID, card.Recipient, card.State = lease.RunID, newRecipient, json.RawMessage(state)
		return nil
	})
	if err != nil {
		return Card{}, err
	}
	return card, nil
}
