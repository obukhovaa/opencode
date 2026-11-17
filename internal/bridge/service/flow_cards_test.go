package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/bridge/store"
	"github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/flow"
	"github.com/opencode-ai/opencode/internal/flowrun"
	"github.com/stretchr/testify/require"
)

type flowCardAdapter struct {
	*stubAdapter
	posted, edited []bridge.FlowProgress
	tokens         []string
	failure        error
	editFailure    error
}

func (a *flowCardAdapter) SendFlowProgress(_ context.Context, p bridge.PeerRef, card bridge.FlowProgress) (string, error) {
	if a.failure != nil {
		return "", a.failure
	}
	a.posted = append(a.posted, card)
	return fmt.Sprintf("%s/card-%d", p.PeerID, len(a.posted)), nil
}
func (a *flowCardAdapter) EditFlowProgress(_ context.Context, _ bridge.PeerRef, token string, card bridge.FlowProgress) error {
	if a.editFailure != nil {
		return a.editFailure
	}
	if a.failure != nil {
		return a.failure
	}
	a.edited = append(a.edited, card)
	a.tokens = append(a.tokens, token)
	return nil
}
func newFlowCardService(t *testing.T) (*Service, *flowCardAdapter, *testSessionFlows) {
	t.Helper()
	s, conn := newOrchestratorForTest(t)
	q := bridgeFlowQueries{db.New(conn)}
	runs := flowrun.New(conn, q, "proj", "sqlite")
	ctx := context.Background()
	run, _, err := runs.Create(ctx, flowrun.Run{ConversationID: "S1", InvocationID: "invoke", Kind: "generated", Snapshot: json.RawMessage(`{}`)})
	require.NoError(t, err)
	lease, err := runs.Claim(ctx, run.ID, "worker", time.Hour)
	require.NoError(t, err)
	c := &testSessionFlows{run: run, lease: lease}
	s.app = &app.App{FlowRuns: runs}
	s.sessionFlows = c
	s.cfg.ToolUpdatesEnabled = true
	a := &flowCardAdapter{stubAdapter: newStubAdapter("slack", "default")}
	s.adapters[adapterKey("slack", "default")] = a
	_, err = s.store.UpsertBinding(ctx, store.Binding{ProjectID: "proj", Channel: "slack", IdentityID: "default", PeerID: "D1", SessionID: "S1"})
	require.NoError(t, err)
	return s, a, c
}
func appendCardEvent(t *testing.T, s *Service, c *testSessionFlows, e app.FlowRunEvent) app.FlowRunEvent {
	t.Helper()
	e.RunID = c.run.ID
	e.ConversationID = "S1"
	if e.OccurredAt == 0 {
		e.OccurredAt = time.Now().UnixMilli()
	}
	b, err := json.Marshal(e)
	require.NoError(t, err)
	record, err := s.app.FlowRuns.AppendEvent(context.Background(), c.lease, fmt.Sprintf("%s-%d", e.Type, time.Now().UnixNano()), e.Type, b)
	require.NoError(t, err)
	e.ID = record.ID
	e.Sequence = record.Sequence
	return e
}
func flowCardWasDelivered(t *testing.T, s *Service, e app.FlowRunEvent) bool {
	t.Helper()
	ok, err := s.app.FlowRuns.Delivered(context.Background(), e.RunID, e.ID, peerReceiptKey(bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "D1"}))
	require.NoError(t, err)
	return ok
}
func TestFlowCardCoalescesPersistsAndReplays(t *testing.T) {
	ctx := context.Background()
	s, a, c := newFlowCardService(t)
	start := appendCardEvent(t, s, c, app.FlowRunEvent{Type: "session.flow.step", StepID: "review", Status: "running", Agent: "reviewer", Iteration: 1, OccurredAt: 10000})
	s.deliverSessionFlowEvent(ctx, start)
	require.Len(t, a.posted, 1)
	finish := appendCardEvent(t, s, c, app.FlowRunEvent{Type: "session.flow.step", StepID: "review", Status: "completed", Output: `{"ok":true}`, IsStructOutput: true, Iteration: 1, OccurredAt: 14000})
	s.deliverSessionFlowEvent(ctx, finish)
	require.Empty(t, a.edited)
	require.False(t, flowCardWasDelivered(t, s, finish))
	final := appendCardEvent(t, s, c, app.FlowRunEvent{Type: "session.flow.completed", OccurredAt: 15000})
	s.deliverSessionFlowEvent(ctx, final)
	require.Len(t, a.edited, 1)
	require.Equal(t, "completed", a.edited[0].Status)
	require.Equal(t, int64(10000), a.edited[0].Steps[0].StartedAt)
	require.Empty(t, a.edited[0].Steps[0].Output)
	card, err := s.app.FlowRuns.GetCard(ctx, c.run.ID, flowCardRecipient(bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "D1"}))
	require.NoError(t, err)
	require.Equal(t, final.Sequence, card.Sequence)
	require.Contains(t, string(card.State), `\"ok\":true`)
	fresh := &Service{app: s.app, store: s.store, sessionFlows: c, cfg: s.cfg, projectID: s.projectID, adapters: s.adapters}
	fresh.deliverSessionFlowEvent(ctx, finish)
	fresh.deliverSessionFlowEvent(ctx, start)
	require.Len(t, a.posted, 1)
	require.Len(t, a.edited, 1)
	require.True(t, flowCardWasDelivered(t, s, finish))
}
func TestFlowCardFailureDoesNotAcknowledgeOrLoseToken(t *testing.T) {
	ctx := context.Background()
	s, a, c := newFlowCardService(t)
	start := appendCardEvent(t, s, c, app.FlowRunEvent{Type: "session.flow.started"})
	a.failure = errors.New("rate limited")
	s.deliverSessionFlowEvent(ctx, start)
	require.False(t, flowCardWasDelivered(t, s, start))
	require.Empty(t, a.posted)
	a.failure = nil
	s.deliverSessionFlowEvent(ctx, start)
	require.Len(t, a.posted, 1)
	failed := appendCardEvent(t, s, c, app.FlowRunEvent{Type: "session.flow.failed", Output: "provider unavailable"})
	a.failure = errors.New("timeout")
	s.deliverSessionFlowEvent(ctx, failed)
	require.False(t, flowCardWasDelivered(t, s, failed))
	require.Len(t, a.posted, 1)
	a.failure = nil
	s.deliverSessionFlowEvent(ctx, failed)
	require.Len(t, a.edited, 1)
	require.Equal(t, "D1/card-1", a.tokens[0])
	require.Equal(t, "provider unavailable", a.edited[0].Error)
	require.True(t, flowCardWasDelivered(t, s, failed))
}
func TestFlowCardVerbosityAndDisabledUpdates(t *testing.T) {
	ctx := context.Background()
	s, a, c := newFlowCardService(t)
	s.cfg.ToolUpdatesEnabled = false
	routine := appendCardEvent(t, s, c, app.FlowRunEvent{Type: "session.flow.step", StepID: "review", Status: "completed", Output: `{"details":1}`, IsStructOutput: true})
	s.deliverSessionFlowEvent(ctx, routine)
	require.Empty(t, a.posted)
	require.True(t, flowCardWasDelivered(t, s, routine))
	s.cfg.ToolUpdatesEnabled = true
	_, err := s.SetToolVerbosity("full")
	require.NoError(t, err)
	wait := appendCardEvent(t, s, c, app.FlowRunEvent{Type: "session.flow.step", StepID: "approve", Status: "waiting_for_input"})
	s.deliverSessionFlowEvent(ctx, wait)
	require.Len(t, a.posted, 1)
	require.True(t, a.posted[0].Full)
	require.NotEmpty(t, a.posted[0].Steps[0].Output)
	_, err = s.SetToolVerbosity("compact")
	require.NoError(t, err)
	failure := appendCardEvent(t, s, c, app.FlowRunEvent{Type: "session.flow.failed", Output: "approval unavailable"})
	s.cfg.ToolUpdatesEnabled = false
	s.deliverSessionFlowEvent(ctx, failure)
	require.Len(t, a.edited, 1)
	require.False(t, a.edited[0].Full)
	require.Empty(t, a.edited[0].Steps[0].Output)
	require.Equal(t, "approval unavailable", a.edited[0].Error)
}
func TestFlowCardRecipientsAndThreadMigration(t *testing.T) {
	ctx := context.Background()
	s, a, c := newFlowCardService(t)
	event := appendCardEvent(t, s, c, app.FlowRunEvent{Type: "session.flow.started"})
	s.deliverSessionFlowEvent(ctx, event)
	require.NoError(t, s.store.UpdateBindingPeerID(ctx, "proj", "slack", "default", "D1", "D1|thread"))
	final := appendCardEvent(t, s, c, app.FlowRunEvent{Type: "session.flow.completed"})
	s.deliverSessionFlowEvent(ctx, final)
	require.Len(t, a.posted, 1)
	require.Equal(t, "D1/card-1", a.tokens[0])
	_, err := s.app.FlowRuns.GetCard(ctx, c.run.ID, flowCardRecipient(bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "D1"}))
	require.ErrorIs(t, err, flowrun.ErrNotFound)
	_, err = s.store.UpsertBinding(ctx, store.Binding{ProjectID: "proj", Channel: "slack", IdentityID: "default", PeerID: "D1|other", SessionID: "S1"})
	require.NoError(t, err)
	s.deliverSessionFlowEvent(ctx, final)
	require.Len(t, a.posted, 2)
}
func TestFlowCardPendingFlushKeepsCompletedStep(t *testing.T) {
	ctx := context.Background()
	s, a, c := newFlowCardService(t)
	start := appendCardEvent(t, s, c, app.FlowRunEvent{Type: "session.flow.started"})
	s.deliverSessionFlowEvent(ctx, start)
	step := appendCardEvent(t, s, c, app.FlowRunEvent{Type: "session.flow.step", StepID: "one", Status: "completed"})
	s.deliverSessionFlowEvent(ctx, step)
	require.False(t, flowCardWasDelivered(t, s, step))
	require.Len(t, s.flowCardPending, 1)
	// A completion with no following event must still flush after pacing.
	time.Sleep(2100 * time.Millisecond)
	s.flushFlowCards(ctx)
	require.Len(t, a.edited, 1)
	require.Equal(t, "completed", a.edited[0].Steps[0].Status)
	require.True(t, flowCardWasDelivered(t, s, step))
}

