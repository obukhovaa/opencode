package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/llm/provider"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/pubsub"
	"github.com/opencode-ai/opencode/internal/session"
	"github.com/opencode-ai/opencode/internal/task"
)

// ---- in-memory fakes -------------------------------------------------------

// memMessages is a minimal in-memory message store: just enough for the
// agentic loop (Create / Update / List / PublishPart). Unused Service
// methods panic via the embedded nil interface, which is the point — a
// test reaching them means the loop grew a new dependency.
type memMessages struct {
	message.Service
	mu        sync.Mutex
	seq       int
	byID      map[string]message.Message
	bySession map[string][]string
}

func newMemMessages() *memMessages {
	return &memMessages{byID: map[string]message.Message{}, bySession: map[string][]string{}}
}

func (m *memMessages) Create(_ context.Context, sessionID string, params message.CreateMessageParams) (message.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq++
	msg := message.Message{
		ID:        fmt.Sprintf("m-%d", m.seq),
		SessionID: sessionID,
		Role:      params.Role,
		Parts:     params.Parts,
		Model:     params.Model,
		Seq:       int64(m.seq),
	}
	m.byID[msg.ID] = msg
	m.bySession[sessionID] = append(m.bySession[sessionID], msg.ID)
	return msg, nil
}

func (m *memMessages) Update(_ context.Context, msg message.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.byID[msg.ID] = msg
	return nil
}

func (m *memMessages) List(_ context.Context, sessionID string) ([]message.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := m.bySession[sessionID]
	out := make([]message.Message, 0, len(ids))
	for _, id := range ids {
		out = append(out, m.byID[id])
	}
	return out, nil
}

func (m *memMessages) PublishPart(string, string, message.ContentPart) {}

type memSessions struct {
	session.Service
	mu       sync.Mutex
	sessions map[string]session.Session
}

func (s *memSessions) Get(_ context.Context, id string) (session.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessions == nil {
		s.sessions = map[string]session.Session{}
	}
	if sess, ok := s.sessions[id]; ok {
		return sess, nil
	}
	sess := session.Session{ID: id}
	s.sessions[id] = sess
	return sess, nil
}

func (s *memSessions) Save(_ context.Context, sess session.Session) (session.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessions == nil {
		s.sessions = map[string]session.Session{}
	}
	s.sessions[sess.ID] = sess
	return sess, nil
}

// scriptedProvider returns one EventComplete per StreamResponse call, with
// the response chosen by call number. onCall fires before the events are
// emitted — tests use it to flip external state (e.g. finish a background
// task) at a deterministic point.
type scriptedProvider struct {
	mu      sync.Mutex
	calls   int
	respond func(call int) *provider.ProviderResponse
	onCall  func(call int)
}

func (p *scriptedProvider) StreamResponse(_ context.Context, _ []message.Message, _ []tools.BaseTool) <-chan provider.ProviderEvent {
	p.mu.Lock()
	p.calls++
	n := p.calls
	p.mu.Unlock()
	if p.onCall != nil {
		p.onCall(n)
	}
	ch := make(chan provider.ProviderEvent, 1)
	ch <- provider.ProviderEvent{Type: provider.EventComplete, Response: p.respond(n)}
	close(ch)
	return ch
}

func (p *scriptedProvider) SendMessages(context.Context, []message.Message, []tools.BaseTool) (*provider.ProviderResponse, error) {
	return nil, fmt.Errorf("scriptedProvider: unexpected SendMessages call")
}

func (p *scriptedProvider) Model() models.Model {
	return models.Model{ID: "scripted-model", ContextWindow: 200_000}
}

func (p *scriptedProvider) CountTokens(context.Context, float64, []message.Message, []tools.BaseTool) (int64, bool) {
	return 100, false
}

func (p *scriptedProvider) AdjustMaxTokens(estimated int64) int64 { return estimated }

