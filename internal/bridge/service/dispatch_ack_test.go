package service

// Tests for Part 2 of bridge-queue-visibility-and-loss-paths:
//   - QueuedAcknowledger ack lifecycle in handleInbound (task 8.5)
//   - Config-gate proof: ack suppressed when disabled, sent when enabled
//   - TestBufferInbound_NoDrainWithoutQuestion (task 9.2)

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/bridge/store"
	"github.com/opencode-ai/opencode/internal/config"
	agentpkg "github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/question"
)

// ---------------------------------------------------------------------------
// ackStubAdapter — a stubAdapter that also implements bridge.QueuedAcknowledger
// ---------------------------------------------------------------------------

type ackStubAdapter struct {
	stubAdapter

	mu          sync.Mutex
	sendCalls   []ackSendCall
	updateCalls []ackUpdateCall
}

type ackSendCall struct {
	Peer     bridge.PeerRef
	Position int
}
type ackUpdateCall struct {
	Peer     bridge.PeerRef
	Token    bridge.QueueAckToken
	Position int
}

func newAckStubAdapter(channel, identity string) *ackStubAdapter {
	return &ackStubAdapter{
		stubAdapter: *newStubAdapter(channel, identity),
	}
}

func (a *ackStubAdapter) SendQueuedAck(ctx context.Context, peer bridge.PeerRef, position int) (bridge.QueueAckToken, error) {
	a.mu.Lock()
	a.sendCalls = append(a.sendCalls, ackSendCall{Peer: peer, Position: position})
	a.mu.Unlock()
	return "token-1", nil
}

func (a *ackStubAdapter) UpdateQueuedAck(_ context.Context, peer bridge.PeerRef, token bridge.QueueAckToken, position int) error {
	a.mu.Lock()
	a.updateCalls = append(a.updateCalls, ackUpdateCall{Peer: peer, Token: token, Position: position})
	a.mu.Unlock()
	return nil
}

func (a *ackStubAdapter) AckSendCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.sendCalls)
}

func (a *ackStubAdapter) AckUpdateCalls() []ackUpdateCall {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]ackUpdateCall, len(a.updateCalls))
	copy(out, a.updateCalls)
	return out
}

// newAckDispatchTestSvc builds a service+dispatcher suitable for ack lifecycle tests.
// acks: whether QueueAcknowledgementsEnabled is true.
func newAckDispatchTestSvc(t *testing.T, ag agentpkg.Service, acksEnabled bool) (*Service, *ackStubAdapter) {
	t.Helper()
	svc, _ := newOrchestratorForTest(t)
	svc.cfg = &bridge.Config{QueueAcknowledgementsEnabled: acksEnabled}
	svc.app = &app.App{
		Messages:         &stubMessageSvc{},
		PrimaryAgents:    map[config.AgentName]agentpkg.Service{config.AgentCoder: ag},
		PrimaryAgentKeys: []config.AgentName{config.AgentCoder},
	}
	ad := newAckStubAdapter("slack", "default")
	svc.adapters[adapterKey("slack", "default")] = ad
	if _, err := svc.store.UpsertBinding(context.Background(), store.Binding{
		ProjectID: "proj", Channel: "slack", IdentityID: "default",
		PeerID: "D1", SessionID: "S1",
	}); err != nil {
		t.Fatalf("UpsertBinding: %v", err)
	}
	return svc, ad
}

// ---------------------------------------------------------------------------
// Task 8.5: TestHandleInbound_QueuedAckLifecycle
// ---------------------------------------------------------------------------

