package message

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/google/uuid"

	"github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/pubsub"
)

// CreateInTransaction records a message through a caller-owned transaction. The
// caller must publish it only after that transaction commits; this lets durable
// question recovery atomically fence its result and consume the answer receipt.
func CreateInTransaction(ctx context.Context, svc Service, q db.QuerierWithTx, sessionID string, params CreateMessageParams) (Message, error) {
	return CreateInTransactionWithID(ctx, svc, q, uuid.NewString(), sessionID, params)
}

func CreateInTransactionWithID(ctx context.Context, svc Service, q db.QuerierWithTx, id, sessionID string, params CreateMessageParams) (Message, error) {
	s, ok := svc.(*service)
	if !ok {
		return Message{}, fmt.Errorf("message service does not support transactional creation")
	}
	parts := append([]ContentPart(nil), params.Parts...)
	if params.Role != Assistant {
		parts = append(parts, Finish{Reason: "stop"})
	}
	data, err := marshallParts(parts)
	if err != nil {
		return Message{}, err
	}
	seq := params.Seq
	if seq == 0 {
		seq, err = s.nextSeqTx(ctx, q, sessionID)
		if err != nil {
			return Message{}, err
		}
	}
	row, err := q.CreateMessage(ctx, db.CreateMessageParams{ID: id, SessionID: sessionID, Role: string(params.Role), Parts: string(data), Model: sql.NullString{String: string(params.Model), Valid: true}, Seq: sql.NullInt64{Int64: seq, Valid: true}, Synthetic: params.Synthetic})
	if err != nil {
		return Message{}, err
	}
	return s.fromDBItem(row)
}
func PublishCreated(svc Service, msg Message) {
	if s, ok := svc.(*service); ok && msg.ID != "" {
		s.Publish(pubsub.CreatedEvent, msg)
	}
}

func GetInTransaction(ctx context.Context, svc Service, q db.QuerierWithTx, id string) (Message, error) {
	s, ok := svc.(*service)
	if !ok {
		return Message{}, fmt.Errorf("message service does not support transactional reads")
	}
	row, err := q.GetMessage(ctx, id)
	if err != nil {
		return Message{}, err
	}
	return s.fromDBItem(row)
}
