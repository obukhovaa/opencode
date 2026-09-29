package flow

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/opencode-ai/opencode/internal/bridge"
	agentpkg "github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/pubsub"
)

// markerProbeHook records whether the session carried the interactive marker
// at the moment each bind hook ran. A pool pod's bridge refuses inbound for a
// bound-but-unmarked session, so the marker must already be set when the bind
// makes the orchestrator start forwarding, and must still be set while the
// unbind stops it.
type markerProbeHook struct {
	perms   *interactivePermissions
	bindErr error

	mu               sync.Mutex
	markedAtStart    []bool
	markedAtComplete []bool
}

func (h *markerProbeHook) OnInteractiveStepStart(_ context.Context, sessionID string, _ []bridge.PeerRef) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.markedAtStart = append(h.markedAtStart, h.perms.IsInteractiveSession(sessionID))
	return h.bindErr
}

func (h *markerProbeHook) OnInteractiveStepComplete(_ context.Context, sessionID string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.markedAtComplete = append(h.markedAtComplete, h.perms.IsInteractiveSession(sessionID))
	return nil
}

func runMarkerProbeFlow(t *testing.T, flowID string, bindErr error) (*markerProbeHook, *interactivePermissions, *FlowState) {
	t.Helper()
	registerTestFlow(t, Flow{ID: flowID, Name: "Interactive", Spec: FlowSpec{Steps: []Step{interactiveStep("products", 20)}}})

	base := &stubAgent{Broker: pubsub.NewBroker[agentpkg.AgentEvent](), responses: []agentpkg.AgentEvent{
		structEvent(`{"products":["a"],"confirmed":true}`),
	}}
	perms := newInteractivePermissions()
	hook := &markerProbeHook{perms: perms, bindErr: bindErr}

	svc := NewService(&stubSessions{}, &stubMessages{}, &stubQuerier{}, perms, &scriptedAgentFactory{agent: &scriptedAgent{stubAgent: base}})
	svc.(*service).SetInteractiveHook(hook)

	agentEvents, flowStates, err := svc.Run(context.Background(), "prefix", flowID, copyArgs(reviewerArgs), true)
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
	return hook, perms, terminal
}

func TestInteractiveStep_MarkerSpansBindAndUnbind(t *testing.T) {
	hook, perms, terminal := runMarkerProbeFlow(t, "marker-order", nil)

	if terminal.Status != FlowStatusCompleted {
		t.Fatalf("terminal status = %q, want completed", terminal.Status)
	}
	if len(hook.markedAtStart) != 1 || !hook.markedAtStart[0] {
		t.Errorf("marker at OnInteractiveStepStart = %v, want [true] (mark must precede bind)", hook.markedAtStart)
	}
	if len(hook.markedAtComplete) != 1 || !hook.markedAtComplete[0] {
		t.Errorf("marker at OnInteractiveStepComplete = %v, want [true] (unbind must precede unmark)", hook.markedAtComplete)
	}
	if perms.IsInteractiveSession(terminal.SessionID) {
		t.Errorf("session %q still marked interactive after the step", terminal.SessionID)
	}
}

func TestInteractiveStep_BindFailureClearsMarker(t *testing.T) {
	hook, perms, terminal := runMarkerProbeFlow(t, "marker-bind-fail", errors.New("slack: channel_not_found"))

	if terminal.Status != FlowStatusFailed {
		t.Fatalf("terminal status = %q, want failed", terminal.Status)
	}
	if len(hook.markedAtStart) != 1 || !hook.markedAtStart[0] {
		t.Errorf("marker at OnInteractiveStepStart = %v, want [true]", hook.markedAtStart)
	}
	perms.mu.Lock()
	defer perms.mu.Unlock()
	for id, marked := range perms.interactive {
		if marked {
			t.Errorf("session %q still marked interactive after a failed bind", id)
		}
	}
}
