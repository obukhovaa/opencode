package flowrun

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/opencode-ai/opencode/internal/db"
)

const questionColumns = "q.id,q.run_id,q.session_id,q.step_id,q.request_id,q.descriptor,q.answer,q.answer_id,q.status"

type rowScanner interface{ Scan(...any) error }

func scanQuestion(row rowScanner) (Question, error) {
	var q Question
	var descriptor, answer string
	err := row.Scan(&q.ID, &q.RunID, &q.SessionID, &q.StepID, &q.RequestID, &descriptor, &answer, &q.AnswerID, &q.Status)
	q.Descriptor = json.RawMessage(descriptor)
	q.Answer = json.RawMessage(answer)
	return q, err
}
func (s *Store) GetQuestion(ctx context.Context, runID, id string) (Question, error) {
	return scanQuestion(s.conn.QueryRowContext(ctx, "SELECT "+questionColumns+" FROM conversation_flow_questions q JOIN conversation_flow_runs r ON r.id=q.run_id WHERE r.project_id=? AND q.run_id=? AND q.id=?", s.projectID, runID, id))
}
func (s *Store) FindQuestionByRequest(ctx context.Context, requestID string) (Question, error) {
	rows, err := s.conn.QueryContext(ctx, "SELECT DISTINCT "+questionColumns+" FROM conversation_flow_questions q LEFT JOIN conversation_flow_question_requests a ON q.run_id=a.run_id AND q.id=a.question_id JOIN conversation_flow_runs r ON r.id=q.run_id WHERE r.project_id=? AND (a.request_id=? OR q.id=?)", s.projectID, requestID, requestID)
	if err != nil {
		return Question{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err = rows.Err(); err != nil {
			return Question{}, err
		}
		return Question{}, ErrNotFound
	}
	q, err := scanQuestion(rows)
	if err != nil {
		return Question{}, err
	}
	if rows.Next() {
		return Question{}, ErrConflict
	}
	return q, rows.Err()
}

func (s *Store) Questions(ctx context.Context, runID string) ([]Question, error) {
	if _, err := s.Get(ctx, runID); err != nil {
		return nil, err
	}
	rows, err := s.conn.QueryContext(ctx, "SELECT "+questionColumns+" FROM conversation_flow_questions q WHERE q.run_id=? AND q.status IN ('pending','answered','interrupted') ORDER BY q.id", runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Question{}
	for rows.Next() {
		q, err := scanQuestion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, rows.Err()
}
func (s *Store) PutQuestion(ctx context.Context, l Lease, q Question) (Question, error) {
	if q.RunID != "" && q.RunID != l.RunID {
		return Question{}, ErrConflict
	}
	q.RunID = l.RunID
	if q.ID == "" || len(q.ID) > 255 || q.RequestID == "" || len(q.RequestID) > 255 || q.SessionID == "" || len(q.SessionID) > 255 || len(q.StepID) > 64 {
		return Question{}, fmt.Errorf("invalid question identity")
	}
	descriptor, err := canonical(q.Descriptor)
	if err != nil {
		return Question{}, err
	}
	err = s.transaction(ctx, func(tx *sql.Tx) error {
		if err := s.fence(ctx, tx, l); err != nil {
			return err
		}
		old, err := scanQuestion(tx.QueryRowContext(ctx, "SELECT "+questionColumns+" FROM conversation_flow_questions q WHERE q.run_id=? AND q.id=?", l.RunID, q.ID))
		if err == nil {
			if string(old.Descriptor) != descriptor || old.SessionID != q.SessionID || old.StepID != q.StepID {
				return ErrConflict
			}
			if _, err = tx.ExecContext(ctx, "UPDATE conversation_flow_questions SET request_id=? WHERE run_id=? AND id=?", q.RequestID, l.RunID, q.ID); err != nil {
				return err
			}
		} else if errors.Is(err, sql.ErrNoRows) {
			_, err = tx.ExecContext(ctx, "INSERT INTO conversation_flow_questions(run_id,id,session_id,step_id,request_id,descriptor,answer,answer_id,status) VALUES(?,?,?,?,?,?,'{}','','pending')", l.RunID, q.ID, q.SessionID, q.StepID, q.RequestID, descriptor)
			if err != nil {
				return err
			}
		} else {
			return err
		}
		var prior string
		err = tx.QueryRowContext(ctx, "SELECT question_id FROM conversation_flow_question_requests WHERE run_id=? AND request_id=?", l.RunID, q.RequestID).Scan(&prior)
		if err == nil {
			if prior != q.ID {
				return ErrConflict
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO conversation_flow_question_requests(run_id,question_id,request_id) VALUES(?,?,?)", l.RunID, q.ID, q.RequestID)
		return err
	})
	if err != nil {
		return Question{}, err
	}
	return s.GetQuestion(ctx, l.RunID, q.ID)
}
func (s *Store) AnswerQuestion(ctx context.Context, runID, id, answerID string, answer json.RawMessage) (Question, bool, error) {
	if answerID == "" || len(answerID) > 255 {
		return Question{}, false, fmt.Errorf("invalid answer identity")
	}
	encoded, err := canonical(answer)
	if err != nil {
		return Question{}, false, err
	}
	created := false
	err = s.transaction(ctx, func(tx *sql.Tx) error {
		created = false
		if err := s.lockRun(ctx, tx, runID); err != nil {
			return err
		}
		q, err := scanQuestion(tx.QueryRowContext(ctx, "SELECT "+questionColumns+" FROM conversation_flow_questions q WHERE q.run_id=? AND q.id=?", runID, id))
		if err != nil {
			return err
		}
		if q.Status == "interrupted" {
			return ErrInputPending
		}
		if q.Status != "pending" {
			if q.AnswerID == answerID && string(q.Answer) == encoded {
				return nil
			}
			return ErrConflict
		}
		pending, err := tx.QueryContext(ctx, "SELECT payload FROM conversation_flow_inbox WHERE run_id=? AND delivered=0", runID)
		if err != nil {
			return err
		}
		blocked := false
		for pending.Next() {
			var payload string
			if err = pending.Scan(&payload); err != nil {
				pending.Close()
				return err
			}
			var target struct {
				QuestionID string `json:"targetQuestionId"`
			}
			if json.Unmarshal([]byte(payload), &target) == nil && target.QuestionID == id {
				blocked = true
			}
		}
		err = pending.Err()
		pending.Close()
		if err != nil {
			return err
		}
		if blocked {
			return ErrInputPending
		}
		var status string
		var deadline int64
		if err = tx.QueryRowContext(ctx, "SELECT status,deadline FROM conversation_flow_runs WHERE project_id=? AND id=?", s.projectID, runID).Scan(&status, &deadline); err != nil {
			return err
		}
		now, err := s.clock(ctx, tx)
		if err != nil {
			return err
		}
		if terminal(status) || deadline > 0 && deadline <= now {
			return ErrConflict
		}
		_, err = tx.ExecContext(ctx, "UPDATE conversation_flow_questions SET answer=?,answer_id=?,status='answered' WHERE run_id=? AND id=?", encoded, answerID, runID, id)
		created = err == nil
		return err
	})
	if err != nil {
		return Question{}, false, err
	}
	q, err := s.GetQuestion(ctx, runID, id)
	return q, created, err
}
func (s *Store) AckAnswer(ctx context.Context, l Lease, id string) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		if err := s.fence(ctx, tx, l); err != nil {
			return err
		}
		var status string
		if err := tx.QueryRowContext(ctx, "SELECT status FROM conversation_flow_questions WHERE run_id=? AND id=?", l.RunID, id).Scan(&status); err != nil {
			return err
		}
		if status == "pending" {
			return ErrConflict
		}
		if status == "delivered" {
			return nil
		}
		_, err := tx.ExecContext(ctx, "UPDATE conversation_flow_questions SET status='delivered' WHERE run_id=? AND id=?", l.RunID, id)
		return err
	})
}

// CommitAnswer writes the exact recovered tool result and its answer receipt in
// one fenced transaction. A retry after commitment does not invoke mutate again.
func (s *Store) CommitAnswer(ctx context.Context, l Lease, id string, mutate func(db.QuerierWithTx) error) error {
	if mutate == nil {
		return fmt.Errorf("answer mutation is required")
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		if err := s.CheckLeaseTx(ctx, tx, l); err != nil {
			return err
		}
		var status string
		if err := tx.QueryRowContext(ctx, "SELECT status FROM conversation_flow_questions WHERE run_id=? AND id=?", l.RunID, id).Scan(&status); err != nil {
			return err
		}
		if status == "delivered" {
			return nil
		}
		if status != "answered" && status != "interrupted" {
			return ErrConflict
		}
		if err := mutate(s.queries.WithTx(tx)); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "UPDATE conversation_flow_questions SET status='delivered' WHERE run_id=? AND id=?", l.RunID, id)
		return err
	})
}

