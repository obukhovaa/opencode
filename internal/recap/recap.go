package recap

import (
	"context"
	"database/sql"
	"errors"

	"github.com/google/uuid"
	"github.com/opencode-ai/opencode/internal/db"
)

type Recap struct {
	ID           string
	SessionID    string
	Content      string
	MessageCount int64
	CreatedAt    int64
}

type Service interface {
	Get(ctx context.Context, sessionID string) (Recap, bool, error)
	Save(ctx context.Context, sessionID string, content string, messageCount int64) (Recap, error)
	Delete(ctx context.Context, sessionID string) error
}

type service struct {
	q db.Querier
}

func (s *service) Get(ctx context.Context, sessionID string) (Recap, bool, error) {
	r, err := s.q.GetRecapBySessionID(ctx, sessionID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Recap{}, false, nil
		}
		return Recap{}, false, err
	}
	return fromDBItem(r), true, nil
}

func (s *service) Save(ctx context.Context, sessionID string, content string, messageCount int64) (Recap, error) {
	r, err := s.q.UpsertRecap(ctx, db.UpsertRecapParams{
		ID:           uuid.New().String(),
		SessionID:    sessionID,
		Content:      content,
		MessageCount: messageCount,
	})
	if err != nil {
		return Recap{}, err
	}
	return fromDBItem(r), nil
}

func (s *service) Delete(ctx context.Context, sessionID string) error {
	return s.q.DeleteRecapBySessionID(ctx, sessionID)
}

func fromDBItem(r db.SessionRecap) Recap {
	return Recap{
		ID:           r.ID,
		SessionID:    r.SessionID,
		Content:      r.Content,
		MessageCount: r.MessageCount,
		CreatedAt:    r.CreatedAt,
	}
}

func NewService(q db.Querier) Service {
	return &service{q: q}
}