func TestFlowCardRefreshesGeneratedPlanAndKeepsPriorOutput(t *testing.T) {
	ctx := context.Background()
	s, a, c := newFlowCardService(t)
	planning := appendCardEvent(t, s, c, app.FlowRunEvent{Type: "session.flow.planning"})
	s.deliverSessionFlowEvent(ctx, planning)
	saved, err := s.app.FlowRuns.Get(ctx, c.run.ID)
	require.NoError(t, err)
	snapshot := json.RawMessage(`{"manifest":{"Definition":{"id":"generated-review","Spec":{"Steps":[{"id":"review","agent":"reviewer"},{"id":"test","agent":"coder"}]}}}}`)
	_, err = s.app.FlowRuns.SaveSnapshot(ctx, c.lease, snapshot, saved.Version)
	require.NoError(t, err)
	started := appendCardEvent(t, s, c, app.FlowRunEvent{Type: "session.flow.step", StepID: "review", Status: "waiting_for_input"})
	s.deliverSessionFlowEvent(ctx, started)
	require.Len(t, a.edited, 1)
	require.Len(t, a.edited[0].Steps, 2)
	require.Equal(t, 2, a.edited[0].TotalSteps)
	require.Equal(t, "pending", a.edited[0].Steps[1].Status)
}
func TestFlowCardOldRunReplayDoesNotReadNewConversationRun(t *testing.T) {
	ctx := context.Background()
	s, a, c := newFlowCardService(t)
	old := appendCardEvent(t, s, c, app.FlowRunEvent{Type: "session.flow.completed", FlowID: "old-flow"})
	require.NoError(t, s.app.FlowRuns.Release(ctx, c.lease, "completed"))
	next, _, err := s.app.FlowRuns.Create(ctx, flowrun.Run{ConversationID: "S1", InvocationID: "new-invoke", Kind: "generated", Snapshot: json.RawMessage(`{}`)})
	require.NoError(t, err)
	c.run = next
	c.lease = flowrun.Lease{}
	s.deliverSessionFlowEvent(ctx, old)
	require.Len(t, a.posted, 1)
	require.Equal(t, old.RunID, a.posted[0].RunID)
	require.Equal(t, "old-flow", a.posted[0].FlowID)
}
func TestFlowCardMissingMessageReplacement(t *testing.T) {
	ctx := context.Background()
	s, a, c := newFlowCardService(t)
	start := appendCardEvent(t, s, c, app.FlowRunEvent{Type: "session.flow.started"})
	s.deliverSessionFlowEvent(ctx, start)
	a.editFailure = bridge.ErrFlowMessageNotFound
	final := appendCardEvent(t, s, c, app.FlowRunEvent{Type: "session.flow.completed"})
	s.deliverSessionFlowEvent(ctx, final)
	require.Len(t, a.posted, 2)
	require.True(t, flowCardWasDelivered(t, s, final))
	card, err := s.app.FlowRuns.GetCard(ctx, c.run.ID, flowCardRecipient(bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "D1"}))
	require.NoError(t, err)
	require.Equal(t, "D1/card-2", card.MessageToken)
}
func TestSessionFlowAdapterErrorIsNotAcknowledged(t *testing.T) {
	for _, channel := range []string{"external", "slack"} {
		t.Run(channel, func(t *testing.T) {
			s, _ := newOrchestratorForTest(t)
			a := newStubAdapter(channel, "default")
			s.adapters[adapterKey(channel, "default")] = a
			_, err := s.store.UpsertBinding(context.Background(), store.Binding{ProjectID: "proj", Channel: channel, IdentityID: "default", PeerID: "D1", SessionID: "S1"})
			require.NoError(t, err)
			c := &testSessionFlows{}
			s.sessionFlows = c
			a.sendErr = errors.New("failed platform request")
			s.deliverSessionFlowEvent(context.Background(), app.FlowRunEvent{ID: "failed-send", RunID: "run", ConversationID: "S1", Type: "session.flow.completed"})
			require.Empty(t, c.receipts)
		})
	}
}
func TestFlowCardLongPlanRetainsLateFailureAndOutputOmission(t *testing.T) {
	p := bridge.FlowProgress{TotalSteps: 300}
	for i := 0; i < 256; i++ {
		p.Steps = append(p.Steps, bridge.FlowProgressStep{ID: fmt.Sprint(i), Status: "pending"})
	}
	foldFlowProgress(&p, app.FlowRunEvent{Type: "session.flow.step", StepID: "299", Status: "failed", Output: "late failure"})
	require.Equal(t, "late failure", p.Error)
	require.Equal(t, "299", p.Steps[255].ID)
	for i := range p.Steps {
		p.Steps[i].Output = string(make([]byte, 3000))
	}
	foldFlowProgress(&p, app.FlowRunEvent{Type: "session.flow.completed"})
	require.Contains(t, p.Steps[0].Output, "omitted")
	data, err := json.Marshal(p)
	require.NoError(t, err)
	require.Less(t, len(data), 256*1024)
}

