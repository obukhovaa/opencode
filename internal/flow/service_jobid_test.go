package flow

import (
	"context"
	"testing"

	agentpkg "github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/pubsub"
)

// The flow engine has ten flow_states write sites and JobID is a plain string,
// so an omission at any one of them persists "" instead of failing to compile.
// createFlowState/updateFlowState exist to stamp it in one place per operation;
// this test is what holds that invariant, by asserting on EVERY recorded write
// rather than on a sampled one.
func TestJobIDIsStampedOnEveryFlowStateWrite(t *testing.T) {
	const jobID = "job-0123456789abcdef"

	testFlow := Flow{
		ID: "job-id-stamping",
		Spec: FlowSpec{
			Steps: []Step{{ID: "step-one", Prompt: "do something"}},
		},
	}
	registerTestFlow(t, testFlow)

	agent := &stubAgent{
		Broker: pubsub.NewBroker[agentpkg.AgentEvent](),
		responses: []agentpkg.AgentEvent{
			{
				Type:    agentpkg.AgentEventTypeResponse,
				Message: message.Message{Role: message.Assistant},
			},
		},
	}
	q := &stubQuerier{}
	svc := NewService(&stubSessions{}, nil, q, &stubPermissions{}, &stubAgentFactory{agent: agent}, jobID)

	agentEvents, flowStates, err := svc.Run(context.Background(), "prefix", testFlow.ID, map[string]any{}, true)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	for range flowStates {
	}
	for range agentEvents {
	}

	q.mu.Lock()
	creates := append([]struct {
		SessionID string
		JobID     string
	}(nil))
	for _, c := range q.createdFlowStates {
		creates = append(creates, struct {
			SessionID string
			JobID     string
		}{c.SessionID, c.JobID})
	}
	for _, u := range q.updatedFlowStates {
		creates = append(creates, struct {
			SessionID string
			JobID     string
		}{u.SessionID, u.JobID})
	}
	q.mu.Unlock()

	if len(creates) == 0 {
		t.Fatal("no flow_states writes recorded; the test would pass vacuously")
	}
	for i, w := range creates {
		if w.JobID != jobID {
			t.Errorf("write %d (session %q) job_id = %q, want %q",
				i, w.SessionID, w.JobID, jobID)
		}
	}
}

// A standalone opencode run has no orchestrator job, and must record an empty
// job_id rather than inventing one.
func TestJobIDEmptyWhenStandalone(t *testing.T) {
	testFlow := Flow{
		ID: "job-id-standalone",
		Spec: FlowSpec{
			Steps: []Step{{ID: "step-one", Prompt: "do something"}},
		},
	}
	registerTestFlow(t, testFlow)

	agent := &stubAgent{
		Broker: pubsub.NewBroker[agentpkg.AgentEvent](),
		responses: []agentpkg.AgentEvent{
			{
				Type:    agentpkg.AgentEventTypeResponse,
				Message: message.Message{Role: message.Assistant},
			},
		},
	}
	q := &stubQuerier{}
	svc := NewService(&stubSessions{}, nil, q, &stubPermissions{}, &stubAgentFactory{agent: agent}, "")

	agentEvents, flowStates, err := svc.Run(context.Background(), "prefix", testFlow.ID, map[string]any{}, true)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	for range flowStates {
	}
	for range agentEvents {
	}

	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.createdFlowStates) == 0 {
		t.Fatal("no flow_states creates recorded; the test would pass vacuously")
	}
	for i, c := range q.createdFlowStates {
		if c.JobID != "" {
			t.Errorf("create %d job_id = %q, want empty", i, c.JobID)
		}
	}
}
