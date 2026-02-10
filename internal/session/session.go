package session

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/google/uuid"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/pubsub"
)

type Session struct {
	ID               string
	ProjectID        string
	ParentSessionID  string
	RootSessionID    string
	Title            string
	MessageCount     int64
	PromptTokens     int64
	CompletionTokens int64
	SummaryMessageID string
	Cost             float64
	CreatedAt        int64
	UpdatedAt        int64
}

type Service interface {
	pubsub.Suscriber[Session]
	Create(ctx context.Context, title string) (Session, error)
	CreateTitleSession(ctx context.Context, parentSessionID string) (Session, error)
	CreateTaskSession(ctx context.Context, toolCallID, parentSessionID, title string) (Session, error)
	Get(ctx context.Context, id string) (Session, error)
	List(ctx context.Context) ([]Session, error)
	ListChildren(ctx context.Context, rootSessionID string) ([]Session, error)
	Save(ctx context.Context, session Session) (Session, error)
	Delete(ctx context.Context, id string) error
}

type service struct {
	*pubsub.Broker[Session]
	q         db.Querier
	projectID string
}

func (s *service) Create(ctx context.Context, title string) (Session, error) {
	id := uuid.New().String()
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
	err = s.q.DeleteSession(ctx, session.ID)
	if err != nil {
		return err
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
		ID:               session.ID,
		Title:            session.Title,
		PromptTokens:     session.PromptTokens,
		CompletionTokens: session.CompletionTokens,
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
		ID:               item.ID,
		ProjectID:        item.ProjectID.String,
		ParentSessionID:  item.ParentSessionID.String,
		RootSessionID:    item.RootSessionID.String,
		Title:            item.Title,
		MessageCount:     item.MessageCount,
		PromptTokens:     item.PromptTokens,
		CompletionTokens: item.CompletionTokens,
		SummaryMessageID: item.SummaryMessageID.String,
		Cost:             item.Cost,
		CreatedAt:        item.CreatedAt,
		UpdatedAt:        item.UpdatedAt,
	}
}

func NewService(q db.Querier) Service {
	cfg := config.Get()
	projectID := db.GetProjectID(cfg.WorkingDir)
	broker := pubsub.NewBroker[Session]()
	return &service{
		broker,
		q,
		projectID,
	}
}
