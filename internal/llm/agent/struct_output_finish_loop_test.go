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
	respond func(call int, forced bool) *provider.ProviderResponse
	onCall  func(call int)
	forced  []bool // per-call: was struct_output forced on this request's ctx
}

func (p *scriptedProvider) StreamResponse(ctx context.Context, _ []message.Message, _ []tools.BaseTool) <-chan provider.ProviderEvent {
	isForced := provider.ForcedTool(ctx) == tools.StructOutputToolName
	p.mu.Lock()
	p.calls++
	n := p.calls
	p.forced = append(p.forced, isForced)
	p.mu.Unlock()
	if p.onCall != nil {
		p.onCall(n)
	}
	ch := make(chan provider.ProviderEvent, 1)
	ch <- provider.ProviderEvent{Type: provider.EventComplete, Response: p.respond(n, isForced)}
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
	p := &scriptedProvider{respond: func(call int, _ bool) *provider.ProviderResponse {
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
	p.respond = func(call int, _ bool) *provider.ProviderResponse {
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

// ---- max-turns forcing (Decision 1) ----------------------------------------

// testStructOutputTool mirrors the struct_output tool newLoopAgent injects:
// an object with a required string `status`.
func testStructOutputTool() tools.BaseTool {
	return tools.NewStructOutputTool(map[string]any{
		"type":       "object",
		"properties": map[string]any{"status": map[string]any{"type": "string"}},
		"required":   []any{"status"},
	})
}

// structOutputCall is a single struct_output tool-call turn with the given
// input; the id varies per call so tool_result matching is unambiguous.
func structOutputCall(id, input string) *provider.ProviderResponse {
	return &provider.ProviderResponse{
		ToolCalls: []message.ToolCall{{
			ID:       id,
			Name:     tools.StructOutputToolName,
			Input:    input,
			Finished: true,
		}},
		FinishReason: message.FinishReasonToolUse,
	}
}

// noopTool is a harmless registered tool used to keep the plain-run loop
// iterating toward max turns without side effects.
type noopTool struct{}

func (noopTool) Info() tools.ToolInfo { return tools.ToolInfo{Name: "noop", Description: "no-op"} }
func (noopTool) Run(context.Context, tools.ToolCall) (tools.ToolResponse, error) {
	return tools.NewTextResponse("ok"), nil
}
func (noopTool) AllowParallelism(tools.ToolCall, []tools.ToolCall) bool { return true }
func (noopTool) IsBaseline() bool                                       { return true }

func newLoopAgentWithTools(t *testing.T, p provider.Provider, ts []tools.BaseTool) *agent {
	t.Helper()
	if config.Get() == nil {
		if _, err := config.Load(t.TempDir(), false); err != nil {
			t.Fatalf("config.Load: %v", err)
		}
	}
	toolsCh := make(chan tools.BaseTool, len(ts))
	for _, tl := range ts {
		toolsCh <- tl
	}
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

// A schema-bearing run (struct_output tool present) that never emits a valid
// struct_output on its own MUST, at max turns, get a FORCED struct_output
// wrap-up turn whose result is CAPTURED — not a prose request whose tool call
// is discarded. Regression guard for the GENAI-134 max-turns-discard incident.
func TestProcessGeneration_MaxTurnsForcesStructOutputForSchemaStep(t *testing.T) {
	withFreshTaskRegistry(t)
	p := &scriptedProvider{respond: func(call int, forced bool) *provider.ProviderResponse {
		if forced {
			// The forced max-turns wrap-up: emit a valid struct_output.
			return structOutputCall(fmt.Sprintf("call-%d", call), `{"status":"done"}`)
		}
		// Normal turns: an invalid struct_output (missing required `status`)
		// is schema-rejected, so the loop keeps going toward max turns without
		// finishing early.
		return structOutputCall(fmt.Sprintf("call-%d", call), `{}`)
	}}
	a := newLoopAgentWithTools(t, p, []tools.BaseTool{testStructOutputTool()})

	res := a.processGeneration(context.Background(), "sess-maxturns-force", "do work", 1, nil, RunOptions{NonInteractive: true})

	if res.Error != nil {
		t.Fatalf("processGeneration error: %v", res.Error)
	}
	if res.StructOutput == nil || res.StructOutput.IsError {
		t.Fatalf("StructOutput = %+v, want the forced struct_output captured at max turns", res.StructOutput)
	}
	if !strings.Contains(res.StructOutput.Content, `"status"`) {
		t.Errorf("StructOutput content = %q, want the forced JSON", res.StructOutput.Content)
	}
	if n := len(p.forced); n == 0 || !p.forced[n-1] {
		t.Errorf("forced per-call = %v, want the final (max-turns wrap-up) call to be forced", p.forced)
	}
	if len(p.forced) > 0 && p.forced[0] {
		t.Errorf("the first normal turn must NOT be forced; forced=%v", p.forced)
	}
}

// Graceful degradation: if the forced wrap-up turn returns text and no tool
// call (a provider that ignores forced tool_choice), the run MUST NOT panic
// (a nil tool-results message) and MUST NOT hard-fail — it returns without a
// usable struct_output and lets the flow layer's own guard retry. Regression
// guard for the nil-`finalToolResults` capture panic.
func TestProcessGeneration_MaxTurnsForcedWrapUpReturningTextDoesNotPanic(t *testing.T) {
	withFreshTaskRegistry(t)
	p := &scriptedProvider{respond: func(call int, forced bool) *provider.ProviderResponse {
		if forced {
			return endTurn() // provider ignores forcing → plain text, no tool call
		}
		return structOutputCall(fmt.Sprintf("call-%d", call), `{}`) // invalid → loop
	}}
	a := newLoopAgentWithTools(t, p, []tools.BaseTool{testStructOutputTool()})

	res := a.processGeneration(context.Background(), "sess-maxturns-graceful", "do work", 1, nil, RunOptions{NonInteractive: true})

	if res.Error != nil {
		t.Fatalf("processGeneration error: %v — a nil finalToolResults on the forced turn must not panic/hard-fail", res.Error)
	}
	if res.StructOutput != nil && !res.StructOutput.IsError {
		t.Errorf("StructOutput = %+v, want none usable (forced turn produced no tool call)", res.StructOutput)
	}
	if n := len(p.forced); n == 0 || !p.forced[n-1] {
		t.Errorf("forced per-call = %v, want the wrap-up to have been forced", p.forced)
	}
}

// A plain run (no struct_output tool) at max turns keeps the existing behavior:
// a free-text wrap-up (never forced), with any stray tool call discarded.
func TestProcessGeneration_MaxTurnsPlainStepReturnsTextNotForced(t *testing.T) {
	withFreshTaskRegistry(t)
	p := &scriptedProvider{respond: func(call int, _ bool) *provider.ProviderResponse {
		if call == 1 {
			return &provider.ProviderResponse{
				ToolCalls:    []message.ToolCall{{ID: "call-noop", Name: "noop", Input: "{}", Finished: true}},
				FinishReason: message.FinishReasonToolUse,
			}
		}
		return endTurn()
	}}
	a := newLoopAgentWithTools(t, p, []tools.BaseTool{noopTool{}})

	res := a.processGeneration(context.Background(), "sess-maxturns-plain", "do work", 1, nil, RunOptions{NonInteractive: true})

	if res.Error != nil {
		t.Fatalf("processGeneration error: %v", res.Error)
	}
	if res.StructOutput != nil {
		t.Errorf("plain step must not produce struct_output, got %+v", res.StructOutput)
	}
	for i, f := range p.forced {
		if f {
			t.Fatalf("plain step must never force struct_output; forced[%d]=true (%v)", i, p.forced)
		}
	}
}

// If the FORCED max-turns wrap-up turn itself emits a schema-rejected
// struct_output, that error result must NOT be promoted as the run's output
// (the !structOutputIsErr guard) — the run ends with no usable StructOutput and
// the flow layer decides what to do, rather than persisting a bad result.
func TestProcessGeneration_MaxTurnsForcedSchemaRejectedNotPromoted(t *testing.T) {
	withFreshTaskRegistry(t)
	p := &scriptedProvider{respond: func(call int, _ bool) *provider.ProviderResponse {
		// Every turn (including the forced wrap-up) emits an invalid payload.
		return structOutputCall(fmt.Sprintf("call-%d", call), `{}`)
	}}
	a := newLoopAgentWithTools(t, p, []tools.BaseTool{testStructOutputTool()})

	res := a.processGeneration(context.Background(), "sess-maxturns-rejected", "do work", 1, nil, RunOptions{NonInteractive: true})

	if res.Error != nil {
		t.Fatalf("processGeneration error: %v", res.Error)
	}
	if res.StructOutput != nil {
		t.Errorf("a schema-rejected forced struct_output must NOT be promoted, got %+v", res.StructOutput)
	}
	if n := len(p.forced); n == 0 || !p.forced[n-1] {
		t.Errorf("forced per-call = %v, want the wrap-up to have been forced", p.forced)
	}
}

// TestProcessGeneration_TurnsExhaustedFlag: the flow runner keys its
// "is a struct_output re-prompt worth a request" decision off
// AgentEvent.TurnsExhausted, so the flag must be set when — and only when — a
// turn-budget gate ended the run, not when the model chose to end its turn.
func TestProcessGeneration_TurnsExhaustedFlag(t *testing.T) {
	t.Run("max turns exhaustion sets the flag", func(t *testing.T) {
		withFreshTaskRegistry(t)
		// Every normal turn emits a schema-rejected struct_output so the loop
		// never finishes early and walks into the max-turns wrap-up.
		p := &scriptedProvider{respond: func(call int, forced bool) *provider.ProviderResponse {
			if forced {
				return endTurn()
			}
			return structOutputCall(fmt.Sprintf("call-%d", call), `{}`)
		}}
		a := newLoopAgentWithTools(t, p, []tools.BaseTool{testStructOutputTool()})

		res := a.processGeneration(context.Background(), "sess-exhausted", "do work", 1, nil, RunOptions{NonInteractive: true})

		if res.Error != nil {
			t.Fatalf("processGeneration error: %v", res.Error)
		}
		if !res.TurnsExhausted {
			t.Error("TurnsExhausted = false, want true — the run ended on the max-turns gate")
		}
	})

	t.Run("model-chosen end of turn leaves the flag clear", func(t *testing.T) {
		withFreshTaskRegistry(t)
		// First turn ends the turn on its own, well inside the budget.
		p := &scriptedProvider{respond: func(int, bool) *provider.ProviderResponse { return endTurn() }}
		a := newLoopAgentWithTools(t, p, []tools.BaseTool{testStructOutputTool()})

		res := a.processGeneration(context.Background(), "sess-not-exhausted", "do work", 10, nil, RunOptions{NonInteractive: true})

		if res.Error != nil {
			t.Fatalf("processGeneration error: %v", res.Error)
		}
		if res.TurnsExhausted {
			t.Error("TurnsExhausted = true, want false — the model ended its own turn within budget")
		}
	})
}
