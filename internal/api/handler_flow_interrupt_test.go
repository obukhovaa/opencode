package api

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/flow"
	agentpkg "github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/pubsub"
)

// inFlightFlowService emits a running step and then holds the run open until
// its ctx is cancelled, like an engine whose step is mid LLM call. On
// cancellation it mirrors the engine: a failed step state for a plain cancel,
// none for a flow.ErrProcessShutdown cause.
type inFlightFlowService struct {
	*stubFlowService
}

func (s *inFlightFlowService) RunWithOptions(ctx context.Context, _ string, flowID string, _ map[string]any, _ flow.RunOptions) (<-chan agentpkg.AgentEvent, <-chan *flow.FlowState, error) {
	ae := make(chan agentpkg.AgentEvent)
	fs := make(chan *flow.FlowState, 2)
	go func() {
		defer close(ae)
		defer close(fs)
		fs <- &flow.FlowState{FlowID: flowID, StepID: "s1", SessionID: "sess-s1", Status: flow.FlowStatusRunning}
		<-ctx.Done()
		if !errors.Is(context.Cause(ctx), flow.ErrProcessShutdown) {
			fs <- &flow.FlowState{FlowID: flowID, StepID: "s1", SessionID: "sess-s1", Status: flow.FlowStatusFailed, Output: ctx.Err().Error()}
		}
	}()
	return ae, fs, nil
}

func startInFlightRun(t *testing.T) (*Server, <-chan pubsub.Event[FlowEvent]) {
	t.Helper()
	fr := newFlowRunner(&inFlightFlowService{stubFlowService: newStubFlowService(nil)})
	fr.validateFlowID = nil
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	events := fr.broker.Subscribe(ctx)
	s := &Server{flowRunner: fr}
	if _, err := s.StartFlow("t", nil, false, false); err != nil {
		t.Fatalf("StartFlow: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		snap := fr.Snapshot()
		if snap != nil && snap.CurrentStep != nil && snap.CurrentStep.Status == string(flow.FlowStatusRunning) {
			return s, events
		}
		if time.Now().After(deadline) {
			t.Fatal("run never reported its step running")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// drainTypes collects the event types published within d.
func drainTypes(events <-chan pubsub.Event[FlowEvent], d time.Duration) []string {
	var types []string
	timer := time.NewTimer(d)
	defer timer.Stop()
	for {
		select {
		case ev := <-events:
			types = append(types, string(ev.Payload.Type))
		case <-timer.C:
			return types
		}
	}
}

// TestInterruptFlowKeepsRunInFlight (GENAI-709): a shutdown interrupt reports
// the run as in flight, waits for it to unwind, and publishes no terminal
// event, so the orchestrator still sees the step running when the process
// exits 143. DELETE /flow's Abort keeps publishing flow.failed.
func TestInterruptFlowKeepsRunInFlight(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		interrupt  func(s *Server) bool
		wantFailed bool
		wantStatus flowRunStatus
	}{
		{
			name:       "process shutdown",
			interrupt:  func(s *Server) bool { return s.InterruptFlow(flow.ErrProcessShutdown, 5*time.Second) },
			wantFailed: false,
			wantStatus: flowRunRunning,
		},
		{
			name: "abort",
			interrupt: func(s *Server) bool {
				_, ok := s.flowRunner.Abort()
				return ok
			},
			wantFailed: true,
			wantStatus: flowRunFailed,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s, events := startInFlightRun(t)
			_ = drainTypes(events, 50*time.Millisecond)
			if !tt.interrupt(s) {
				t.Fatal("interrupt reported no in-flight run")
			}
			types := drainTypes(events, 300*time.Millisecond)
			failed := false
			for _, typ := range types {
				if typ == string(evFlowFailed) || typ == string(evFlowStepFailed) || typ == string(evFlowCompleted) {
					failed = failed || typ != string(evFlowCompleted)
					if !tt.wantFailed {
						t.Errorf("published %s after interrupt; want no terminal event (got %v)", typ, types)
					}
				}
			}
			if tt.wantFailed && !failed {
				t.Errorf("want flow.failed after abort, got %v", types)
			}
			if got := s.flowRunner.Snapshot().Status; got != tt.wantStatus {
				t.Errorf("run status = %q, want %q", got, tt.wantStatus)
			}
		})
	}
}

// TestInterruptFlowIdleOrTerminal: with nothing in flight the signal handler
// must see false, so the process keeps exiting 0.
func TestInterruptFlowIdleOrTerminal(t *testing.T) {
	t.Parallel()
	t.Run("idle", func(t *testing.T) {
		t.Parallel()
		s := &Server{flowRunner: newFlowRunner(newStubFlowService(nil))}
		if s.InterruptFlow(flow.ErrProcessShutdown, time.Second) {
			t.Error("idle runner reported an in-flight run")
		}
	})
	t.Run("no runner", func(t *testing.T) {
		t.Parallel()
		s := &Server{}
		if s.InterruptFlow(flow.ErrProcessShutdown, time.Second) {
			t.Error("server without a flow runner reported an in-flight run")
		}
	})
	t.Run("terminal", func(t *testing.T) {
		t.Parallel()
		fr := newFlowRunner(newStubFlowService([]flow.FlowState{
			{StepID: "s1", Status: flow.FlowStatusRunning},
			{StepID: "s1", Status: flow.FlowStatusCompleted, Output: "done"},
		}))
		fr.validateFlowID = nil
		s := &Server{flowRunner: fr}
		if _, err := s.StartFlow("t", nil, false, false); err != nil {
			t.Fatalf("StartFlow: %v", err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for !isTerminalStatus(fr.Snapshot().Status) {
			if time.Now().After(deadline) {
				t.Fatal("run never reached terminal")
			}
			time.Sleep(10 * time.Millisecond)
		}
		if s.InterruptFlow(flow.ErrProcessShutdown, time.Second) {
			t.Error("terminal run reported as in flight")
		}
	})
}

// TestAbortCancelsWithPlainCause: DELETE /flow must not carry the shutdown
// cause, or the engine would leave the aborted step running.
func TestAbortCancelsWithPlainCause(t *testing.T) {
	t.Parallel()
	var gotCause error
	done := make(chan struct{})
	svc := &causeRecordingFlowService{stubFlowService: newStubFlowService(nil), started: make(chan struct{}), record: func(err error) { gotCause = err; close(done) }}
	fr := newFlowRunner(svc)
	fr.validateFlowID = nil
	if _, err := (&Server{flowRunner: fr}).StartFlow("t", nil, false, false); err != nil {
		t.Fatalf("StartFlow: %v", err)
	}
	<-svc.started
	if _, ok := fr.Abort(); !ok {
		t.Fatal("Abort found no run")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("run ctx was never cancelled")
	}
	if errors.Is(gotCause, flow.ErrProcessShutdown) {
		t.Errorf("abort cause = %v, must not be ErrProcessShutdown", gotCause)
	}
}

type causeRecordingFlowService struct {
	*stubFlowService
	record  func(error)
	started chan struct{}
}

func (s *causeRecordingFlowService) RunWithOptions(ctx context.Context, _ string, _ string, _ map[string]any, _ flow.RunOptions) (<-chan agentpkg.AgentEvent, <-chan *flow.FlowState, error) {
	ae := make(chan agentpkg.AgentEvent)
	fs := make(chan *flow.FlowState)
	go func() {
		defer close(ae)
		defer close(fs)
		close(s.started)
		<-ctx.Done()
		s.record(context.Cause(ctx))
	}()
	return ae, fs, nil
}
