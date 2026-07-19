package message

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/pubsub"
)

// partsBufferSize is the per-subscriber channel buffer for the parts broker.
// One turn with ~10 parallel tools × 3 lifecycle transitions = 30 events.
// A higher cap than the default (64) reduces drops if an SSE client is slow.
const partsBufferSize = 256

const BytesPerTokenEta = 4

type CreateMessageParams struct {
	Role  MessageRole
	Parts []ContentPart
	Model models.ModelID
	Seq   int64
	// Synthetic marks the message as system-injected (not produced by the
	// agent or user). See message.Message.Synthetic for details.
	Synthetic bool
}

type Service interface {
	pubsub.Suscriber[Message]
	Create(ctx context.Context, sessionID string, params CreateMessageParams) (Message, error)
	// CreatePair atomically creates two messages in a single transaction with consecutive
	// sequence numbers. Used by the cron scheduler to write synthetic tool_call + tool_result
	// pairs that cannot be interleaved with agent-authored messages.
	CreatePair(ctx context.Context, sessionID string, first, second CreateMessageParams) (Message, Message, error)
	Update(ctx context.Context, message Message) error
	Get(ctx context.Context, id string) (Message, error)
	List(ctx context.Context, sessionID string) ([]Message, error)
	ListLatest(ctx context.Context, sessionID string, limit int64) ([]Message, error)
	MaxSeq(ctx context.Context, sessionID string) (int64, error)
	Delete(ctx context.Context, id string) error
	DeleteSessionMessages(ctx context.Context, sessionID string) error

	// Per-part SSE event surface — independent of the whole-message broker.
	SubscribeParts(ctx context.Context) <-chan pubsub.Event[PartEvent]
	// PublishPart emits a part-level event. Returns immediately without
	// allocating when no subscribers are connected (the dominant CLI/TUI case).
	PublishPart(sessionID, messageID string, part ContentPart)
	// Shutdown stops both brokers and releases subscriber channels. Safe to
	// call multiple times.
	Shutdown()
}

type service struct {
	*pubsub.Broker[Message]
	parts *pubsub.Broker[PartEvent]
	db    *sql.DB
	q     db.QuerierWithTx
}

func NewService(q db.QuerierWithTx, database *sql.DB) Service {
	return &service{
		Broker: pubsub.NewBroker[Message](),
		parts:  pubsub.NewBrokerWithOptions[PartEvent](partsBufferSize, 1000),
		q:      q,
		db:     database,
	}
}

func (s *service) SubscribeParts(ctx context.Context) <-chan pubsub.Event[PartEvent] {
	return s.parts.Subscribe(ctx)
}

func (s *service) PublishPart(sessionID, messageID string, part ContentPart) {
	// Fast path: zero subscribers (CLI/TUI default). One RLock via
	// GetSubscriberCount() returns the cached subCount; no allocation,
	// no clonePart, no map iteration, no channel send.
	if s.parts.GetSubscriberCount() == 0 {
		return
	}
	s.parts.Publish(pubsub.UpdatedEvent, PartEvent{
		SessionID: sessionID,
		MessageID: messageID,
		Part:      clonePart(part),
		Time:      time.Now().UnixMilli(),
	})
}

func (s *service) Shutdown() {
	s.Broker.Shutdown()
	s.parts.Shutdown()
}

func (s *service) Delete(ctx context.Context, id string) error {
	message, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	err = s.q.DeleteMessage(ctx, message.ID)
	if err != nil {
		return err
	}
	s.Publish(pubsub.DeletedEvent, message)
	return nil
}