// TestHandleInbound_QueuedAckLifecycle is the end-to-end ack lifecycle test:
//
//	(a) SendQueuedAck is called exactly once, after busyAckThreshold elapses
//	(b) UpdateQueuedAck is called on each subsequent retry (with position >= 1)
//	(c) UpdateQueuedAck is called once more with position==0 (resolve) when Run succeeds
//
// busyAckThreshold is set to 0 so the test runs deterministically without sleeping.
func TestHandleInbound_QueuedAckLifecycle(t *testing.T) {
	// busyAckThreshold = 0 means the ack fires immediately on the first retry.
	old := busyAckThreshold
	busyAckThreshold = 0
	defer func() { busyAckThreshold = old }()

	// N=3 busy calls before success; gives 3 retries after the first call.
	const N = 3
	errs := make([]error, N)
	for i := range errs {
		errs[i] = agentpkg.ErrSessionBusy
	}
	ag := &busyRetryStubAgent{runErrors: errs}
	svc, ad := newAckDispatchTestSvc(t, ag, true) // acks enabled

	in := testInbound("ack-lifecycle-message")
	d := newBareDispatch(svc, "S1")
	d.handleInbound(context.Background(), in)

	// (a) SendQueuedAck called exactly once.
	if got := ad.AckSendCount(); got != 1 {
		t.Errorf("SendQueuedAck called %d times, want 1", got)
	}

	updates := ad.AckUpdateCalls()
	// (b) At least N UpdateQueuedAck calls with position > 0.
	positivePosUpdates := 0
	for _, u := range updates {
		if u.Position > 0 {
			positivePosUpdates++
		}
	}
	if positivePosUpdates == 0 {
		t.Errorf("no UpdateQueuedAck calls with position>0; got %v", updates)
	}

	// (c) Final UpdateQueuedAck must have position==0 (resolve sentinel).
	if len(updates) == 0 {
		t.Fatal("UpdateQueuedAck never called")
	}
	last := updates[len(updates)-1]
	if last.Position != 0 {
		t.Errorf("last UpdateQueuedAck position = %d, want 0 (resolve)", last.Position)
	}
	if last.Token != "token-1" {
		t.Errorf("last UpdateQueuedAck token = %q, want \"token-1\"", last.Token)
	}
}

// ---------------------------------------------------------------------------
// Config-gate proof: acks suppressed when disabled, sent when enabled
// ---------------------------------------------------------------------------

// TestQueuedAck_ConfigGate_Disabled proves that with QueueAcknowledgementsEnabled=false
// no SendQueuedAck or UpdateQueuedAck is ever called, even when ErrSessionBusy is returned.
func TestQueuedAck_ConfigGate_Disabled(t *testing.T) {
	old := busyAckThreshold
	busyAckThreshold = 0
	defer func() { busyAckThreshold = old }()

	errs := []error{agentpkg.ErrSessionBusy, agentpkg.ErrSessionBusy}
	ag := &busyRetryStubAgent{runErrors: errs}
	svc, ad := newAckDispatchTestSvc(t, ag, false) // acks DISABLED

	d := newBareDispatch(svc, "S1")
	d.handleInbound(context.Background(), testInbound("gate-off"))

	if got := ad.AckSendCount(); got != 0 {
		t.Errorf("GATE DISABLED: SendQueuedAck called %d times, want 0", got)
	}
	if updates := ad.AckUpdateCalls(); len(updates) != 0 {
		t.Errorf("GATE DISABLED: UpdateQueuedAck called %d times, want 0", len(updates))
	}
}

// TestQueuedAck_ConfigGate_Enabled proves the ack IS sent when the gate is on.
func TestQueuedAck_ConfigGate_Enabled(t *testing.T) {
	old := busyAckThreshold
	busyAckThreshold = 0
	defer func() { busyAckThreshold = old }()

	errs := []error{agentpkg.ErrSessionBusy}
	ag := &busyRetryStubAgent{runErrors: errs}
	svc, ad := newAckDispatchTestSvc(t, ag, true) // acks ENABLED

	d := newBareDispatch(svc, "S1")
	d.handleInbound(context.Background(), testInbound("gate-on"))

	if got := ad.AckSendCount(); got == 0 {
		t.Error("GATE ENABLED: SendQueuedAck was NOT called — ack was not sent")
	}
	updates := ad.AckUpdateCalls()
	if len(updates) == 0 {
		t.Error("GATE ENABLED: UpdateQueuedAck never called (resolve never happened)")
	}
	last := updates[len(updates)-1]
	if last.Position != 0 {
		t.Errorf("GATE ENABLED: last UpdateQueuedAck position = %d, want 0 (resolve)", last.Position)
	}
}

