package question

import (
	"context"
	"errors"
	"sync"

	"github.com/google/uuid"
	"github.com/opencode-ai/opencode/internal/pubsub"
)

var ErrQuestionRejected = errors.New("the user dismissed the question")

// UnansweredError is what Ask returns for a question closed through
// Unanswered: the user did not answer it, and Note tells the agent why
// (for example, that they sent messages instead).
type UnansweredError struct {
	Note string
}

func (e *UnansweredError) Error() string { return e.Note }

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
	FlowRunID  string   `json:"flow_run_id,omitempty"`
	ToolCallID string   `json:"tool_call_id,omitempty"`
	ID         string   `json:"id"`
	SessionID  string   `json:"session_id"`
	Questions  []Prompt `json:"questions"`
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
	// Unanswered closes a pending question request without an answer.
	// Its Ask returns an *UnansweredError carrying note.
	Unanswered(requestID string, note string) error
	// List returns all currently pending question requests.
	List() []Request
}

type pendingEntry struct {
	request      Request
	replyCh      chan [][]string
	rejectCh     chan struct{}
	unansweredCh chan string
}

type questionService struct {
	*pubsub.Broker[Request]
	pending sync.Map // requestID -> *pendingEntry
}

func (s *questionService) Ask(ctx context.Context, sessionID string, questions []Prompt) ([][]string, error) {
	requestID := uuid.New().String()
	request := Request{
		FlowRunID:  FlowRunIDFromContext(ctx),
		ToolCallID: ToolCallIDFromContext(ctx),
		ID:         requestID,
		SessionID:  sessionID,
		Questions:  questions,
	}
	entry := &pendingEntry{
		request:      request,
		replyCh:      make(chan [][]string, 1),
		rejectCh:     make(chan struct{}, 1),
		unansweredCh: make(chan string, 1),
	}
	s.pending.Store(requestID, entry)
	defer s.pending.Delete(requestID)

	if hook, _ := ctx.Value(startHookKey{}).(StartHook); hook != nil {
		if err := hook(ctx, request); err != nil {
			return nil, err
		}
	}
	// A durable owner may settle a replayed question or consume queued input
	// before any widget is published. Never resurrect that closed request.
	if _, live := s.pending.Load(requestID); live {
		s.Publish(pubsub.CreatedEvent, request)
	}

	select {
	case answers := <-entry.replyCh:
		return answers, nil
	case <-entry.rejectCh:
		return nil, ErrQuestionRejected
	case note := <-entry.unansweredCh:
		return nil, &UnansweredError{Note: note}
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

func (s *questionService) Unanswered(requestID string, note string) error {
	val, ok := s.pending.LoadAndDelete(requestID)
	if !ok {
		return errors.New("question request not found")
	}
	entry := val.(*pendingEntry)
	select {
	case entry.unansweredCh <- note:
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

type toolCallIDKey struct{}

func WithToolCallID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, toolCallIDKey{}, id)
}
func ToolCallIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(toolCallIDKey{}).(string)
	return id
}

// DurableDescriptor and DurableAnswer retain a question's logical tool identity
// across worker replacement; credentials and transport widgets are not stored.
type DurableDescriptor struct {
	Questions  []Prompt `json:"questions"`
	ToolCallID string   `json:"toolCallId"`
}
type DurableAnswer struct {
	Unanswered string     `json:"unanswered,omitempty"`
	Answers    [][]string `json:"answers"`
	Rejected   bool       `json:"rejected"`
}

type flowRunIDKey struct{}

func WithFlowRunID(ctx context.Context, runID string) context.Context {
	return context.WithValue(ctx, flowRunIDKey{}, runID)
}
func FlowRunIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(flowRunIDKey{}).(string)
	return id
}
func InheritFlowRunID(dst, src context.Context) context.Context {
	if id := FlowRunIDFromContext(src); id != "" {
		dst = WithFlowRunID(dst, id)
	}
	if hook, _ := src.Value(startHookKey{}).(StartHook); hook != nil {
		dst = WithStartHook(dst, hook)
	}
	return dst
}

type steeringQuestionKey struct{}

// WithSteeringQuestion carries an adapter-selected visible request, never a
// caller-provided question ID. Presence with an empty ID explicitly means that
// no question was visible; a stale hint must not select another pending request.
func WithSteeringQuestion(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, steeringQuestionKey{}, requestID)
}
func SteeringQuestion(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(steeringQuestionKey{}).(string)
	return id, ok
}

// StartHook persists or settles a controller-owned question before fan-out.
// Ordinary questions have no hook and preserve their existing behavior.
type StartHook func(context.Context, Request) error
type startHookKey struct{}

func WithStartHook(ctx context.Context, hook StartHook) context.Context {
	return context.WithValue(ctx, startHookKey{}, hook)
}