// InterruptQuestion records queued steering taking precedence over an unanswered
// prompt. An already committed answer wins and is returned unchanged.
func (s *Store) InterruptQuestion(ctx context.Context, l Lease, id, reason string) (Question, error) {
	payload, err := json.Marshal(struct {
		Unanswered string `json:"unanswered"`
	}{reason})
	if err != nil {
		return Question{}, err
	}
	sum := sha256.Sum256([]byte(l.RunID + "\x00" + id + "\x00" + reason))
	answerID := "interrupt-" + hex.EncodeToString(sum[:])
	err = s.transaction(ctx, func(tx *sql.Tx) error {
		if err := s.CheckLeaseTx(ctx, tx, l); err != nil {
			return err
		}
		q, err := scanQuestion(tx.QueryRowContext(ctx, "SELECT "+questionColumns+" FROM conversation_flow_questions q WHERE q.run_id=? AND q.id=?", l.RunID, id))
		if err != nil {
			return err
		}
		if q.Status != "pending" {
			return nil
		}
		_, err = tx.ExecContext(ctx, "UPDATE conversation_flow_questions SET status='interrupted',answer=?,answer_id=? WHERE run_id=? AND id=?", string(payload), answerID, l.RunID, id)
		return err
	})
	if err != nil {
		return Question{}, err
	}
	return s.GetQuestion(ctx, l.RunID, id)
}