// ---------------------------------------------------------------------------
// Task 9.2: TestBufferInbound_NoDrainWithoutQuestion
// ---------------------------------------------------------------------------

// TestBufferInbound_NoDrainWithoutQuestion verifies that when a question arrives
// for an interactive session that already has buffered messages, handleNewRequest
// auto-answers the question from the HEAD of the buffer (FIFO), leaving the
// rest in the buffer, and does NOT fan the question out to peers.
func TestBufferInbound_NoDrainWithoutQuestion(t *testing.T) {
	// Not parallel: calls newOrchestratorForTest which acquires gooseSerialMu,
	// and also drives question.Service goroutines. Running parallel increases
	// race-detector false-positive risk for the goose migrations mutex.
	svc, _ := newOrchestratorForTest(t)
	svc.app = &app.App{
		Questions: question.NewService(),
	}
	r := newBufferRouter(svc)
	svc.questionRouter = r

	ad := newStubAdapter("slack", "default")
	svc.adapters[adapterKey("slack", "default")] = ad
	if _, err := svc.store.UpsertBinding(context.Background(), store.Binding{
		ProjectID: "proj", Channel: "slack", IdentityID: "default",
		PeerID: "D1", SessionID: "S1",
	}); err != nil {
		t.Fatalf("UpsertBinding: %v", err)
	}

	// Buffer 3 messages in FIFO order: "first", "second", "third".
	ctx := context.Background()
	r.BufferInbound(ctx, "S1", bridge.Inbound{
		Peer: bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "D1"},
		Text: "first",
	})
	r.BufferInbound(ctx, "S1", bridge.Inbound{
		Peer: bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "D1"},
		Text: "second",
	})
	r.BufferInbound(ctx, "S1", bridge.Inbound{
		Peer: bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "D1"},
		Text: "third",
	})
	if got := r.bufferedLen("S1"); got != 3 {
		t.Fatalf("setup: buffered %d, want 3", got)
	}

	// Subscribe so we can capture the question request.
	sub := svc.app.Questions.Subscribe(ctx)

	// Start an Ask in the background.
	type askResult struct {
		answers [][]string
		err     error
	}
	resultCh := make(chan askResult, 1)
	var runCalls atomic.Int32
	go func() {
		runCalls.Add(1)
		ans, err := svc.app.Questions.Ask(ctx, "S1", []question.Prompt{{
			Question: "Approve?",
			Options:  []question.Option{{Label: "first"}, {Label: "other"}},
		}})
		resultCh <- askResult{ans, err}
	}()

	// Capture the CreatedEvent and drive handleNewRequest directly.
	select {
	case ev := <-sub:
		r.handleNewRequest(ctx, ev.Payload)
	case <-time.After(2 * time.Second):
		t.Fatal("no question CreatedEvent observed")
	}

	select {
	case res := <-resultCh:
		if res.err != nil {
			t.Fatalf("Ask returned error: %v", res.err)
		}
		// The HEAD of the buffer ("first") must have answered the question.
		if len(res.answers) == 0 || len(res.answers[0]) == 0 || res.answers[0][0] != "first" {
			t.Errorf("Ask answered with %v, want [[first]] (FIFO head)", res.answers)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Ask never returned")
	}

	// Two messages remain in buffer ("second", "third").
	if got := r.bufferedLen("S1"); got != 2 {
		t.Errorf("buffer len = %d after auto-answer, want 2", got)
	}
	// Question was NOT fanned out to the adapter.
	if sends := ad.Sends(); len(sends) != 0 {
		t.Errorf("question fanned out to adapter (%d sends); auto-answer should suppress fan-out", len(sends))
	}
}
