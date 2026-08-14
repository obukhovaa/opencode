package flow

import (
	"context"
	"testing"

	"github.com/opencode-ai/opencode/internal/bridge"
	agentpkg "github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/pubsub"
)

// GENAI-170: interactive steps used to be exempt from the forcing wrap-up turn
// (`!step.Interactive` in the gate). A prose-only ending then completed as a
// non-error, the args merge was skipped, and routing rules evaluated on values
// inherited from an earlier step — so the reviewer's answers were discarded while
// the flow advanced as though the step had produced them. These tests pin the
// exemption's removal.

// stubOKHook binds and unbinds successfully, unlike nopInteractiveHook which
// fails fast with ErrInteractiveBridgeDisabled.
type stubOKHook struct {
	binds   int
	unbinds int
}

func (h *stubOKHook) OnInteractiveStepStart(context.Context, string, []bridge.PeerRef) error {
	h.binds++
	return nil
}

func (h *stubOKHook) OnInteractiveStepComplete(context.Context, string) error {
	h.unbinds++
	return nil
}

// stubInteractivePermissions covers the two calls an interactive step makes that
// stubPermissions does not implement (its embedded interface is nil, so calling
// them there would panic).
type stubInteractivePermissions struct {
	permission.Service
	marked  int
	removed int
}

func (p *stubInteractivePermissions) AutoApproveSession(_ string)       {}
func (p *stubInteractivePermissions) MarkInteractiveSession(_ string)   { p.marked++ }
func (p *stubInteractivePermissions) RemoveInteractiveSession(_ string) { p.removed++ }

// runInteractiveForceFlow runs a two-step flow: an autonomous step that emits
// struct_output, then an interactive step with its own schema. The second step's
// scripted responses decide what the forcing path sees.
func runInteractiveForceFlow(t *testing.T, flowID string, responses []agentpkg.AgentEvent) (*stubAgent, *stubOKHook, *FlowState) {
	t.Helper()

	first := Step{
		ID:     "autonomous",
		Prompt: "decide",
		Output: &StepOutput{Schema: map[string]any{"type": "object"}},
		Rules:  []Rule{{Then: "ask-reviewer"}},
	}
	second := Step{
		ID:          "ask-reviewer",
		Prompt:      "talk to the reviewer",
		Interactive: true,
		Interaction: &StepInteraction{Target: "${args.reviewer}"},
		Output:      &StepOutput{Schema: map[string]any{"type": "object"}},
	}
	registerTestFlow(t, Flow{
		ID:   flowID,
		Name: "Interactive Force Struct Output",
		Spec: FlowSpec{Steps: []Step{first, second}},
	})

	agent := &stubAgent{Broker: pubsub.NewBroker[agentpkg.AgentEvent](), responses: responses}
	hook := &stubOKHook{}
	svc := NewService(&stubSessions{}, nil, &stubQuerier{}, &stubInteractivePermissions{}, &stubAgentFactory{agent: agent})
	// SetInteractiveHook lives on the concrete *service, not on the Service
	// interface. Without a hook, nopInteractiveHook fails the bind fast.
	svc.(*service).SetInteractiveHook(hook)

	args := map[string]any{
		"reviewer": map[string]any{
			"channel":  "slack",
			"identity": "default",
			"peerId":   "D012345",
		},
	}
	agentEvents, flowStates, err := svc.Run(context.Background(), "prefix", flowID, args, true)
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
	return agent, hook, terminal
}

// An interactive schema step that ends in prose MUST get the forcing wrap-up
// turn, and its struct_output MUST become the step result. Before GENAI-170 the
// gate skipped interactive steps entirely, so the call count was 2 and the prose
// was accepted.
func TestForceStructOutput_InteractiveStepIsForced(t *testing.T) {
	agent, hook, terminal := runInteractiveForceFlow(t, "force-interactive-upgrade",
		[]agentpkg.AgentEvent{
			structEvent(`{"validated":true}`),               // autonomous step
			proseEvent("thanks, I'll write that up"),        // interactive step ends in prose
			structEvent(`{"validated":false,"asked":true}`), // forcing turn
		})

	if got := agent.callCount(); got != 3 {
		t.Fatalf("expected 3 agent calls (autonomous + interactive + forcing turn), got %d", got)
	}
	if terminal.Status != FlowStatusCompleted {
		t.Fatalf("terminal status = %q, want completed", terminal.Status)
	}
	if !containsSubstring(terminal.Output, `"asked":true`) {
		t.Fatalf("expected the forced struct_output as step output, got %q", terminal.Output)
	}
	// The forcing turn runs while the session is still bound — the unbind is
	// deferred to runStep's return — so exactly one bind/unbind pair, not one per
	// agent call.
	if hook.binds != 1 || hook.unbinds != 1 {
		t.Fatalf("bind/unbind = %d/%d, want 1/1", hook.binds, hook.unbinds)
	}
	if prompts := agent.snapshotPrompts(); len(prompts) != 3 || !containsSubstring(prompts[2], "struct_output") {
		t.Fatalf("expected the 3rd prompt to be the struct_output corrective prompt, got %+v", prompts)
	}
}

// When the forcing turn ALSO returns prose (a provider that ignores forced tool
// choice), the step degrades to its own prose rather than adopting the earlier
// step's struct_output as if it had produced it. This is the failure GENAI-170
// was about: `validated: true` came from the autonomous step, and the flow must
// not present it as the interactive step's result.
func TestForceStructOutput_InteractiveProseDoesNotAdoptEarlierStepOutput(t *testing.T) {
	agent, _, terminal := runInteractiveForceFlow(t, "force-interactive-stale",
		[]agentpkg.AgentEvent{
			structEvent(`{"validated":true}`),      // autonomous step
			proseEvent("reviewer asked for edits"), // interactive step ends in prose
			proseEvent("still prose"),              // forcing turn fails to structure it
		})

	if got := agent.callCount(); got != 3 {
		t.Fatalf("expected the forcing turn to be attempted on an interactive step (3 calls), got %d", got)
	}
	if terminal.Status != FlowStatusCompleted {
		t.Fatalf("terminal status = %q, want completed (graceful degradation, not a failure)", terminal.Status)
	}
	if !containsSubstring(terminal.Output, "reviewer asked for edits") {
		t.Fatalf("expected the step's own prose as output, got %q", terminal.Output)
	}
	if containsSubstring(terminal.Output, `"validated":true`) {
		t.Fatalf("step output carries the EARLIER step's struct_output — stale-args adoption, got %q", terminal.Output)
	}
}