func (p *scriptedProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// ---- harness ---------------------------------------------------------------

func structOutputTurn() *provider.ProviderResponse {
	return &provider.ProviderResponse{
		ToolCalls: []message.ToolCall{{
			ID:       "call-struct-1",
			Name:     tools.StructOutputToolName,
			Input:    `{"status":"done"}`,
			Finished: true,
		}},
		FinishReason: message.FinishReasonToolUse,
	}
}

func endTurn() *provider.ProviderResponse {
	return &provider.ProviderResponse{
		Content:      "all wrapped up",
		FinishReason: message.FinishReasonEndTurn,
	}
}

func newLoopAgent(t *testing.T, p provider.Provider) *agent {
	t.Helper()
	if config.Get() == nil {
		if _, err := config.Load(t.TempDir(), false); err != nil {
			t.Fatalf("config.Load: %v", err)
		}
	}
	toolsCh := make(chan tools.BaseTool, 1)
	toolsCh <- tools.NewStructOutputTool(map[string]any{
		"type":       "object",
		"properties": map[string]any{"status": map[string]any{"type": "string"}},
		"required":   []any{"status"},
	})
	close(toolsCh)
	return &agent{
		Broker:   pubsub.NewBroker[AgentEvent](),
		sessions: &memSessions{},
		messages: newMemMessages(),
		agentID:  config.AgentName("coder"),
		toolsCh:  toolsCh,
		provider: p,
	}
}

func withFreshTaskRegistry(t *testing.T) task.Registry {
	t.Helper()
	dir := t.TempDir()
	reg := task.NewRegistry(func() string { return dir })
	task.ResetGlobalRegistry()
	task.SetGlobalRegistry(reg)
	t.Cleanup(task.ResetGlobalRegistry)
	return reg
}

// ---- loop-level tests ------------------------------------------------------

// An accepted struct_output must finish the run right there: exactly ONE
// provider call, no wrap-up turn. Regression guard for the stranded-step
// incident where the post-struct_output wrap-up request hung in provider
// retries until the job deadline killed a fully-completed step.
func TestProcessGeneration_FinishesOnAcceptedStructOutputWithoutWrapUpTurn(t *testing.T) {
	withFreshTaskRegistry(t)
	p := &scriptedProvider{respond: func(call int) *provider.ProviderResponse {
		if call == 1 {
			return structOutputTurn()
		}
		// Reached only if the finish-on-struct_output short circuit regresses.
		return endTurn()
	}}
	a := newLoopAgent(t, p)

	res := a.processGeneration(context.Background(), "sess-finish", "produce the output", 0, nil, RunOptions{NonInteractive: true})

	if res.Error != nil {
		t.Fatalf("processGeneration error: %v", res.Error)
	}
	if res.StructOutput == nil {
		t.Fatal("StructOutput is nil — accepted struct_output was not captured")
	}
	if res.StructOutput.IsError {
		t.Fatalf("StructOutput.IsError = true, content: %s", res.StructOutput.Content)
	}
	if !strings.Contains(res.StructOutput.Content, `"status"`) {
		t.Errorf("StructOutput content = %q, want the echoed JSON", res.StructOutput.Content)
	}
	if !res.Done {
		t.Error("AgentEvent.Done = false, want true")
	}
	if got := p.callCount(); got != 1 {
		t.Errorf("provider StreamResponse calls = %d, want 1 — the wrap-up turn must be skipped after an accepted struct_output", got)
	}
}

// With a pending background task the finish must be DEFERRED: the loop grants
// the model its wrap-up turn (second provider call) so the outer wait can
// drain the task before the run returns — completions must not be enqueued
// onto a finished session. The struct_output captured on turn one still
// travels out on the final event.
func TestProcessGeneration_PendingTaskDefersFinishUntilWrapUp(t *testing.T) {
	reg := withFreshTaskRegistry(t)
	const sess = "sess-defer"

	taskID := task.NewTaskID(task.KindTask)
	if err := reg.Register(&task.Task{ID: taskID, SessionID: sess, Kind: task.KindTask}); err != nil {
		t.Fatalf("register task: %v", err)
	}

	p := &scriptedProvider{}
	p.respond = func(call int) *provider.ProviderResponse {
		if call == 1 {
			return structOutputTurn()
		}
		return endTurn()
	}
	// Finish the task as the wrap-up turn starts, so the outer wait finds a
	// drained session and the run ends after exactly two provider calls.
	p.onCall = func(call int) {
		if call == 2 {
			reg.MarkFinished(taskID, task.StateCompleted, nil)
		}
	}
	a := newLoopAgent(t, p)

	res := a.processGeneration(context.Background(), sess, "produce the output", 0, nil, RunOptions{NonInteractive: true})

	if res.Error != nil {
		t.Fatalf("processGeneration error: %v", res.Error)
	}
	if got := p.callCount(); got != 2 {
		t.Errorf("provider StreamResponse calls = %d, want 2 — a pending task must defer the finish into the wrap-up turn", got)
	}
	if res.StructOutput == nil || res.StructOutput.IsError {
		t.Fatalf("StructOutput = %+v, want the turn-one accepted result to survive the deferred finish", res.StructOutput)
	}
	if remaining := reg.PendingForSession(sess, nil); len(remaining) != 0 {
		t.Errorf("%d task(s) still pending after run returned", len(remaining))
	}
}
