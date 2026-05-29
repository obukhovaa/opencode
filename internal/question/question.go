package question

import (
	"context"
	"errors"
	"sync"

	"github.com/google/uuid"
	"github.com/opencode-ai/opencode/internal/pubsub"
)

var ErrQuestionRejected = errors.New("the user dismissed the question")

// Option represents a selectable choice for a question.
type Option struct {
	Label       string `json:"label"`       // Display text (1-5 words, concise)
	Description string `json:"description"` // Explanation of choice
}

// Prompt represents a single question with its options.
type Prompt struct {
	Question string   `json:"question"`           // Complete question text
	Options  []Option `json:"options"`            // Available choices
	Multiple bool     `json:"multiple,omitempty"` // Allow selecting multiple choices
	Custom   *bool    `json:"custom,omitempty"`   // Allow typing a custom answer (default: true)
}

// IsCustomEnabled returns whether custom answers are allowed (defaults to true).
func (p Prompt) IsCustomEnabled() bool {
	return p.Custom == nil || *p.Custom
}

// Request represents a pending question request from an agent.
type Request struct {
	ID        string   `json:"id"`
	SessionID string   `json:"session_id"`
	Questions []Prompt `json:"questions"`
}

// Service coordinates question requests between agents and the UI.
type Service interface {
	pubsub.Suscriber[Request]
	// Ask blocks until the user responds or ctx is cancelled.
	// Returns one answer per question (each answer is a slice of selected labels).
	Ask(ctx context.Context, sessionID string, questions []Prompt) ([][]string, error)
	// Reply resolves a pending question request with user answers.
	Reply(requestID string, answers [][]string) error
	// Reject dismisses a pending question request.
	Reject(requestID string) error
	// List returns all currently pending question requests.
	List() []Request
}

type pendingEntry struct {
	request  Request
	replyCh  chan [][]string
	rejectCh chan struct{}
}

type questionService struct {
	*pubsub.Broker[Request]
	pending sync.Map // requestID -> *pendingEntry
}

func (s *questionService) Ask(ctx context.Context, sessionID string, questions []Prompt) ([][]string, error) {
	requestID := uuid.New().String()
	request := Request{
		ID:        requestID,
		SessionID: sessionID,
		Questions: questions,
	}
	entry := &pendingEntry{
		request:  request,
		replyCh:  make(chan [][]string, 1),
		rejectCh: make(chan struct{}, 1),
	}
	s.pending.Store(requestID, entry)
	defer s.pending.Delete(requestID)

	s.Publish(pubsub.CreatedEvent, request)

	select {
	case answers := <-entry.replyCh:
		return answers, nil
	case <-entry.rejectCh:
		return nil, ErrQuestionRejected
	case <-ctx.Done():
		return nil, ErrQuestionRejected
	}
}

func (s *questionService) Reply(requestID string, answers [][]string) error {
	val, ok := s.pending.LoadAndDelete(requestID)
	if !ok {
		return errors.New("question request not found")
	}
	entry := val.(*pendingEntry)
	select {
	case entry.replyCh <- answers:
	default:
	}
	return nil
}

func (s *questionService) Reject(requestID string) error {
	val, ok := s.pending.LoadAndDelete(requestID)
	if !ok {
		return errors.New("question request not found")
	}
	entry := val.(*pendingEntry)
	select {
	case entry.rejectCh <- struct{}{}:
	default:
	}
	return nil
}

func (s *questionService) List() []Request {
	var requests []Request
	s.pending.Range(func(_, value any) bool {
		entry := value.(*pendingEntry)
		requests = append(requests, entry.request)
		return true
	})
	return requests
}

func NewService() Service {
	return &questionService{
		Broker: pubsub.NewBroker[Request](),
	}
}
