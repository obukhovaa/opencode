package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/flow"
	"github.com/opencode-ai/opencode/internal/pubsub"
	"github.com/opencode-ai/opencode/internal/session"
)

// stubSessions implements just enough of session.Service to satisfy the
// flow runner's per-step Cost / ContextSize lookup. Get returns canned
// fixtures keyed by session ID; missing IDs return ErrSessionNotFound
// (verifying the graceful zero-value fallback path).
type stubSessions struct {
	pubsub.Broker[session.Session]
	byID map[string]session.Session
	err  error
}

func (s *stubSessions) Create(context.Context, string) (session.Session, error) {
	return session.Session{}, nil
}
func (s *stubSessions) CreateWithID(context.Context, string, string) (session.Session, error) {
	return session.Session{}, nil
}
func (s *stubSessions) CreateFlowSession(context.Context, string, string, string) (session.Session, error) {
	return session.Session{}, nil
}
func (s *stubSessions) CreateTitleSession(context.Context, string) (session.Session, error) {
	return session.Session{}, nil
}
func (s *stubSessions) CreateTaskSession(context.Context, string, string, string) (session.Session, error) {
	return session.Session{}, nil
}
func (s *stubSessions) Get(_ context.Context, id string) (session.Session, error) {
	if s.err != nil {
		return session.Session{}, s.err
	}
	sess, ok := s.byID[id]
	if !ok {
		return session.Session{}, errors.New("not found")
	}
	return sess, nil
}
func (s *stubSessions) List(context.Context) ([]session.Session, error) { return nil, nil }
func (s *stubSessions) ListChildren(context.Context, string) ([]session.Session, error) {
	return nil, nil
}
func (s *stubSessions) Save(_ context.Context, sess session.Session) (session.Session, error) {
	return sess, nil
}
func (s *stubSessions) Delete(context.Context, string) error     { return nil }
func (s *stubSessions) DeleteTree(context.Context, string) error { return nil }
func (s *stubSessions) ListOldSessions(context.Context, string) ([]session.Session, error) {
	return nil, nil
}
func (s *stubSessions) CleanupOldSessions(context.Context, string) (int, error) {
	return 0, nil
}
func (s *stubSessions) Subscribe(ctx context.Context) <-chan pubsub.Event[session.Session] {
	return s.Broker.Subscribe(ctx)
}

func newFlowTestServerWithSessions(t *testing.T, svc flow.Service, sessions session.Service) (*httptest.Server, *flowRunner) {
	t.Helper()
	fr := newFlowRunnerWithSessions(svc, sessions)
	fr.validateFlowID = nil
	s := &Server{flowRunner: fr}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /flow", s.handleFlowList)
	mux.HandleFunc("POST /flow", s.handleFlowStart)
	mux.HandleFunc("GET /flow/status", s.handleFlowStatus)
	mux.HandleFunc("DELETE /flow", s.handleFlowAbort)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server, fr
}

