package flow

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/contextfile"
	agentpkg "github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/pubsub"
)

// blockingAgent stands in for an agent whose LLM call is in flight: RunWith
// blocks until its ctx is cancelled and then reports the cancellation, as the
// real agent does.
type blockingAgent struct {
	*stubAgent
	started chan struct{}
}

func (a *blockingAgent) RunWith(ctx context.Context, sessionID string, prompt string, maxTurns int, opts agentpkg.RunOptions, atts ...message.Attachment) (<-chan agentpkg.AgentEvent, error) {
	a.mu.Lock()
	a.calls++
	a.sessionIDs = append(a.sessionIDs, sessionID)
	a.mu.Unlock()
	ch := make(chan agentpkg.AgentEvent, 1)
	go func() {
		defer close(ch)
		select {
		case a.started <- struct{}{}:
		default:
		}
		<-ctx.Done()
		ch <- agentpkg.AgentEvent{Type: agentpkg.AgentEventTypeError, Error: ctx.Err()}
	}()
	return ch, nil
}

func (a *blockingAgent) Run(ctx context.Context, sessionID string, prompt string, maxTurns int, atts ...message.Attachment) (<-chan agentpkg.AgentEvent, error) {
	return a.RunWith(ctx, sessionID, prompt, maxTurns, agentpkg.RunOptions{}, atts...)
}

// blockingAgentFactory hands every step the blockingAgent.
type blockingAgentFactory struct {
	*stubAgentFactory
	agent *blockingAgent
}

func (f *blockingAgentFactory) NewAgent(_ context.Context, _ string, _ map[string]any, _ string, _ bool, _ []bridge.PeerRef, _ *contextfile.StepContext, _ contextfile.TemplateVars, _ agentpkg.ModelOverride) (agentpkg.Service, error) {
	return f.agent, nil
}

// interruptStep runs a one-step flow whose agent is mid-call, cancels it with
// cause, and returns the states the run emitted plus the querier holding the
// persisted rows.
func interruptStep(t *testing.T, flowID string, cause error) ([]*FlowState, []agentpkg.AgentEvent, *stubQuerier, *stubSessions) {
	t.Helper()
	registerTestFlow(t, Flow{
		ID:   flowID,
		Name: flowID,
		Spec: FlowSpec{Steps: []Step{{ID: "step-one", Prompt: "do something"}}},
	})
	agent := &blockingAgent{stubAgent: &stubAgent{Broker: pubsub.NewBroker[agentpkg.AgentEvent]()}, started: make(chan struct{}, 1)}
	q := &stubQuerier{}
	sessions := &stubSessions{}
	svc := NewService(sessions, nil, q, &stubPermissions{}, &blockingAgentFactory{stubAgentFactory: &stubAgentFactory{}, agent: agent})

	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	agentEvents, flowStates, err := svc.RunWithOptions(ctx, "prefix", flowID, map[string]any{"k": "v"}, RunOptions{Fresh: true})
	if err != nil {
		t.Fatalf("RunWithOptions() error: %v", err)
	}
	select {
	case <-agent.started:
	case <-time.After(5 * time.Second):
		t.Fatal("agent never started")
	}
	cancel(cause)

	var states []*FlowState
	var events []agentpkg.AgentEvent
	done := make(chan struct{})
	go func() {
		for ev := range agentEvents {
			events = append(events, ev)
		}
		close(done)
	}()
	for st := range flowStates {
		states = append(states, st)
	}
	<-done
	return states, events, q, sessions
}

func rowStatus(q *stubQuerier, stepID string) string {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, fs := range q.flowStates {
		if fs.StepID == stepID {
			return fs.Status
		}
	}
	return ""
}

// TestShutdownInterruptLeavesStepRunning (GENAI-709): a step cut off by
// process shutdown must stay `running` with no failed state or error event —
// killed-step recovery resumes only running rows, and the orchestrator's view
// drops the step on a failed event. A plain cancellation (DELETE /flow) still
// fails the step.
func TestShutdownInterruptLeavesStepRunning(t *testing.T) {
	tests := []struct {
		name       string
		cause      error
		wantStatus string
		wantFailed bool
	}{
		{name: "process shutdown", cause: ErrProcessShutdown, wantStatus: string(FlowStatusRunning), wantFailed: false},
		{name: "wrapped process shutdown", cause: errors.Join(errors.New("sigterm"), ErrProcessShutdown), wantStatus: string(FlowStatusRunning), wantFailed: false},
		{name: "abort", cause: context.Canceled, wantStatus: string(FlowStatusFailed), wantFailed: true},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			states, events, q, _ := interruptStep(t, "test-interrupt-"+string(rune('a'+i)), tt.cause)
			if got := rowStatus(q, "step-one"); got != tt.wantStatus {
				t.Fatalf("persisted step-one status = %q, want %q", got, tt.wantStatus)
			}
			failed := false
			for _, st := range states {
				if st.Status == FlowStatusFailed {
					failed = true
				}
			}
			if failed != tt.wantFailed {
				t.Errorf("failed state emitted = %v, want %v (states %v)", failed, tt.wantFailed, statusesOf(states))
			}
			errEvent := false
			for _, ev := range events {
				if ev.Type == agentpkg.AgentEventTypeError {
					errEvent = true
				}
			}
			if !tt.wantFailed && errEvent {
				t.Error("an interrupted step must emit no error agent event")
			}
		})
	}
}

// TestShutdownInterruptedStepResumesWithRecoverRunning: the continuation the
// orchestrator starts after the kill (--flow-recover-running) re-enters the
// very row the interrupted process left running, in its own session.
func TestShutdownInterruptedStepResumesWithRecoverRunning(t *testing.T) {
	_, _, q, sessions := interruptStep(t, "test-interrupt-recover", ErrProcessShutdown)
	if got := rowStatus(q, "step-one"); got != string(FlowStatusRunning) {
		t.Fatalf("precondition: interrupted step-one status = %q, want running", got)
	}
	q.mu.Lock()
	createdBefore := len(q.createdFlowStates)
	q.mu.Unlock()

	agent := newStubAgent()
	svc := NewService(sessions, nil, q, &stubPermissions{}, &stubAgentFactory{agent: agent})
	agentEvents, flowStates, err := svc.RunWithOptions(context.Background(), "prefix", "test-interrupt-recover", map[string]any{"k": "v"}, RunOptions{RecoverRunning: true})
	if err != nil {
		t.Fatalf("RunWithOptions(RecoverRunning) error: %v", err)
	}
	var states []*FlowState
	for st := range flowStates {
		states = append(states, st)
	}
	for range agentEvents {
	}

	if agent.callCount() != 1 {
		t.Fatalf("recovered step agent calls = %d, want 1 (states %v)", agent.callCount(), statusesOf(states))
	}
	if got := rowStatus(q, "step-one"); got != string(FlowStatusCompleted) {
		t.Errorf("recovered step-one status = %q, want completed", got)
	}
	q.mu.Lock()
	createdAfter := len(q.createdFlowStates)
	q.mu.Unlock()
	if createdAfter != createdBefore {
		t.Errorf("recovery must re-enter the interrupted row, not create one (created %d new)", createdAfter-createdBefore)
	}
}

func statusesOf(states []*FlowState) []string {
	out := make([]string, 0, len(states))
	for _, st := range states {
		out = append(out, st.StepID+":"+string(st.Status))
	}
	return out
}
