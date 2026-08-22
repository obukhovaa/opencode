package flow

import (
	"context"
	"errors"
	"testing"

	agentpkg "github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/pubsub"
)

// proseEvent is an agent turn that ended with text and no struct_output.
func proseEvent(text string) agentpkg.AgentEvent {
	return agentpkg.AgentEvent{
		Type: agentpkg.AgentEventTypeResponse,
		Message: message.Message{
			Role:  message.Assistant,
			Parts: []message.ContentPart{message.TextContent{Text: text}},
		},
	}
}

// structEvent is an agent turn that emitted a valid struct_output.
func structEvent(content string) agentpkg.AgentEvent {
	return agentpkg.AgentEvent{
		Type:         agentpkg.AgentEventTypeResponse,
		StructOutput: &message.ToolResult{Name: "struct_output", Content: content},
		Message: message.Message{
			Role:  message.Assistant,
			Parts: []message.ContentPart{message.TextContent{Text: content}},
		},
	}
}

func runForceFlow(t *testing.T, flowID string, output *StepOutput, responses []agentpkg.AgentEvent) (*stubAgent, *FlowState) {
	t.Helper()
	step := Step{
		ID:     "plan",
		Prompt: "produce a plan",
		Output: output,
	}
	testFlow := Flow{ID: flowID, Name: "Force Struct Output", Spec: FlowSpec{Steps: []Step{step}}}
	registerTestFlow(t, testFlow)

	agent := &stubAgent{Broker: pubsub.NewBroker[agentpkg.AgentEvent](), responses: responses}
	svc := NewService(&stubSessions{}, nil, &stubQuerier{}, &stubPermissions{}, &stubAgentFactory{agent: agent}, "")

	agentEvents, flowStates, err := svc.Run(context.Background(), "prefix", flowID, map[string]any{}, true)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	var terminal *FlowState
	for s := range flowStates {
		if s.Status == FlowStatusFailed || s.Status == FlowStatusCompleted {
			terminal = s
		}
	}
	for range agentEvents {
	}
	if terminal == nil {
		t.Fatal("expected a terminal flow state")
	}
	return agent, terminal
}

// TestForceStructOutput_UpgradesProseToStructOutput: a schema step whose agent
// ends in prose gets a forcing wrap-up turn; when that turn emits struct_output
// it becomes the step result (so routing sees the structured fields).
func TestForceStructOutput_UpgradesProseToStructOutput(t *testing.T) {
	agent, terminal := runForceFlow(t, "force-upgrade", &StepOutput{Schema: map[string]any{"type": "object"}},
		[]agentpkg.AgentEvent{proseEvent("here is my plan, in prose"), structEvent(`{"ok":true}`)})

	if got := agent.callCount(); got != 2 {
		t.Fatalf("expected 2 agent calls (agentic + forcing turn), got %d", got)
	}
	if terminal.Status != FlowStatusCompleted {
		t.Fatalf("terminal status = %q, want completed", terminal.Status)
	}
	if !containsSubstring(terminal.Output, `"ok":true`) {
		t.Fatalf("expected forced struct_output as step output, got %q", terminal.Output)
	}
	if prompts := agent.snapshotPrompts(); len(prompts) != 2 || !containsSubstring(prompts[1], "struct_output") {
		t.Fatalf("expected 2nd prompt to be the struct_output corrective prompt, got %+v", prompts)
	}
}

// TestForceStructOutput_GracefulFallbackWhenStillProse: if the forcing turn
// also returns prose (e.g. a provider that ignores forced tool choice), the
// step falls back to the original prose and does NOT fail.
func TestForceStructOutput_GracefulFallbackWhenStillProse(t *testing.T) {
	agent, terminal := runForceFlow(t, "force-fallback", &StepOutput{Schema: map[string]any{"type": "object"}},
		[]agentpkg.AgentEvent{proseEvent("original prose plan"), proseEvent("still prose, no struct_output")})

	if got := agent.callCount(); got != 2 {
		t.Fatalf("expected the forcing turn to be attempted (2 calls), got %d", got)
	}
	if terminal.Status != FlowStatusCompleted {
		t.Fatalf("terminal status = %q, want completed (must not fail on missing struct_output)", terminal.Status)
	}
	if !containsSubstring(terminal.Output, "original prose plan") {
		t.Fatalf("expected fallback to the original prose, got %q", terminal.Output)
	}
	if containsSubstring(terminal.Output, `"ok"`) {
		t.Fatalf("did not expect struct_output content in fallback, got %q", terminal.Output)
	}
}