// TestFlowEventCarriesExtendedFields verifies that step.started /
// completed / failed events emitted to /event SSE include the new
// IsStructOutput / Iteration / Cost / ContextSize fields sourced from
// FlowState and session.Service.
func TestFlowEventCarriesExtendedFields(t *testing.T) {
	t.Parallel()
	sessions := &stubSessions{
		byID: map[string]session.Session{
			"sess-1": {ID: "sess-1", Cost: 0.42, PromptTokens: 1234},
		},
	}
	svc := newStubFlowService([]flow.FlowState{
		{StepID: "s1", SessionID: "sess-1", Status: flow.FlowStatusRunning, IsStructOutput: false, Iteration: 1},
		{StepID: "s1", SessionID: "sess-1", Status: flow.FlowStatusCompleted, Output: `{"ok":true}`, IsStructOutput: true, Iteration: 2},
	})
	_, fr := newFlowTestServerWithSessions(t, svc, sessions)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := fr.subscribeFlowEvents(ctx)

	if _, err := fr.Start(context.Background(), "x", nil, false); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Drain events until we see the step-completed (or timeout).
	deadline := time.After(2 * time.Second)
	var startedSeen, completedSeen bool
	for !(startedSeen && completedSeen) {
		select {
		case ev := <-ch:
			switch ev.Payload.Type {
			case evFlowStepStarted:
				if ev.Payload.Iteration != 1 {
					t.Errorf("started Iteration = %d, want 1", ev.Payload.Iteration)
				}
				if ev.Payload.Cost != 0.42 {
					t.Errorf("started Cost = %v, want 0.42", ev.Payload.Cost)
				}
				if ev.Payload.ContextSize != 1234 {
					t.Errorf("started ContextSize = %d, want 1234", ev.Payload.ContextSize)
				}
				if ev.Payload.IsStructOutput {
					t.Errorf("started IsStructOutput = true, want false")
				}
				startedSeen = true
			case evFlowStepCompleted:
				if !ev.Payload.IsStructOutput {
					t.Errorf("completed IsStructOutput = false, want true")
				}
				if ev.Payload.Iteration != 2 {
					t.Errorf("completed Iteration = %d, want 2", ev.Payload.Iteration)
				}
				if ev.Payload.Cost != 0.42 {
					t.Errorf("completed Cost = %v", ev.Payload.Cost)
				}
				if ev.Payload.Output != `{"ok":true}` {
					t.Errorf("completed Output = %q", ev.Payload.Output)
				}
				completedSeen = true
			}
		case <-deadline:
			t.Fatalf("timed out: started=%v completed=%v", startedSeen, completedSeen)
		}
	}
}

// TestFlowEventJSONIncludesNewFields asserts the wire shape contains
// the new keys (orchestrator unmarshals against them).
func TestFlowEventJSONIncludesNewFields(t *testing.T) {
	t.Parallel()
	ev := FlowEvent{
		Type:           evFlowStepCompleted,
		RunID:          "r1",
		StepID:         "s1",
		IsStructOutput: true,
		Iteration:      3,
		Cost:           1.5,
		ContextSize:    9876,
	}
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out := string(b)
	for _, key := range []string{`"isStructOutput":true`, `"iteration":3`, `"cost":1.5`, `"contextSize":9876`} {
		if !strings.Contains(out, key) {
			t.Errorf("missing %s in %s", key, out)
		}
	}
}

// TestFlowEventSessionLookupFailureZeroValues asserts that when session
// lookup fails, Cost/ContextSize gracefully zero-value without
// publish-failing.
func TestFlowEventSessionLookupFailureZeroValues(t *testing.T) {
	t.Parallel()
	// Sessions backend returns "not found" for any ID — exercises the
	// graceful fallback path.
	sessions := &stubSessions{byID: map[string]session.Session{}}
	svc := newStubFlowService([]flow.FlowState{
		{StepID: "s1", SessionID: "missing", Status: flow.FlowStatusRunning, IsStructOutput: false, Iteration: 1},
		{StepID: "s1", SessionID: "missing", Status: flow.FlowStatusCompleted, IsStructOutput: true},
	})
	_, fr := newFlowTestServerWithSessions(t, svc, sessions)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := fr.subscribeFlowEvents(ctx)
	if _, err := fr.Start(context.Background(), "x", nil, false); err != nil {
		t.Fatalf("Start: %v", err)
	}
	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-ch:
			if ev.Payload.Type == evFlowStepCompleted {
				if ev.Payload.Cost != 0 || ev.Payload.ContextSize != 0 {
					t.Errorf("expected zero cost/context on missing session, got cost=%v ctx=%d", ev.Payload.Cost, ev.Payload.ContextSize)
				}
				return
			}
		case <-deadline:
			t.Fatalf("never saw completed event")
		}
	}
}

