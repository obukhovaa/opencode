// Package flowrun persists conversation-owned flow runs independently of their
// execution engine and surface adapters. Every Store is project-scoped.
package flowrun

import (
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/opencode-ai/opencode/internal/db"
)

var (
	ErrNotFound     = sql.ErrNoRows
	ErrConflict     = errors.New("flow run conflict")
	ErrLeaseLost    = errors.New("flow run ownership lost")
	ErrBusy         = errors.New("conversation already owns a nonterminal flow")
	ErrInputPending = errors.New("queued_input_pending")
)

type Run struct {
	ID             string          `json:"id"`
	ProjectID      string          `json:"project_id"`
	ConversationID string          `json:"conversation_id"`
	Kind           string          `json:"kind"`
	Status         string          `json:"status"`
	InvocationID   string          `json:"invocation_id"`
	Snapshot       json.RawMessage `json:"snapshot"`
	Origin         json.RawMessage `json:"origin"`
	Version        int64           `json:"version"`
	Deadline       int64           `json:"deadline"`
	CreatedAt      int64           `json:"created_at"`
	UpdatedAt      int64           `json:"updated_at"`
}
type Lease struct {
	RunID     string `json:"run_id"`
	Owner     string `json:"owner"`
	Token     int64  `json:"token"`
	ExpiresAt int64  `json:"expires_at"`
}
type Inbox struct {
	ID       string          `json:"id"`
	RunID    string          `json:"run_id"`
	Sequence int64           `json:"sequence"`
	Payload  json.RawMessage `json:"payload"`
}
type Event struct {
	ID       string          `json:"id"`
	RunID    string          `json:"run_id"`
	Kind     string          `json:"kind"`
	Sequence int64           `json:"sequence"`
	Payload  json.RawMessage `json:"payload"`
}
type Question struct {
	ID         string          `json:"id"`
	RunID      string          `json:"run_id"`
	SessionID  string          `json:"session_id"`
	StepID     string          `json:"step_id"`
	RequestID  string          `json:"request_id"`
	Descriptor json.RawMessage `json:"descriptor"`
	Answer     json.RawMessage `json:"answer"`
	AnswerID   string          `json:"answer_id"`
	Status     string          `json:"status"`
}

type Store struct {
	conn               *sql.DB
	queries            db.QuerierWithTx
	projectID, dialect string
}

// New does not own conn. dialect is "mysql" or "sqlite" ("sqlite3" also works).
func New(conn *sql.DB, q db.QuerierWithTx, projectID, dialect string) *Store {
	if dialect != "mysql" {
		dialect = "sqlite"
	}
	return &Store{conn: conn, queries: q, projectID: projectID, dialect: dialect}
}
func (s *Store) Dialect() string                   { return s.dialect }
func (s *Store) SupportsDistributedRecovery() bool { return s.dialect == "mysql" }

func (s *Store) ProjectID() string { return s.projectID }
