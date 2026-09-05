package service

// Tests for Part 2 of bridge-queue-visibility-and-loss-paths:
//   - QueuedAcknowledger ack lifecycle in handleInbound (task 8.5)
//   - Config-gate proof: ack suppressed when disabled, sent when enabled
//   - TestBufferInbound_NoDrainWithoutQuestion (task 9.2)

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/bridge/store"
	"github.com/opencode-ai/opencode/internal/config"
	agentpkg "github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/message"
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
//	(b) UpdateQueuedAck is NOT called again while the reported position is
//	    unchanged — the retry loop ticks every 100 ms, and re-editing the same
//	    text thousands of times exhausts platform edit rate limits (Telegram
//	    rejects an unchanged edit outright with "message is not modified")
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
	// (b) The position never changes in this scenario (nothing else is queued),
	// so the only update must be the resolve. No redundant same-position edits.
	if len(updates) != 1 {
		t.Errorf("UpdateQueuedAck called %d times, want 1 (resolve only); got %v",
			len(updates), updates)
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

// ---------------------------------------------------------------------------
// Ack edit economy + honest ack state on the non-delivery paths
// ---------------------------------------------------------------------------

// ackPositionAgent returns ErrSessionBusy for a preset number of calls and, on
// the call at index growAt, pushes an extra inbound into the dispatcher so the
// reported queue position changes mid-retry.
type ackPositionAgent struct {
	busyRetryStubAgent
	d      *sessionDispatch
	growAt int
}

func (a *ackPositionAgent) Run(
	ctx context.Context, sid, content string, mt int, atts ...message.Attachment,
) (<-chan agentpkg.AgentEvent, error) {
	a.mu.Lock()
	idx := a.calls
	a.mu.Unlock()
	if idx == a.growAt && a.d != nil {
		a.d.pushInbound(testInbound("filler"))
	}
	return a.busyRetryStubAgent.Run(ctx, sid, content, mt, atts...)
}

// TestQueuedAck_UpdatesOnlyOnPositionChange proves the ack is edited when the
// position actually changes, and not on the retries where it does not. Without
// the position-change gate the 100 ms retry loop issues an identical edit per
// tick — thousands per queued message against the platform's edit rate limit.
func TestQueuedAck_UpdatesOnlyOnPositionChange(t *testing.T) {
	old := busyAckThreshold
	busyAckThreshold = 0
	defer func() { busyAckThreshold = old }()

	errs := make([]error, 4)
	for i := range errs {
		errs[i] = agentpkg.ErrSessionBusy
	}
	ag := &ackPositionAgent{
		busyRetryStubAgent: busyRetryStubAgent{runErrors: errs},
		growAt:             2,
	}
	svc, ad := newAckDispatchTestSvc(t, ag, true)
	d := newBareDispatch(svc, "S1")
	ag.d = d

	d.handleInbound(context.Background(), testInbound("position-change"))

	if got := ad.AckSendCount(); got != 1 {
		t.Errorf("SendQueuedAck called %d times, want 1", got)
	}
	updates := ad.AckUpdateCalls()
	// Expected: one update for the position change (2), then the resolve (0).
	if len(updates) != 2 {
		t.Fatalf("UpdateQueuedAck calls = %v, want exactly 2 (position change + resolve)", updates)
	}
	if updates[0].Position != 2 {
		t.Errorf("first update position = %d, want 2", updates[0].Position)
	}
	if updates[1].Position != 0 {
		t.Errorf("second update position = %d, want 0 (resolve)", updates[1].Position)
	}
}

// TestQueuedAck_NotResolvedOnRunFailure asserts the ack is NOT resolved to
// "▶ Processing your message now…" when the run never starts: resolving would
// directly contradict the failure reply sent immediately afterwards.
func TestQueuedAck_NotResolvedOnRunFailure(t *testing.T) {
	old := busyAckThreshold
	busyAckThreshold = 0
	defer func() { busyAckThreshold = old }()

	ag := &busyRetryStubAgent{runErrors: []error{
		agentpkg.ErrSessionBusy,
		errors.New("provider exploded"),
	}}
	svc, ad := newAckDispatchTestSvc(t, ag, true)
	d := newBareDispatch(svc, "S1")

	d.handleInbound(context.Background(), testInbound("run-failure"))

	if got := ad.AckSendCount(); got != 1 {
		t.Fatalf("SendQueuedAck called %d times, want 1", got)
	}
	for _, u := range ad.AckUpdateCalls() {
		if u.Position == 0 {
			t.Errorf("ack resolved (position 0) even though the run never started: %v", u)
		}
	}
}

// TestQueuedAck_NotResolvedOnBudgetExpiry asserts the ack is NOT resolved when
// the busy-retry budget expires: the message is re-queued for another attempt,
// so telling the peer "▶ Processing your message now…" would be false.
func TestQueuedAck_NotResolvedOnBudgetExpiry(t *testing.T) {
	oldThreshold := busyAckThreshold
	busyAckThreshold = 0
	defer func() { busyAckThreshold = oldThreshold }()

	errs := make([]error, 64)
	for i := range errs {
		errs[i] = agentpkg.ErrSessionBusy
	}
	ag := &busyRetryStubAgent{runErrors: errs}
	svc, ad := newAckDispatchTestSvc(t, ag, true)
	d := newBareDispatch(svc, "S1")

	// Shrink the retry budget so it expires after the ack has been sent but
	// before Run ever succeeds.
	oldBudget := busyRetryBudget
	busyRetryBudget = 150 * time.Millisecond
	defer func() { busyRetryBudget = oldBudget }()

	d.handleInbound(context.Background(), testInbound("budget-expiry"))

	for _, u := range ad.AckUpdateCalls() {
		if u.Position == 0 {
			t.Errorf("ack resolved (position 0) on budget expiry — message was re-queued, not processed: %v", u)
		}
	}
	// The message must be re-queued, never dropped.
	if len(d.inbound) == 0 && len(d.overflow) == 0 {
		t.Error("inbound was neither re-queued into d.inbound nor overflow")
	}
}

// TestQueuedAck_SurvivesRequeueCycle asserts that when the busy-retry budget
// expires and the inbound is re-queued, the NEXT cycle edits the ack message
// that is already in chat instead of sending a second one. Without the
// dispatcher-level token memo, a session held for hours accumulates one
// orphaned, never-resolved "⏳ queued" message per 5-minute cycle.
func TestQueuedAck_SurvivesRequeueCycle(t *testing.T) {
	oldThreshold := busyAckThreshold
	busyAckThreshold = 0
	defer func() { busyAckThreshold = oldThreshold }()
	oldBudget := busyRetryBudget
	busyRetryBudget = 150 * time.Millisecond
	defer func() { busyRetryBudget = oldBudget }()

	errs := make([]error, 64)
	for i := range errs {
		errs[i] = agentpkg.ErrSessionBusy
	}
	ag := &busyRetryStubAgent{runErrors: errs}
	svc, ad := newAckDispatchTestSvc(t, ag, true)
	d := newBareDispatch(svc, "S1")

	in := testInbound("requeue-ack")
	// Cycle 1: ack sent, budget expires, inbound re-queued.
	d.handleInbound(context.Background(), in)
	if got := ad.AckSendCount(); got != 1 {
		t.Fatalf("cycle 1: SendQueuedAck called %d times, want 1", got)
	}
	// Cycle 2: same peer, still busy — must reuse the existing ack.
	d.handleInbound(context.Background(), in)
	if got := ad.AckSendCount(); got != 1 {
		t.Errorf("cycle 2: SendQueuedAck called %d times total, want 1 (ack must be reused, not re-sent)", got)
	}
	for _, u := range ad.AckUpdateCalls() {
		if u.Token != "token-1" {
			t.Errorf("update used token %q, want the original \"token-1\"", u.Token)
		}
	}
}