func TestFlowCardSummaryUsesExecutionOrder(t *testing.T) {
	p := bridge.FlowProgress{Steps: []bridge.FlowProgressStep{{ID: "first", Status: "pending"}, {ID: "later", Status: "pending"}}}
	foldFlowProgress(&p, app.FlowRunEvent{Type: "session.flow.step", StepID: "later", Status: "completed", Output: "old result"})
	foldFlowProgress(&p, app.FlowRunEvent{Type: "session.flow.step", StepID: "first", Status: "completed", Output: "latest result"})
	foldFlowProgress(&p, app.FlowRunEvent{Type: "session.flow.completed"})
	require.Equal(t, "latest result", flowCardPresentation(p).Summary)
	foldFlowProgress(&p, app.FlowRunEvent{Type: "session.flow.step", StepID: "first", Status: "completed", Output: `{"summary":"structured human summary"}`, IsStructOutput: true})
	require.Equal(t, "structured human summary", flowCardPresentation(p).Summary)
}

func TestFlowCardLateFailureSurvivesNextLateRunningStep(t *testing.T) {
	p := bridge.FlowProgress{TotalSteps: 300}
	for i := 0; i < 256; i++ {
		p.Steps = append(p.Steps, bridge.FlowProgressStep{ID: fmt.Sprint(i), Status: "completed"})
	}
	foldFlowProgress(&p, app.FlowRunEvent{Type: "session.flow.step", StepID: "257", Status: "failed", Output: "failed late"})
	foldFlowProgress(&p, app.FlowRunEvent{Type: "session.flow.step", StepID: "258", Status: "running"})
	statuses := map[string]string{}
	for _, step := range p.Steps {
		statuses[step.ID] = step.Status
	}
	require.Equal(t, "failed", statuses["257"])
	require.Equal(t, "running", statuses["258"])
}