// TestForceStructOutput_SkippedWhenNoSchema: a step whose output block carries
// no schema never gets the struct_output tool injected (agent tools.go gates
// injection on Output.Schema != nil), so the forcing turn must NOT fire —
// forcing tool_choice on an absent tool would 400 the request. Such a step
// behaves like a plain prose step: a single agentic turn, prose accepted.
func TestForceStructOutput_SkippedWhenNoSchema(t *testing.T) {
	agent, terminal := runForceFlow(t, "force-no-schema", &StepOutput{},
		[]agentpkg.AgentEvent{proseEvent("free prose summary")})

	if got := agent.callCount(); got != 1 {
		t.Fatalf("expected exactly 1 agent call (no forcing turn without a schema), got %d", got)
	}
	if terminal.Status != FlowStatusCompleted {
		t.Fatalf("terminal status = %q, want completed", terminal.Status)
	}
	if !containsSubstring(terminal.Output, "free prose summary") {
		t.Fatalf("expected prose output, got %q", terminal.Output)
	}
}

// ---- Decision 2: empty / errored last-ditch forcing ------------------------

// emptyEvent is an agent turn that produced neither struct_output nor any text.
func emptyEvent() agentpkg.AgentEvent {
	return agentpkg.AgentEvent{
		Type: agentpkg.AgentEventTypeResponse,
		Message: message.Message{
			Role:  message.Assistant,
			Parts: []message.ContentPart{message.TextContent{Text: ""}},
		},
	}
}

// errorEvent is an errored agent run.
func errorEvent(err error) agentpkg.AgentEvent {
	return agentpkg.AgentEvent{Type: agentpkg.AgentEventTypeError, Error: err}
}

// runForceFlowCtx is runForceFlow with an explicit parent ctx and it also
// returns the last AgentEvent emitted on the agentEvents channel (to assert the
// rescued event is a fresh Response, not the pending error event).
func runForceFlowCtx(t *testing.T, ctx context.Context, flowID string, output *StepOutput, responses []agentpkg.AgentEvent) (*stubAgent, *FlowState, *agentpkg.AgentEvent) {
	t.Helper()
	step := Step{ID: "plan", Prompt: "produce a plan", Output: output}
	testFlow := Flow{ID: flowID, Name: "Force Struct Output", Spec: FlowSpec{Steps: []Step{step}}}
	registerTestFlow(t, testFlow)

	agent := &stubAgent{Broker: pubsub.NewBroker[agentpkg.AgentEvent](), responses: responses}
	svc := NewService(&stubSessions{}, nil, &stubQuerier{}, &stubPermissions{}, &stubAgentFactory{agent: agent}, "")

	agentEvents, flowStates, err := svc.Run(ctx, "prefix", flowID, map[string]any{}, true)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	var terminal *FlowState
	for s := range flowStates {
		if s.Status == FlowStatusFailed || s.Status == FlowStatusCompleted {
			terminal = s
		}
	}
	var lastEvent *agentpkg.AgentEvent
	for e := range agentEvents {
		ev := e
		lastEvent = &ev
	}
	if terminal == nil {
		t.Fatal("expected a terminal flow state")
	}
	return agent, terminal, lastEvent
}

// TestForceStructOutput_EmptyResponseRescuedByForcingTurn: a schema step whose
// agent returns an empty response (no struct_output, no text) gets the
// last-ditch forcing turn before failing; when that turn emits struct_output
// the step completes. Supersedes the old "empty stays a retryable failure".
func TestForceStructOutput_EmptyResponseRescuedByForcingTurn(t *testing.T) {
	agent, terminal := runForceFlow(t, "force-empty", &StepOutput{Schema: map[string]any{"type": "object"}},
		[]agentpkg.AgentEvent{emptyEvent(), structEvent(`{"ok":true}`)})

	if got := agent.callCount(); got != 2 {
		t.Fatalf("expected 2 agent calls (empty run + forcing turn), got %d", got)
	}
	if terminal.Status != FlowStatusCompleted {
		t.Fatalf("terminal status = %q, want completed (empty response must be rescued)", terminal.Status)
	}
	if !containsSubstring(terminal.Output, `"ok":true`) {
		t.Fatalf("expected forced struct_output as step output, got %q", terminal.Output)
	}
	// The rescue must actually FORCE struct_output on the 2nd turn (not just a
	// plain retry), under a bounded ctx.
	if opts := agent.snapshotRunOpts(); len(opts) != 2 || opts[0].ForceStructOutput || !opts[1].ForceStructOutput {
		t.Fatalf("expected initial turn unforced and forcing turn forced, got %+v", opts)
	}
	if dl := agent.snapshotCtxDeadline(); len(dl) != 2 || !dl[1] {
		t.Fatalf("expected the forcing turn under a bounded (deadline) ctx, got %v", dl)
	}
}

