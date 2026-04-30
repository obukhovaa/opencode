package session

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/pubsub"
)

type Session struct {
	ID                    string
	ProjectID             string
	ParentSessionID       string
	RootSessionID         string
	Title                 string
	MessageCount          int64
	PromptTokens          int64
	CompletionTokens      int64
	TotalPromptTokens     int64
	TotalCompletionTokens int64
	SummaryMessageID      string
	Cost                  float64
	CreatedAt             int64
	UpdatedAt             int64
}

type Service interface {
	pubsub.Suscriber[Session]
	Create(ctx context.Context, title string) (Session, error)
	CreateWithID(ctx context.Context, id, title string) (Session, error)
	CreateFlowSession(ctx context.Context, id, rootSessionID, title string) (Session, error)
	CreateTitleSession(ctx context.Context, parentSessionID string) (Session, error)
	CreateTaskSession(ctx context.Context, toolCallID, parentSessionID, title string) (Session, error)
	Get(ctx context.Context, id string) (Session, error)
	List(ctx context.Context) ([]Session, error)
	ListChildren(ctx context.Context, rootSessionID string) ([]Session, error)
	Save(ctx context.Context, session Session) (Session, error)
	Delete(ctx context.Context, id string) error
	DeleteTree(ctx context.Context, id string) error
	ListOldSessions(ctx context.Context, activeSessionID string) ([]Session, error)
	CleanupOldSessions(ctx context.Context, activeSessionID string) (int, error)
}

type service struct {
	*pubsub.Broker[Session]
	q         db.Querier
	projectID string
}

func (s *service) Create(ctx context.Context, title string) (Session, error) {
	id := uuid.New().String()
	return s.createWithID(ctx, id, title)
}

func (s *service) CreateWithID(ctx context.Context, id, title string) (Session, error) {
	return s.createWithID(ctx, id, title)
}

func (s *service) CreateFlowSession(ctx context.Context, id, rootSessionID, title string) (Session, error) {
	dbSession, err := s.q.CreateSession(ctx, db.CreateSessionParams{
		ID:            id,
		ProjectID:     sql.NullString{String: s.projectID, Valid: true},
		RootSessionID: sql.NullString{String: rootSessionID, Valid: true},
		Title:         title,
	})
	if err != nil {
		return Session{}, err
	}
	session := s.fromDBItem(dbSession)
	s.Publish(pubsub.CreatedEvent, session)
	return session, nil
}

func (s *service) createWithID(ctx context.Context, id, title string) (Session, error) {
	dbSession, err := s.q.CreateSession(ctx, db.CreateSessionParams{
		ID:            id,
		ProjectID:     sql.NullString{String: s.projectID, Valid: true},
		RootSessionID: sql.NullString{String: id, Valid: true},
		Title:         title,
	})
	if err != nil {
		return Session{}, err
	}
	session := s.fromDBItem(dbSession)
	s.Publish(pubsub.CreatedEvent, session)
	return session, nil
}

func (s *service) CreateTaskSession(ctx context.Context, toolCallID, parentSessionID, title string) (Session, error) {
	parent, err := s.q.GetSessionByID(ctx, parentSessionID)
	if err != nil {
		return Session{}, fmt.Errorf("failed to get parent session: %w", err)
	}

	rootSessionID := parent.RootSessionID.String
	if !parent.RootSessionID.Valid || rootSessionID == "" {
		rootSessionID = parentSessionID
	}

	dbSession, err := s.q.CreateSession(ctx, db.CreateSessionParams{
		ID:              toolCallID,
		ProjectID:       sql.NullString{String: s.projectID, Valid: true},
		ParentSessionID: sql.NullString{String: parentSessionID, Valid: true},
		RootSessionID:   sql.NullString{String: rootSessionID, Valid: true},
		Title:           title,
	})
	if err != nil {
		return Session{}, err
	}
	session := s.fromDBItem(dbSession)
	s.Publish(pubsub.CreatedEvent, session)
	return session, nil
}

