package flow

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/db"
	agentpkg "github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/pubsub"
	"github.com/opencode-ai/opencode/internal/session"
)

// stubQuerier records calls to delete operations and returns pre-configured flow states.
type stubQuerier struct {
	db.QuerierWithTx

	flowStates              []db.FlowState
	deletedFlowRootSessions []string
	createdFlowStates       []db.CreateFlowStateParams
}

func (q *stubQuerier) ListFlowStatesByRootSession(_ context.Context, rootSessionID string) ([]db.FlowState, error) {
	var result []db.FlowState
	for _, fs := range q.flowStates {
		if fs.RootSessionID == rootSessionID {
			result = append(result, fs)
		}
	}
	return result, nil
}

func (q *stubQuerier) DeleteFlowStatesByRootSession(_ context.Context, rootSessionID string) error {
	q.deletedFlowRootSessions = append(q.deletedFlowRootSessions, rootSessionID)
	var remaining []db.FlowState
	for _, fs := range q.flowStates {
		if fs.RootSessionID != rootSessionID {
			remaining = append(remaining, fs)
		}
	}
	q.flowStates = remaining
	return nil
}

func (q *stubQuerier) GetFlowState(_ context.Context, sessionID string) (db.FlowState, error) {
	for _, fs := range q.flowStates {
		if fs.SessionID == sessionID {
			return fs, nil
		}
	}
	return db.FlowState{}, sql.ErrNoRows
}

func (q *stubQuerier) CreateFlowState(_ context.Context, arg db.CreateFlowStateParams) (db.FlowState, error) {
	q.createdFlowStates = append(q.createdFlowStates, arg)
	now := time.Now().Unix()
	return db.FlowState{
		SessionID:      arg.SessionID,
		RootSessionID:  arg.RootSessionID,
		FlowID:         arg.FlowID,
		StepID:         arg.StepID,
		Status:         arg.Status,
		Args:           arg.Args,
		IsStructOutput: arg.IsStructOutput,
		CreatedAt:      now,
		UpdatedAt:      now,
	}, nil
}

func (q *stubQuerier) UpdateFlowState(_ context.Context, arg db.UpdateFlowStateParams) (db.FlowState, error) {
	now := time.Now().Unix()
	return db.FlowState{
		SessionID: arg.SessionID,
		Status:    arg.Status,
		Args:      arg.Args,
		Output:    arg.Output,
		UpdatedAt: now,
	}, nil
}

func (q *stubQuerier) WithTx(_ *sql.Tx) db.QuerierWithTx { return q }

// stubSessions records delete calls and provides minimal session operations.
type stubSessions struct {
	session.Service
	deletedIDs []string
}

func (s *stubSessions) Delete(_ context.Context, id string) error {
	s.deletedIDs = append(s.deletedIDs, id)
	return nil
}

func (s *stubSessions) Get(_ context.Context, _ string) (session.Session, error) {
	return session.Session{}, fmt.Errorf("not found")
}

func (s *stubSessions) CreateFlowSession(_ context.Context, id, rootSessionID, title string) (session.Session, error) {
	return session.Session{ID: id, RootSessionID: rootSessionID, Title: title}, nil
}

type stubPermissions struct {
	permission.Service
}

func (p *stubPermissions) AutoApproveSession(_ string) {}

// stubAgent returns a response event immediately.
type stubAgent struct {
	*pubsub.Broker[agentpkg.AgentEvent]
}

func newStubAgent() *stubAgent {
	return &stubAgent{Broker: pubsub.NewBroker[agentpkg.AgentEvent]()}
}

func (a *stubAgent) Run(_ context.Context, _ string, _ string, _ ...message.Attachment) (<-chan agentpkg.AgentEvent, error) {
	ch := make(chan agentpkg.AgentEvent, 1)
	ch <- agentpkg.AgentEvent{
		Type: agentpkg.AgentEventTypeResponse,
		Message: message.Message{
			Role:  message.Assistant,
			Parts: []message.ContentPart{message.TextContent{Text: "done"}},
		},
	}
	close(ch)
	return ch, nil
}

func (a *stubAgent) AgentID() config.AgentName   { return "coder" }
func (a *stubAgent) Model() models.Model         { return models.Model{} }
func (a *stubAgent) Cancel(_ string)             {}
func (a *stubAgent) IsSessionBusy(_ string) bool { return false }
func (a *stubAgent) IsBusy() bool                { return false }
func (a *stubAgent) Update(_ config.AgentName, _ models.ModelID) (models.Model, error) {
	return models.Model{}, nil
}
func (a *stubAgent) Summarize(_ context.Context, _ string) error { return nil }

// stubAgentFactory returns the stubAgent.
type stubAgentFactory struct{}

func (f *stubAgentFactory) NewAgent(_ context.Context, _ string, _ map[string]any, _ string) (agentpkg.Service, error) {
	return newStubAgent(), nil
}