// TestForceStructOutput_ErroredRunRescuedWithFreshResponseEvent: a schema step
// whose agent run ERRORS (parent ctx alive, non-transient) gets the last-ditch
// forcing turn; on success the step completes AND the emitted AgentEvent is a
// fresh Response (not the pending error event).
func TestForceStructOutput_ErroredRunRescuedWithFreshResponseEvent(t *testing.T) {
	agent, terminal, lastEvent := runForceFlowCtx(t, context.Background(), "force-errored",
		&StepOutput{Schema: map[string]any{"type": "object"}},
		[]agentpkg.AgentEvent{errorEvent(errors.New("failed to process events: boom")), structEvent(`{"ok":true}`)})

	if got := agent.callCount(); got != 2 {
		t.Fatalf("expected 2 agent calls (errored run + forcing turn), got %d", got)
	}
	if terminal.Status != FlowStatusCompleted {
		t.Fatalf("terminal status = %q, want completed (errored run must be rescued)", terminal.Status)
	}
	if !containsSubstring(terminal.Output, `"ok":true`) {
		t.Fatalf("expected forced struct_output as step output, got %q", terminal.Output)
	}
	if lastEvent == nil {
		t.Fatal("expected an AgentEvent to be emitted")
	}
	if lastEvent.Type != agentpkg.AgentEventTypeResponse {
		t.Errorf("emitted event type = %v, want Response (a completed step must not carry the error event)", lastEvent.Type)
	}
	if lastEvent.Error != nil {
		t.Errorf("emitted event still carries Error: %v", lastEvent.Error)
	}
	if opts := agent.snapshotRunOpts(); len(opts) != 2 || opts[0].ForceStructOutput || !opts[1].ForceStructOutput {
		t.Fatalf("expected initial turn unforced and forcing turn forced, got %+v", opts)
	}
	if dl := agent.snapshotCtxDeadline(); len(dl) != 2 || !dl[1] {
		t.Fatalf("expected the forcing turn under a bounded (deadline) ctx, got %v", dl)
	}
}

// TestForceStructOutput_TransientErrorSkipsForcingTurn: a transient provider
// error must NOT trigger a forcing turn (it is left to the failure/postpone
// path); the step fails without consuming the scripted struct_output.
func TestForceStructOutput_TransientErrorSkipsForcingTurn(t *testing.T) {
	agent, terminal := runForceFlow(t, "force-transient", &StepOutput{Schema: map[string]any{"type": "object"}},
		[]agentpkg.AgentEvent{errorEvent(errors.New("HTTP 429: too many requests")), structEvent(`{"ok":true}`)})

	if got := agent.callCount(); got != 1 {
		t.Fatalf("transient provider error must not trigger a forcing turn (1 call), got %d", got)
	}
	if terminal.Status != FlowStatusFailed {
		t.Fatalf("terminal status = %q, want failed", terminal.Status)
	}
}

// TestForceStructOutput_ParentCtxCancelledSkipsForcingTurn: when the parent ctx
// is already cancelled the forcing turn cannot run, so no forcing turn is
// attempted and the step fails.
func TestForceStructOutput_ParentCtxCancelledSkipsForcingTurn(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	agent, terminal, _ := runForceFlowCtx(t, ctx, "force-ctx-cancelled",
		&StepOutput{Schema: map[string]any{"type": "object"}},
		[]agentpkg.AgentEvent{errorEvent(errors.New("failed to process events: boom")), structEvent(`{"ok":true}`)})

	if got := agent.callCount(); got != 1 {
		t.Fatalf("expected exactly the initial turn to run and the forcing turn suppressed, got %d calls", got)
	}
	if terminal.Status != FlowStatusFailed {
		t.Fatalf("terminal status = %q, want failed", terminal.Status)
	}
}