func TestFlowCardLargePlanWindowPreservesOrder(t *testing.T) {
	p := bridge.FlowProgress{}
	definition := flow.DynamicManifest{Definition: flow.Flow{ID: "large"}}
	for i := 0; i < 300; i++ {
		definition.Definition.Spec.Steps = append(definition.Definition.Spec.Steps, flow.Step{ID: fmt.Sprintf("step-%03d", i)})
	}
	data, err := json.Marshal(map[string]any{"manifest": definition})
	require.NoError(t, err)
	record := flowrun.Run{Snapshot: data}
	require.NoError(t, refreshFlowCardPlan(&p, record))
	for i := range p.Steps {
		p.Steps[i].Status = "completed"
	}
	foldFlowProgress(&p, app.FlowRunEvent{Type: "session.flow.step", StepID: "step-257", Status: "failed", Output: "late failure"})
	foldFlowProgress(&p, app.FlowRunEvent{Type: "session.flow.step", StepID: "step-258", Status: "running"})
	require.NoError(t, refreshFlowCardPlan(&p, record))
	require.Equal(t, "step-257", p.Steps[254].ID)
	require.Equal(t, "failed", p.Steps[254].Status)
	require.Equal(t, "step-258", p.Steps[255].ID)
	require.Contains(t, bridge.FlowProgressTitle(p), "tracked steps")
	foldFlowProgress(&p, app.FlowRunEvent{Type: "session.flow.suspended"})
	require.Equal(t, "suspended", p.Steps[255].Status)
}