// TargetInbox attaches previously untargeted input to the first question that
// claims it. Target assignment and interruption share the run-row transaction,
// so competing questions cannot both consume the same accepted directions.
// Input already targeted elsewhere, or an already answered question, is kept.
func (s *Store) TargetInbox(ctx context.Context, lease Lease, questionID, reason string) (Question, []Inbox, error) {
	var current Question
	var claimed []Inbox
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		claimed = nil
		if err := s.CheckLeaseTx(ctx, tx, lease); err != nil {
			return err
		}
		var err error
		current, err = scanQuestion(tx.QueryRowContext(ctx, "SELECT "+questionColumns+" FROM conversation_flow_questions q WHERE q.run_id=? AND q.id=?", lease.RunID, questionID))
		if err != nil {
			return err
		}
		if current.Status != "pending" && current.Status != "interrupted" {
			return nil
		}
		rows, err := tx.QueryContext(ctx, "SELECT id,sequence,payload FROM conversation_flow_inbox WHERE run_id=? AND delivered=0 ORDER BY sequence", lease.RunID)
		if err != nil {
			return err
		}
		var items []Inbox
		for rows.Next() {
			item := Inbox{RunID: lease.RunID}
			var payload string
			if err = rows.Scan(&item.ID, &item.Sequence, &payload); err != nil {
				rows.Close()
				return err
			}
			item.Payload = json.RawMessage(payload)
			items = append(items, item)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, item := range items {
			var fields map[string]json.RawMessage
			if err = json.Unmarshal(item.Payload, &fields); err != nil || fields == nil {
				return ErrConflict
			}
			var targetQuestion, targetSession string
			if value := fields["targetQuestionId"]; len(value) > 0 {
				if err = json.Unmarshal(value, &targetQuestion); err != nil {
					return err
				}
			}
			if value := fields["targetSessionId"]; len(value) > 0 {
				if err = json.Unmarshal(value, &targetSession); err != nil {
					return err
				}
			}
			if targetQuestion == questionID {
				claimed = append(claimed, item)
				continue
			}
			var noRetarget bool
			if value := fields["noQuestionRetarget"]; len(value) > 0 {
				if err = json.Unmarshal(value, &noRetarget); err != nil {
					return err
				}
			}
			if targetQuestion != "" || targetSession != "" || current.Status != "pending" || noRetarget {
				continue
			}
			fields["targetQuestionId"], _ = json.Marshal(current.ID)
			fields["targetSessionId"], _ = json.Marshal(current.SessionID)
			if current.StepID != "" {
				fields["targetStepId"], _ = json.Marshal(current.StepID)
			} else {
				delete(fields, "targetStepId")
			}
			item.Payload, err = json.Marshal(fields)
			if err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, "UPDATE conversation_flow_inbox SET payload=? WHERE run_id=? AND id=? AND delivered=0", string(item.Payload), lease.RunID, item.ID); err != nil {
				return err
			}
			claimed = append(claimed, item)
		}
		if len(claimed) > 0 && current.Status == "pending" {
			current.Answer, err = json.Marshal(struct {
				Unanswered string `json:"unanswered"`
			}{reason})
			if err != nil {
				return err
			}
			sum := sha256.Sum256([]byte(lease.RunID + "\x00" + current.ID + "\x00" + reason))
			current.AnswerID = "interrupt-" + hex.EncodeToString(sum[:])
			current.Status = "interrupted"
			_, err = tx.ExecContext(ctx, "UPDATE conversation_flow_questions SET status='interrupted',answer=?,answer_id=? WHERE run_id=? AND id=?", string(current.Answer), current.AnswerID, lease.RunID, current.ID)
			if err != nil {
				return err
			}
		}
		return nil
	})
	return current, claimed, err
}