func (s *service) CreateTitleSession(ctx context.Context, parentSessionID string) (Session, error) {
	parent, err := s.q.GetSessionByID(ctx, parentSessionID)
	if err != nil {
		return Session{}, fmt.Errorf("failed to get parent session: %w", err)
	}

	rootSessionID := parent.RootSessionID.String
	if !parent.RootSessionID.Valid || rootSessionID == "" {
		rootSessionID = parentSessionID
	}

	dbSession, err := s.q.CreateSession(ctx, db.CreateSessionParams{
		ID:              "title-" + parentSessionID,
		ProjectID:       sql.NullString{String: s.projectID, Valid: true},
		ParentSessionID: sql.NullString{String: parentSessionID, Valid: true},
		RootSessionID:   sql.NullString{String: rootSessionID, Valid: true},
		Title:           "Generate a title",
	})
	if err != nil {
		return Session{}, err
	}
	session := s.fromDBItem(dbSession)
	s.Publish(pubsub.CreatedEvent, session)
	return session, nil
}

func (s *service) Delete(ctx context.Context, id string) error {
	session, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	// Only cascade to the entire tree when deleting the root session itself.
	// Deleting an intermediate node deletes only that single row so callers
	// that target a sub-session do not silently wipe siblings.
	if session.RootSessionID == "" || session.RootSessionID == session.ID {
		return s.deleteTree(ctx, session)
	}
	if err := s.q.DeleteSession(ctx, session.ID); err != nil {
		return err
	}
	s.Publish(pubsub.DeletedEvent, session)
	return nil
}

// DeleteTree deletes a session and every descendant sharing its root_session_id.
// It is safe to call with any session ID in the tree; the entire tree rooted at
// session.RootSessionID is removed.
func (s *service) DeleteTree(ctx context.Context, id string) error {
	session, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	return s.deleteTree(ctx, session)
}

func (s *service) deleteTree(ctx context.Context, session Session) error {
	rootID := session.RootSessionID
	if rootID == "" {
		rootID = session.ID
	}
	// Match both the root row itself (for legacy sessions where
	// root_session_id is NULL) and any descendants pointing at it.
	if err := s.q.DeleteSessionTree(ctx, db.DeleteSessionTreeParams{
		ID:            rootID,
		RootSessionID: sql.NullString{String: rootID, Valid: true},
	}); err != nil {
		return fmt.Errorf("failed to delete session tree: %w", err)
	}
	s.Publish(pubsub.DeletedEvent, session)
	return nil
}

func (s *service) Get(ctx context.Context, id string) (Session, error) {
	dbSession, err := s.q.GetSessionByID(ctx, id)
	if err != nil {
		return Session{}, err
	}
	return s.fromDBItem(dbSession), nil
}

func (s *service) Save(ctx context.Context, session Session) (Session, error) {
	dbSession, err := s.q.UpdateSession(ctx, db.UpdateSessionParams{
		ID:                    session.ID,
		Title:                 session.Title,
		PromptTokens:          session.PromptTokens,
		CompletionTokens:      session.CompletionTokens,
		TotalPromptTokens:     session.TotalPromptTokens,
		TotalCompletionTokens: session.TotalCompletionTokens,
		SummaryMessageID: sql.NullString{
			String: session.SummaryMessageID,
			Valid:  session.SummaryMessageID != "",
		},
		Cost: session.Cost,
	})
	if err != nil {
		return Session{}, err
	}
	session = s.fromDBItem(dbSession)
	s.Publish(pubsub.UpdatedEvent, session)
	return session, nil
}

func (s *service) List(ctx context.Context) ([]Session, error) {
	dbSessions, err := s.q.ListSessions(ctx, sql.NullString{String: s.projectID, Valid: true})
	if err != nil {
		return nil, err
	}
	sessions := make([]Session, len(dbSessions))
	for i, dbSession := range dbSessions {
		sessions[i] = s.fromDBItem(dbSession)
	}
	return sessions, nil
}