// TestFlowEventNoSessionService asserts that when the session service
// is nil (test seam), the runner still emits FlowEvents with zero cost/
// context — no nil-deref.
func TestFlowEventNoSessionService(t *testing.T) {
	t.Parallel()
	svc := newStubFlowService([]flow.FlowState{
		{StepID: "s1", SessionID: "x", Status: flow.FlowStatusCompleted, Output: "done"},
	})
	server := newFlowTestServer(t, svc) // legacy constructor wires sessions=nil
	resp, err := server.Client().Post(server.URL+"/flow", "application/json", strings.NewReader(`{"flowID":"x"}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	_ = resp.Body.Close()
	time.Sleep(150 * time.Millisecond)
	// Verify the run completed (no panic, no crash).
	statusResp, err := server.Client().Get(server.URL + "/flow/status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	defer statusResp.Body.Close()
	var snap flowRunSnapshot
	_ = json.NewDecoder(statusResp.Body).Decode(&snap)
	if snap.Status != flowRunCompleted {
		t.Errorf("status = %q, want completed (the nil-sessions path must not crash)", snap.Status)
	}
}

// TestWaitFlowTerminalGraceHoldsThenExits verifies that
// WaitFlowTerminal sleeps approximately `grace` after observing a
// terminal status, then invokes onTerminal exactly once.
func TestWaitFlowTerminalGraceHoldsThenExits(t *testing.T) {
	t.Parallel()
	svc := newStubFlowService([]flow.FlowState{
		{StepID: "s1", SessionID: "sess-1", Status: flow.FlowStatusCompleted, Output: "done"},
	})
	server, fr := newFlowTestServerWithSessions(t, svc, &stubSessions{byID: map[string]session.Session{"sess-1": {ID: "sess-1"}}})
	_ = server

	apiServer := &Server{flowRunner: fr}
	runID, err := apiServer.StartFlow("x", nil, false)
	if err != nil {
		t.Fatalf("StartFlow: %v", err)
	}

	terminalAt := time.Time{}
	called := make(chan struct{}, 1)
	onTerminal := func() {
		terminalAt = time.Now()
		called <- struct{}{}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	grace := 300 * time.Millisecond
	start := time.Now()
	go apiServer.WaitFlowTerminal(ctx, runID, grace, onTerminal)

	select {
	case <-called:
	case <-time.After(3 * time.Second):
		t.Fatalf("onTerminal never invoked")
	}
	elapsed := terminalAt.Sub(start)
	if elapsed < grace {
		t.Errorf("onTerminal fired after %s, want at least %s (grace was skipped?)", elapsed, grace)
	}
}

// TestWaitFlowTerminalCtxCancelShortCircuitsGrace verifies that a
// parent ctx cancellation during the grace window short-circuits the
// wait so SIGTERM is honored.
func TestWaitFlowTerminalCtxCancelShortCircuitsGrace(t *testing.T) {
	t.Parallel()
	svc := newStubFlowService([]flow.FlowState{
		{StepID: "s1", SessionID: "sess-1", Status: flow.FlowStatusCompleted, Output: "done"},
	})
	server, fr := newFlowTestServerWithSessions(t, svc, &stubSessions{byID: map[string]session.Session{"sess-1": {ID: "sess-1"}}})
	_ = server

	apiServer := &Server{flowRunner: fr}
	runID, err := apiServer.StartFlow("x", nil, false)
	if err != nil {
		t.Fatalf("StartFlow: %v", err)
	}

	called := make(chan struct{}, 1)
	onTerminal := func() { called <- struct{}{} }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	grace := 5 * time.Second
	go apiServer.WaitFlowTerminal(ctx, runID, grace, onTerminal)
	// Wait for the terminal status to be observed and the grace window
	// to start, then cancel.
	time.Sleep(400 * time.Millisecond)
	cancel()

	select {
	case <-called:
	case <-time.After(1 * time.Second):
		t.Fatalf("ctx cancellation during grace did not unblock onTerminal in 1s")
	}
}