func (f *stubAgentFactory) InitPrimaryAgents(_ context.Context, _ map[string]any) ([]agentpkg.Service, error) {
	return nil, nil
}

func registerTestFlow(t *testing.T, f Flow) {
	t.Helper()
	flowCacheLock.Lock()
	if flowCache == nil {
		flowCache = make(map[string]Flow)
	}
	flowCache[f.ID] = f
	flowCacheInit = true
	flowCacheLock.Unlock()
	t.Cleanup(Invalidate)
}

func TestRunFreshDeletesRunningStates(t *testing.T) {
	testFlow := Flow{
		ID:   "test-fresh",
		Name: "Test Fresh",
		Spec: FlowSpec{
			Steps: []Step{
				{ID: "step-one", Prompt: "do something"},
			},
		},
	}
	registerTestFlow(t, testFlow)

	rootSessionID := "prefix-test-fresh-step-one"

	q := &stubQuerier{
		flowStates: []db.FlowState{
			{
				SessionID:     "prefix-test-fresh-step-one",
				RootSessionID: rootSessionID,
				FlowID:        "test-fresh",
				StepID:        "step-one",
				Status:        string(FlowStatusRunning),
				Args:          sql.NullString{String: `{}`, Valid: true},
				CreatedAt:     time.Now().Unix(),
				UpdatedAt:     time.Now().Unix(),
			},
		},
	}
	sessions := &stubSessions{}

	svc := NewService(sessions, nil, q, &stubPermissions{}, &stubAgentFactory{})

	ctx := context.Background()
	agentEvents, flowStates, err := svc.Run(ctx, "prefix", "test-fresh", map[string]any{}, true)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	for range agentEvents {
	}
	for range flowStates {
	}

	if len(q.deletedFlowRootSessions) == 0 {
		t.Fatal("expected DeleteFlowStatesByRootSession to be called, but it was not")
	}
	if q.deletedFlowRootSessions[0] != rootSessionID {
		t.Errorf("deleted root session = %q, want %q", q.deletedFlowRootSessions[0], rootSessionID)
	}

	if len(sessions.deletedIDs) == 0 {
		t.Fatal("expected session Delete to be called, but it was not")
	}
	found := false
	for _, id := range sessions.deletedIDs {
		if id == "prefix-test-fresh-step-one" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected session 'prefix-test-fresh-step-one' to be deleted, got %v", sessions.deletedIDs)
	}

	if len(q.createdFlowStates) == 0 {
		t.Fatal("expected CreateFlowState to be called for fresh start, but it was not")
	}
	if q.createdFlowStates[0].StepID != "step-one" {
		t.Errorf("created flow state step = %q, want %q", q.createdFlowStates[0].StepID, "step-one")
	}
}

func TestRunWithoutFreshReturnsRunningStates(t *testing.T) {
	testFlow := Flow{
		ID:   "test-no-fresh",
		Name: "Test No Fresh",
		Spec: FlowSpec{
			Steps: []Step{
				{ID: "step-one", Prompt: "do something"},
			},
		},
	}
	registerTestFlow(t, testFlow)

	rootSessionID := "prefix-test-no-fresh-step-one"

	q := &stubQuerier{
		flowStates: []db.FlowState{
			{
				SessionID:     "prefix-test-no-fresh-step-one",
				RootSessionID: rootSessionID,
				FlowID:        "test-no-fresh",
				StepID:        "step-one",
				Status:        string(FlowStatusRunning),
				Args:          sql.NullString{String: `{}`, Valid: true},
				CreatedAt:     time.Now().Unix(),
				UpdatedAt:     time.Now().Unix(),
			},
		},
	}
	sessions := &stubSessions{}

	svc := NewService(sessions, nil, q, &stubPermissions{}, &stubAgentFactory{})

	ctx := context.Background()
	agentEvents, flowStates, err := svc.Run(ctx, "prefix", "test-no-fresh", map[string]any{}, false)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	var states []*FlowState
	for s := range flowStates {
		states = append(states, s)
	}
	for range agentEvents {
	}

	if len(q.deletedFlowRootSessions) != 0 {
		t.Errorf("expected no deletions, got %v", q.deletedFlowRootSessions)
	}

	if len(sessions.deletedIDs) != 0 {
		t.Errorf("expected no session deletions, got %v", sessions.deletedIDs)
	}

	if len(states) != 1 {
		t.Fatalf("expected 1 state re-emitted, got %d", len(states))
	}
	if states[0].Status != FlowStatusRunning {
		t.Errorf("re-emitted state status = %q, want %q", states[0].Status, FlowStatusRunning)
	}

	if len(q.createdFlowStates) != 0 {
		t.Errorf("expected no CreateFlowState calls, got %d", len(q.createdFlowStates))
	}
}
