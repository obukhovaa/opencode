package service

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/bridge/store"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/pubsub"
	"github.com/opencode-ai/opencode/internal/question"
)

// newBufferRouter builds a router with the interactive buffer initialised
// but NO goroutines launched — tests drive handleNewRequest / BufferInbound
// directly so they stay deterministic (no reliance on the pubsub subscriber
// goroutine).
func newBufferRouter(svc *Service) *QuestionRouter {
	return &QuestionRouter{
		svc:      svc,
		pending:  map[string]*pendingQuestion{},
		buffered: map[string][]bridge.Inbound{},
	}
}

func (r *QuestionRouter) bufferedLen(sessionID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.buffered[sessionID])
}

// TestBufferInbound_FIFOAndDropOldest verifies the queue is FIFO and drops
// the oldest entry once the cap is hit (bounded memory).
func TestBufferInbound_FIFOAndDropOldest(t *testing.T) {
	t.Parallel()
	r := newBufferRouter(nil)

	for i := 0; i < interactiveInboundBufferCap+2; i++ {
		r.BufferInbound("S1", bridge.Inbound{Text: strconv.Itoa(i)})
	}
	if got := r.bufferedLen("S1"); got != interactiveInboundBufferCap {
		t.Fatalf("buffer len = %d, want cap %d", got, interactiveInboundBufferCap)
	}
	// The two oldest ("0","1") were dropped, so the head is now "2".
	r.mu.Lock()
	head, ok := r.popBufferedLocked("S1")
	r.mu.Unlock()
	if !ok || head.Text != "2" {
		t.Fatalf("head = %q ok=%v, want \"2\"", head.Text, ok)
	}
}

// TestClearSession_DropsPendingAndBuffered verifies unbind cleanup removes
// both the pending question and the buffered inbounds for a session.
func TestClearSession_DropsPendingAndBuffered(t *testing.T) {
	t.Parallel()
	r := newBufferRouter(nil)
	r.pending["S1"] = &pendingQuestion{requestID: "req-1"}
	r.BufferInbound("S1", bridge.Inbound{Text: "hi"})

	r.ClearSession("S1")

	r.mu.Lock()
	_, hasPending := r.pending["S1"]
	nBuf := len(r.buffered["S1"])
	r.mu.Unlock()
	if hasPending {
		t.Error("pending not cleared")
	}
	if nBuf != 0 {
		t.Errorf("buffered len = %d, want 0", nBuf)
	}
}

// TestDispatchInbound_InteractiveNoPending_Buffers proves that when a session
// is inside an interactive flow step and no question is pending, a reviewer
// message is BUFFERED rather than dispatched to app.ActiveAgent() (which
// would hijack the step). Also the control case: a non-interactive session
// must NOT buffer — this is the property that keeps daemon agents and
// non-flow bridge chat unaffected.
func TestDispatchInbound_InteractiveNoPending_Buffers(t *testing.T) {
	t.Parallel()
	svc, _ := newOrchestratorForTest(t)
	svc.app = &app.App{
		Permissions: permission.NewPermissionService(),
		Questions:   question.NewService(),
	}
	// Start so the fall-through (control) case has a live dispatcher ctx;
	// Start installs the real questionRouter (buffer map initialised).
	if err := svc.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Direct binding row so resolveBinding maps peer D1 → session S1
	// without needing the adapter/dispatcher machinery.
	if _, err := svc.store.UpsertBinding(context.Background(), store.Binding{
		ProjectID: "proj", Channel: "slack", IdentityID: "default",
		PeerID: "D1", SessionID: "S1",
	}); err != nil {
		t.Fatalf("UpsertBinding: %v", err)
	}
	in := bridge.Inbound{
		Peer: bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "D1"},
		Text: "please reword bullet 1",
	}

	// Control: NOT interactive → must fall through, not buffer.
	svc.dispatchInbound(context.Background(), in)
	if n := svc.questionRouter.bufferedLen("S1"); n != 0 {
		t.Fatalf("non-interactive session buffered %d messages, want 0 (daemon/non-flow must be unaffected)", n)
	}

	// Now mark interactive (as the flow engine does at step start) → buffer.
	svc.app.Permissions.MarkInteractiveSession("S1")
	svc.dispatchInbound(context.Background(), in)
	if n := svc.questionRouter.bufferedLen("S1"); n != 1 {
		t.Fatalf("interactive session buffered %d messages, want 1", n)
	}
}

// TestHandleNewRequest_DrainsBufferedIntoReply proves the core behaviour: a
// buffered reviewer message auto-answers the next question the flow agent
// asks (so the reply stays inside the flow agent's Run), and the question is
// NOT fanned out to peers.
func TestHandleNewRequest_DrainsBufferedIntoReply(t *testing.T) {
	t.Parallel()
	svc, _ := newOrchestratorForTest(t)
	svc.app = &app.App{
		Permissions: permission.NewPermissionService(),
		Questions:   question.NewService(),
	}
	r := newBufferRouter(svc)
	svc.questionRouter = r

	// Bound peer via a stub adapter so we can assert the question was NOT
	// fanned out (auto-answer path returns before fan-out).
	ad := newStubAdapter("slack", "default")
	svc.adapters["slack/default"] = ad
	if _, err := svc.store.UpsertBinding(context.Background(), store.Binding{
		ProjectID: "proj", Channel: "slack", IdentityID: "default",
		PeerID: "D1", SessionID: "S1",
	}); err != nil {
		t.Fatalf("UpsertBinding: %v", err)
	}

	// A reviewer message arrived while no question was pending.
	r.BufferInbound("S1", bridge.Inbound{
		Peer: bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "D1"},
		Text: "Approve",
	})

	ctx := context.Background()
	sub := svc.app.Questions.Subscribe(ctx)

	ansCh := make(chan [][]string, 1)
	go func() {
		ans, _ := svc.app.Questions.Ask(ctx, "S1", []question.Prompt{{
			Question: "Approve?",
			Options:  []question.Option{{Label: "Approve"}, {Label: "Reject"}},
		}})
		ansCh <- ans
	}()

	// Capture the CreatedEvent the agent's Ask published, then drive
	// handleNewRequest directly (deterministic — no reliance on the router
	// goroutine).
	var req question.Request
	select {
	case ev := <-sub:
		if ev.Type != pubsub.CreatedEvent {
			t.Fatalf("first event type = %v, want Created", ev.Type)
		}
		req = ev.Payload
	case <-time.After(2 * time.Second):
		t.Fatal("no question CreatedEvent observed")
	}
	r.handleNewRequest(ctx, req)

	select {
	case ans := <-ansCh:
		if len(ans) != 1 || len(ans[0]) != 1 || ans[0][0] != "Approve" {
			t.Fatalf("Ask returned %v, want [[Approve]] (buffered message should answer it)", ans)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Ask never returned — buffered message did not drain into the question reply")
	}

	if n := r.bufferedLen("S1"); n != 0 {
		t.Errorf("buffer not drained: %d left", n)
	}
	if sends := ad.Sends(); len(sends) != 0 {
		t.Errorf("question was fanned out to peer (%d sends); auto-answer should skip fan-out", len(sends))
	}
}