func (s *service) Create(ctx context.Context, sessionID string, params CreateMessageParams) (Message, error) {
	if params.Role != Assistant {
		params.Parts = append(params.Parts, Finish{
			Reason: "stop",
		})
	}
	partsJSON, err := marshallParts(params.Parts)
	if err != nil {
		return Message{}, err
	}
	seq := params.Seq
	if seq != 0 {
		dbMessage, err := s.q.CreateMessage(ctx, db.CreateMessageParams{
			ID:        uuid.New().String(),
			SessionID: sessionID,
			Role:      string(params.Role),
			Parts:     string(partsJSON),
			Model:     sql.NullString{String: string(params.Model), Valid: true},
			Seq:       sql.NullInt64{Int64: seq, Valid: true},
			Synthetic: params.Synthetic,
		})
		if err != nil {
			return Message{}, err
		}
		message, err := s.fromDBItem(dbMessage)
		if err != nil {
			return Message{}, err
		}
		s.Publish(pubsub.CreatedEvent, message)
		return message, nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return Message{}, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	qtx := s.q.WithTx(tx)

	seq, err = s.nextSeqTx(ctx, qtx, sessionID)
	if err != nil {
		return Message{}, err
	}

	dbMessage, err := qtx.CreateMessage(ctx, db.CreateMessageParams{
		ID:        uuid.New().String(),
		SessionID: sessionID,
		Role:      string(params.Role),
		Parts:     string(partsJSON),
		Model:     sql.NullString{String: string(params.Model), Valid: true},
		Seq:       sql.NullInt64{Int64: seq, Valid: true},
		Synthetic: params.Synthetic,
	})
	if err != nil {
		return Message{}, err
	}

	if err = tx.Commit(); err != nil {
		return Message{}, fmt.Errorf("failed to commit transaction: %w", err)
	}
	message, err := s.fromDBItem(dbMessage)
	if err != nil {
		return Message{}, err
	}
	s.Publish(pubsub.CreatedEvent, message)
	return message, nil
}

func (s *service) CreatePair(ctx context.Context, sessionID string, first, second CreateMessageParams) (Message, Message, error) {
	if (first.Seq != 0) != (second.Seq != 0) {
		return Message{}, Message{}, fmt.Errorf("CreatePair: both Seq values must be set or both must be zero")
	}
	if first.Seq != 0 && second.Seq <= first.Seq {
		return Message{}, Message{}, fmt.Errorf("CreatePair: second Seq (%d) must be greater than first (%d)", second.Seq, first.Seq)
	}

	firstJSON, err := marshallParts(first.Parts)
	if err != nil {
		return Message{}, Message{}, err
	}
	// Add finish part for non-assistant messages. Copy the slice first so we
	// never mutate the caller's backing array (a future caller might reuse the
	// same slice across calls or hold a reference to it).
	secondParts := second.Parts
	if second.Role != Assistant {
		secondParts = make([]ContentPart, len(second.Parts), len(second.Parts)+1)
		copy(secondParts, second.Parts)
		secondParts = append(secondParts, Finish{Reason: "stop"})
	}
	secondJSON, err := marshallParts(secondParts)
	if err != nil {
		return Message{}, Message{}, err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return Message{}, Message{}, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	qtx := s.q.WithTx(tx)

	seq1 := first.Seq
	seq2 := second.Seq
	if seq1 == 0 {
		seq1, err = s.nextSeqTx(ctx, qtx, sessionID)
		if err != nil {
			return Message{}, Message{}, err
		}
		seq2 = seq1 + 1
	}

	dbMsg1, err := qtx.CreateMessage(ctx, db.CreateMessageParams{
		ID:        uuid.New().String(),
		SessionID: sessionID,
		Role:      string(first.Role),
		Parts:     string(firstJSON),
		Model:     sql.NullString{String: string(first.Model), Valid: true},
		Seq:       sql.NullInt64{Int64: seq1, Valid: true},
		Synthetic: first.Synthetic,
	})
	if err != nil {
		return Message{}, Message{}, fmt.Errorf("failed to create first message: %w", err)
	}

	dbMsg2, err := qtx.CreateMessage(ctx, db.CreateMessageParams{
		ID:        uuid.New().String(),
		SessionID: sessionID,
		Role:      string(second.Role),
		Parts:     string(secondJSON),
		Model:     sql.NullString{String: string(second.Model), Valid: true},
		Seq:       sql.NullInt64{Int64: seq2, Valid: true},
		Synthetic: second.Synthetic,
	})
	if err != nil {
		return Message{}, Message{}, fmt.Errorf("failed to create second message: %w", err)
	}

	if err = tx.Commit(); err != nil {
		return Message{}, Message{}, fmt.Errorf("failed to commit transaction: %w", err)
	}

	msg1, err := s.fromDBItem(dbMsg1)
	if err != nil {
		return Message{}, Message{}, err
	}
	msg2, err := s.fromDBItem(dbMsg2)
	if err != nil {
		return Message{}, Message{}, err
	}

	// Publish after commit
	s.Publish(pubsub.CreatedEvent, msg1)
	s.Publish(pubsub.CreatedEvent, msg2)

	// For synthetic injections (cron-fired completions, background
	// bash/task/monitor) also publish per-part events with Synthetic=true
	// so SSE/TUI/bridge subscribers see them. The bridge's parts demux
	// filters on Synthetic and skips outbound tool-update indicators —
	// but transcript-style consumers (TUI, transcript exporter) still need
	// the events to render the synthetic pair.
	if msg1.Synthetic {
		for _, p := range msg1.Parts {
			s.publishSyntheticPart(msg1.SessionID, msg1.ID, p)
		}
	}
	if msg2.Synthetic {
		for _, p := range msg2.Parts {
			s.publishSyntheticPart(msg2.SessionID, msg2.ID, p)
		}
	}

	return msg1, msg2, nil
}

// publishSyntheticPart is the synthetic-flavored variant of PublishPart.
// Emits a PartEvent with Synthetic=true. Same zero-subscriber fast path.
func (s *service) publishSyntheticPart(sessionID, messageID string, part ContentPart) {
	if s.parts.GetSubscriberCount() == 0 {
		return
	}
	s.parts.Publish(pubsub.UpdatedEvent, PartEvent{
		SessionID: sessionID,
		MessageID: messageID,
		Part:      clonePart(part),
		Synthetic: true,
		Time:      time.Now().UnixMilli(),
	})
}

func (s *service) nextSeqTx(ctx context.Context, qtx db.Querier, sessionID string) (int64, error) {
	maxSeq, err := qtx.GetMaxSeqBySession(ctx, sessionID)
	if err != nil {
		return 0, err
	}
	return maxSeq + 1, nil
}

func (s *service) DeleteSessionMessages(ctx context.Context, sessionID string) error {
	messages, err := s.List(ctx, sessionID)
	if err != nil {
		return err
	}
	for _, message := range messages {
		if message.SessionID == sessionID {
			err = s.Delete(ctx, message.ID)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *service) Update(ctx context.Context, message Message) error {
	parts, err := marshallParts(message.Parts)
	if err != nil {
		return err
	}
	finishedAt := sql.NullInt64{}
	if f := message.FinishPart(); f != nil {
		finishedAt.Int64 = f.Time
		finishedAt.Valid = true
	}
	err = s.q.UpdateMessage(ctx, db.UpdateMessageParams{
		ID:         message.ID,
		Parts:      string(parts),
		FinishedAt: finishedAt,
	})
	if err != nil {
		return err
	}
	message.UpdatedAt = time.Now().Unix()
	s.Publish(pubsub.UpdatedEvent, message)
	return nil
}

func (s *service) Get(ctx context.Context, id string) (Message, error) {
	dbMessage, err := s.q.GetMessage(ctx, id)
	if err != nil {
		return Message{}, err
	}
	return s.fromDBItem(dbMessage)
}

func (s *service) List(ctx context.Context, sessionID string) ([]Message, error) {
	dbMessages, err := s.q.ListMessagesBySession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	messages := make([]Message, len(dbMessages))
	for i, dbMessage := range dbMessages {
		messages[i], err = s.fromDBItem(dbMessage)
		if err != nil {
			return nil, err
		}
	}
	return messages, nil
}

func (s *service) ListLatest(ctx context.Context, sessionID string, limit int64) ([]Message, error) {
	dbMessages, err := s.q.ListLatestMessagesBySession(ctx, db.ListLatestMessagesBySessionParams{
		SessionID: sessionID,
		Limit:     limit,
	})
	if err != nil {
		return nil, err
	}
	messages := make([]Message, len(dbMessages))
	for i, dbMessage := range dbMessages {
		messages[i], err = s.fromDBItem(dbMessage)
		if err != nil {
			return nil, err
		}
	}
	return messages, nil
}

func (s *service) MaxSeq(ctx context.Context, sessionID string) (int64, error) {
	return s.q.GetMaxSeqBySession(ctx, sessionID)
}

func (s *service) fromDBItem(item db.Message) (Message, error) {
	parts, err := unmarshallParts([]byte(item.Parts))
	if err != nil {
		return Message{}, err
	}
	return Message{
		ID:        item.ID,
		SessionID: item.SessionID,
		Role:      MessageRole(item.Role),
		Parts:     parts,
		Model:     models.ModelID(item.Model.String),
		Seq:       item.Seq.Int64,
		CreatedAt: item.CreatedAt,
		UpdatedAt: item.UpdatedAt,
		Synthetic: item.Synthetic,
	}, nil
}

type partType string

const (
	reasoningType  partType = "reasoning"
	textType       partType = "text"
	imageURLType   partType = "image_url"
	binaryType     partType = "binary"
	toolCallType   partType = "tool_call"
	toolResultType partType = "tool_result"
	finishType     partType = "finish"
)

type partWrapper struct {
	Type partType    `json:"type"`
	Data ContentPart `json:"data"`
}

func marshallParts(parts []ContentPart) ([]byte, error) {
	wrappedParts := make([]partWrapper, len(parts))

	for i, part := range parts {
		var typ partType

		switch part.(type) {
		case ReasoningContent:
			typ = reasoningType
		case TextContent:
			typ = textType
		case ImageURLContent:
			typ = imageURLType
		case BinaryContent:
			typ = binaryType
		case ToolCall:
			typ = toolCallType
		case ToolResult:
			typ = toolResultType
		case Finish:
			typ = finishType
		default:
			return nil, fmt.Errorf("unknown part type: %T", part)
		}

		wrappedParts[i] = partWrapper{
			Type: typ,
			Data: part,
		}
	}
	return json.Marshal(wrappedParts)
}

func unmarshallParts(data []byte) ([]ContentPart, error) {
	temp := []json.RawMessage{}

	if err := json.Unmarshal(data, &temp); err != nil {
		return nil, err
	}

	parts := make([]ContentPart, 0)

	for _, rawPart := range temp {
		var wrapper struct {
			Type partType        `json:"type"`
			Data json.RawMessage `json:"data"`
		}

		if err := json.Unmarshal(rawPart, &wrapper); err != nil {
			return nil, err
		}

		switch wrapper.Type {
		case reasoningType:
			part := ReasoningContent{}
			if err := json.Unmarshal(wrapper.Data, &part); err != nil {
				return nil, err
			}
			parts = append(parts, part)
		case textType:
			part := TextContent{}
			if err := json.Unmarshal(wrapper.Data, &part); err != nil {
				return nil, err
			}
			parts = append(parts, part)
		case imageURLType:
			part := ImageURLContent{}
			if err := json.Unmarshal(wrapper.Data, &part); err != nil {
				return nil, err
			}
			parts = append(parts, part)
		case binaryType:
			part := BinaryContent{}
			if err := json.Unmarshal(wrapper.Data, &part); err != nil {
				return nil, err
			}
			parts = append(parts, part)
		case toolCallType:
			part := ToolCall{}
			if err := json.Unmarshal(wrapper.Data, &part); err != nil {
				return nil, err
			}
			parts = append(parts, part)
		case toolResultType:
			part := ToolResult{}
			if err := json.Unmarshal(wrapper.Data, &part); err != nil {
				return nil, err
			}
			parts = append(parts, part)
		case finishType:
			part := Finish{}
			if err := json.Unmarshal(wrapper.Data, &part); err != nil {
				return nil, err
			}
			parts = append(parts, part)
		default:
			return nil, fmt.Errorf("unknown part type: %s", wrapper.Type)
		}

	}

	return parts, nil
}

// Roughly estimate tokens count from message history
// This is a rough estimation: ~4 characters per token for most models
func EstimateTokens(messages []Message, tools []tools.BaseTool, bytesPerToken int) int64 {
	totalChars := 0
	for _, msg := range messages {
		for _, part := range msg.Parts {
			if textPart, ok := part.(TextContent); ok {
				totalChars += len(textPart.Text)
			}
			// For tool calls and other content types, add some overhead
			totalChars += 100 // rough estimate for metadata
		}
	}
	for _, t := range tools {
		info := t.Info()
		totalChars += len(info.Name)
		totalChars += len(info.Description)
		for n, p := range info.Parameters {
			totalChars += len(n)
			if jsonBytes, err := json.Marshal(p); err == nil {
				totalChars += len(jsonBytes)
			} else {
				totalChars += 10 // fallback estimate if marshal fails
			}
		}
	}
	return int64(totalChars / bytesPerToken)
}