func (s *service) ListChildren(ctx context.Context, rootSessionID string) ([]Session, error) {
	dbSessions, err := s.q.ListChildSessions(ctx, sql.NullString{String: rootSessionID, Valid: true})
	if err != nil {
		return nil, err
	}
	sessions := make([]Session, len(dbSessions))
	for i, dbSession := range dbSessions {
		sessions[i] = s.fromDBItem(dbSession)
	}
	return sessions, nil
}

func (s service) fromDBItem(item db.Session) Session {
	return Session{
		ID:                    item.ID,
		ProjectID:             item.ProjectID.String,
		ParentSessionID:       item.ParentSessionID.String,
		RootSessionID:         item.RootSessionID.String,
		Title:                 item.Title,
		MessageCount:          item.MessageCount,
		PromptTokens:          item.PromptTokens,
		CompletionTokens:      item.CompletionTokens,
		TotalPromptTokens:     item.TotalPromptTokens,
		TotalCompletionTokens: item.TotalCompletionTokens,
		SummaryMessageID:      item.SummaryMessageID.String,
		Cost:                  item.Cost,
		CreatedAt:             item.CreatedAt,
		UpdatedAt:             item.UpdatedAt,
	}
}

// ListOldSessions returns one entry per old session tree (deduped by effective
// root id) whose root session has not been updated within the configured max
// age. The active session and any session sharing its tree are excluded.
func (s *service) ListOldSessions(ctx context.Context, activeSessionID string) ([]Session, error) {
	sessions, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	maxAge := config.Get().SessionCleanupMaxAge()
	cutoff := time.Now().Add(-maxAge).Unix()

	// Index every returned row by id so we can resolve a flow step to its root
	// even though sql sessions list filters out parent_session_id IS NULL.
	byID := make(map[string]Session, len(sessions))
	for _, sess := range sessions {
		byID[sess.ID] = sess
	}

	// Resolve the active session's effective root so we exclude the
	// entire tree even when the user is viewing a non-root flow step.
	activeRootID := activeSessionID
	if activeSess, ok := byID[activeSessionID]; ok && activeSess.RootSessionID != "" {
		activeRootID = activeSess.RootSessionID
	}

	seenRoot := make(map[string]struct{})
	old := make([]Session, 0)
	for _, sess := range sessions {
		rootID := sess.RootSessionID
		if rootID == "" {
			rootID = sess.ID
		}
		if rootID == activeRootID {
			continue
		}
		if _, ok := seenRoot[rootID]; ok {
			continue
		}

		// Use the root row's updated_at when we can find it so non-root
		// flow rows do not keep the tree alive past its actual idle time.
		bench := sess
		if root, ok := byID[rootID]; ok {
			bench = root
		}
		if bench.UpdatedAt >= cutoff {
			continue
		}

		seenRoot[rootID] = struct{}{}
		old = append(old, bench)
	}
	return old, nil
}

func (s *service) CleanupOldSessions(ctx context.Context, activeSessionID string) (int, error) {
	old, err := s.ListOldSessions(ctx, activeSessionID)
	if err != nil {
		return 0, err
	}
	deleted := 0
	for _, sess := range old {
		if err := s.DeleteTree(ctx, sess.ID); err != nil {
			// Tolerate rows that have already disappeared (e.g. removed by
			// a concurrent session.Delete or via FK cascade earlier in
			// this same loop) - they still count toward "trees gone".
			if errors.Is(err, sql.ErrNoRows) {
				deleted++
				continue
			}
			return deleted, fmt.Errorf("failed to delete session %s: %w", sess.ID, err)
		}
		deleted++
	}
	return deleted, nil
}

func NewService(q db.Querier, explicitProjectID string) Service {
	var projectID string
	if explicitProjectID != "" {
		projectID = explicitProjectID
	} else {
		cfg := config.Get()
		projectID = db.GetProjectID(cfg.WorkingDir)
	}
	broker := pubsub.NewBroker[Session]()
	return &service{
		broker,
		q,
		projectID,
	}
}
