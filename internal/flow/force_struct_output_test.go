package flow

import (
	"context"
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

func runForceFlow(t *testing.T, flowID string, responses []agentpkg.AgentEvent) (*stubAgent, *FlowState) {
	t.Helper()
	step := Step{
		ID:     "plan",
		Prompt: "produce a plan",
		Output: &StepOutput{Schema: map[string]any{"type": "object"}},
	}
	testFlow := Flow{ID: flowID, Name: "Force Struct Output", Spec: FlowSpec{Steps: []Step{step}}}
	registerTestFlow(t, testFlow)

	agent := &stubAgent{Broker: pubsub.NewBroker[agentpkg.AgentEvent](), responses: responses}
	svc := NewService(&stubSessions{}, nil, &stubQuerier{}, &stubPermissions{}, &stubAgentFactory{agent: agent})

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
	agent, terminal := runForceFlow(t, "force-upgrade",
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
	agent, terminal := runForceFlow(t, "force-fallback",
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
