package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/flow"
	agentpkg "github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/pubsub"
)

// stubFlowService implements flow.Service with a synthetic run that
// emits the requested step transitions on the flowStates channel. Used
// to drive handler_flow.go's tracker without spinning up the real
// flow engine (which needs a session/messages/permissions stack).
type stubFlowService struct {
	*pubsub.Broker[flow.FlowState]
	steps     []flow.FlowState // emitted in order
	stepDelay time.Duration
	runErr    error
	mu        sync.Mutex
	runs      int
}

func newStubFlowService(steps []flow.FlowState) *stubFlowService {
	return &stubFlowService{
		Broker: pubsub.NewBroker[flow.FlowState](),
		steps:  steps,
	}
}

func (s *stubFlowService) Run(ctx context.Context, _ string, flowID string, _ map[string]any, _ bool) (<-chan agentpkg.AgentEvent, <-chan *flow.FlowState, error) {
	if s.runErr != nil {
		return nil, nil, s.runErr
	}
	s.mu.Lock()
	s.runs++
	s.mu.Unlock()

	ae := make(chan agentpkg.AgentEvent, 1)
	fs := make(chan *flow.FlowState, len(s.steps))
	go func() {
		defer close(ae)
		defer close(fs)
		for i := range s.steps {
			select {
			case <-ctx.Done():
				return
			case <-time.After(s.stepDelay):
			}
			st := s.steps[i]
			st.FlowID = flowID
			fs <- &st
		}
	}()
	return ae, fs, nil
}

// SetInteractiveHook satisfies the InteractiveHookSetter contract for
// cmd/serve.go's wiring; tests don't actually exercise this path.
func (s *stubFlowService) SetInteractiveHook(h flow.InteractiveHook) {}

func newFlowTestServer(t *testing.T, svc flow.Service) *httptest.Server {
	t.Helper()
	fr := newFlowRunner(svc)
	// Tests drive synthetic flowIDs that never appear in any registered
	// YAML; bypass the registry-based existence check (the registry
	// itself depends on config.Get(), which isn't loaded under tests).
	fr.validateFlowID = nil
	s := &Server{flowRunner: fr}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /flow", s.handleFlowList)
	mux.HandleFunc("POST /flow", s.handleFlowStart)
	mux.HandleFunc("GET /flow/status", s.handleFlowStatus)
	mux.HandleFunc("DELETE /flow", s.handleFlowAbort)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func TestFlowStartReturns202AndRunID(t *testing.T) {
	t.Parallel()
	svc := newStubFlowService([]flow.FlowState{
		{StepID: "s1", Status: flow.FlowStatusRunning, UpdatedAt: 1700000000},
		{StepID: "s1", Status: flow.FlowStatusCompleted, Output: "done"},
	})
	server := newFlowTestServer(t, svc)

	resp, err := server.Client().Post(server.URL+"/flow", "application/json", strings.NewReader(`{"flowID":"test-flow"}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("status = %d, want 202", resp.StatusCode)
	}
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body["runID"] == "" {
		t.Errorf("runID missing in response: %+v", body)
	}
	if body["flowID"] != "test-flow" {
		t.Errorf("flowID = %v", body["flowID"])
	}
}

func TestFlowStartConflictWhenAlreadyRunning(t *testing.T) {
	t.Parallel()
	svc := newStubFlowService([]flow.FlowState{
		{StepID: "s1", Status: flow.FlowStatusRunning},
	})
	svc.stepDelay = 200 * time.Millisecond // keep first run "running" long enough
	server := newFlowTestServer(t, svc)

	resp1, err := server.Client().Post(server.URL+"/flow", "application/json", strings.NewReader(`{"flowID":"t"}`))
	if err != nil {
		t.Fatalf("POST 1: %v", err)
	}
	defer resp1.Body.Close()
	if resp1.StatusCode != http.StatusAccepted {
		t.Fatalf("first POST = %d", resp1.StatusCode)
	}

	resp2, err := server.Client().Post(server.URL+"/flow", "application/json", strings.NewReader(`{"flowID":"t"}`))
	if err != nil {
		t.Fatalf("POST 2: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusConflict {
		t.Errorf("second POST = %d, want 409", resp2.StatusCode)
	}
}

func TestFlowStatusReportsCurrentRun(t *testing.T) {
	t.Parallel()
	svc := newStubFlowService([]flow.FlowState{
		{StepID: "s1", Status: flow.FlowStatusRunning},
		{StepID: "s1", Status: flow.FlowStatusCompleted, Output: "done"},
	})
	server := newFlowTestServer(t, svc)

	postResp, err := server.Client().Post(server.URL+"/flow", "application/json", strings.NewReader(`{"flowID":"x"}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	_ = postResp.Body.Close()

	// Allow the stub to drain.
	time.Sleep(200 * time.Millisecond)

	resp, err := server.Client().Get(server.URL + "/flow/status")
	if err != nil {
		t.Fatalf("GET status: %v", err)
	}
	defer resp.Body.Close()
	var snap flowRunSnapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if snap.FlowID != "x" {
		t.Errorf("flowID = %q", snap.FlowID)
	}
	if snap.Status != flowRunCompleted {
		t.Errorf("status = %q, want completed", snap.Status)
	}
	if len(snap.CompletedSteps) == 0 {
		t.Errorf("CompletedSteps empty")
	}
}

func TestFlowAbortReturnsOK(t *testing.T) {
	t.Parallel()
	svc := newStubFlowService([]flow.FlowState{
		{StepID: "s1", Status: flow.FlowStatusRunning},
	})
	svc.stepDelay = 500 * time.Millisecond
	server := newFlowTestServer(t, svc)

	postResp, err := server.Client().Post(server.URL+"/flow", "application/json", strings.NewReader(`{"flowID":"x"}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	_ = postResp.Body.Close()
	time.Sleep(50 * time.Millisecond)

	req, _ := http.NewRequest(http.MethodDelete, server.URL+"/flow", nil)
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("DELETE = %d, want 200", resp.StatusCode)
	}
}

func TestFlowAbortReturns409WhenIdle(t *testing.T) {
	t.Parallel()
	svc := newStubFlowService(nil)
	server := newFlowTestServer(t, svc)

	req, _ := http.NewRequest(http.MethodDelete, server.URL+"/flow", nil)
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("DELETE idle = %d, want 409", resp.StatusCode)
	}
}

func TestFlowStartMissingFlowIDIs400(t *testing.T) {
	t.Parallel()
	svc := newStubFlowService(nil)
	server := newFlowTestServer(t, svc)

	resp, err := server.Client().Post(server.URL+"/flow", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestFlowStatusIdleBeforeFirstRun(t *testing.T) {
	t.Parallel()
	svc := newStubFlowService(nil)
	server := newFlowTestServer(t, svc)

	resp, err := server.Client().Get(server.URL + "/flow/status")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body["status"] != "idle" {
		t.Errorf("idle status = %v", body["status"])
	}
}
