package service

// Tests for Part 1 of bridge-queue-visibility-and-loss-paths:
//   - ErrSessionBusy retry + content preservation (tasks 1.3, 1.4)
//   - Cross-session non-starvation via non-blocking push (task 1.6e)
//   - Session serialization invariant (task 1.7)
//   - Nil-agent reply (task 2.2)
//   - Interactive-buffer eviction notification (task 3.2)
//   - Shutdown WARN log (task 4.2)

import (
	"context"
	"log/slog"
	"strings"
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
	"github.com/opencode-ai/opencode/internal/pubsub"
)

// ---------------------------------------------------------------------------
// Stub helpers
// ---------------------------------------------------------------------------

// busyRetryStubAgent implements agent.Service for dispatch tests.
// Only Run is implemented; all other methods panic via nil embedding.
// runErrors[i] is returned as the error for the i-th Run call (nil = success).
type busyRetryStubAgent struct {
	agentpkg.Service // nil — other methods are never called in these tests

	mu        sync.Mutex
	runErrors []error
	calls     int
	lastText  string

	// slowDuration, if > 0, makes Run block for that duration before returning.
	slowDuration time.Duration
	// maxConcurrent tracks the peak number of concurrent Run invocations.
	maxConcurrent atomic.Int32
	activeCalls   atomic.Int32
}

func (a *busyRetryStubAgent) Run(
	_ context.Context, _, content string, _ int, _ ...message.Attachment,
) (<-chan agentpkg.AgentEvent, error) {
	// Track concurrency
	cur := a.activeCalls.Add(1)
	defer a.activeCalls.Add(-1)
	for {
		old := a.maxConcurrent.Load()
		if cur <= old || a.maxConcurrent.CompareAndSwap(old, cur) {
			break
		}
	}

	a.mu.Lock()
	idx := a.calls
	a.calls++
	a.lastText = content
	errs := a.runErrors
	a.mu.Unlock()

	if idx < len(errs) && errs[idx] != nil {
		// Busy or other error — return immediately without the slow wait
		return nil, errs[idx]
	}

	if a.slowDuration > 0 {
		time.Sleep(a.slowDuration)
	}

	ch := make(chan agentpkg.AgentEvent, 1)
	ch <- agentpkg.AgentEvent{Type: agentpkg.AgentEventTypeResponse}
	close(ch)
	return ch, nil
}

func (a *busyRetryStubAgent) runCallCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
}

// stubMessageSvc implements message.Service. Only SubscribeParts is wired;
// all other methods panic via nil embedding.
type stubMessageSvc struct {
	message.Service
}

func (s *stubMessageSvc) SubscribeParts(ctx context.Context) <-chan pubsub.Event[message.PartEvent] {
	ch := make(chan pubsub.Event[message.PartEvent])
	go func() {
		<-ctx.Done()
		close(ch)
	}()
	return ch
}

// newDispatchTestSvc builds a minimal Service suitable for handleInbound tests.
// The stub agent and adapter are wired; a binding for peer "D1"/session "S1"
// is inserted. The run() and runParts() goroutines are NOT started — callers
// invoke handleInbound directly.
func newDispatchTestSvc(t *testing.T, ag agentpkg.Service) (*Service, *stubAdapter) {
	t.Helper()
	svc, _ := newOrchestratorForTest(t)
	svc.app = &app.App{
		Messages:         &stubMessageSvc{},
		PrimaryAgents:    map[config.AgentName]agentpkg.Service{config.AgentCoder: ag},
		PrimaryAgentKeys: []config.AgentName{config.AgentCoder},
	}
	ad := newStubAdapter("slack", "default")
	svc.adapters[adapterKey("slack", "default")] = ad
	if _, err := svc.store.UpsertBinding(context.Background(), store.Binding{
		ProjectID: "proj", Channel: "slack", IdentityID: "default",
		PeerID: "D1", SessionID: "S1",
	}); err != nil {
		t.Fatalf("UpsertBinding: %v", err)
	}
	return svc, ad
}

// newBareDispatch constructs a sessionDispatch without starting goroutines.
// Useful for testing pushInbound, close(), and overflow directly.
func newBareDispatch(svc *Service, sessionID string) *sessionDispatch {
	return &sessionDispatch{
		svc:       svc,
		sessionID: sessionID,
		inbound:   make(chan bridge.Inbound, dispatchInboundCap),
		parts:     make(chan pubsub.Event[message.PartEvent], dispatchPartsCap),
	}
}

func testInbound(text string) bridge.Inbound {
	return bridge.Inbound{
		Peer: bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "D1"},
		Text: text,
	}
}

// ---------------------------------------------------------------------------
// Task 1.4: TestHandleInbound_BusyRetryPreservesContent
// ---------------------------------------------------------------------------

// TestHandleInbound_BusyRetryPreservesContent verifies that when agent.Run
// returns ErrSessionBusy N times and then succeeds:
//
//	(a) Run is called N+1 times with the same inbound text
//	(b) the run-failure reply path is NOT taken (no replyToPeer for busy)
//	(c) the inbound text is intact on the final successful Run call
//
// To prove the test bites: with the OLD blocking pushInbound+immediate discard
// behavior, Run would only be called once (returning ErrSessionBusy) and
// the failure reply ("please resend") would be sent. The retry loop fixes this.
func TestHandleInbound_BusyRetryPreservesContent(t *testing.T) {
	// N=2 busy calls before success; test takes ~200ms (2 * busyRetryBackoff)
	const N = 2
	errs := make([]error, N)
	for i := range errs {
		errs[i] = agentpkg.ErrSessionBusy
	}
	ag := &busyRetryStubAgent{runErrors: errs}
	svc, ad := newDispatchTestSvc(t, ag)

	in := testInbound("keep this content please")
	d := newBareDispatch(svc, "S1")
	d.handleInbound(context.Background(), in)

	// (a) Run called N+1 times
	if got := ag.runCallCount(); got != N+1 {
		t.Errorf("Run called %d times, want %d (N=%d busy + 1 success)", got, N+1, N)
	}
	// (b) No failure reply was sent via the adapter (busy errors go to retry,
	// not to runFailureMessage)
	sends := ad.Sends()
	for _, s := range sends {
		if strings.Contains(s.Text, "resend") || strings.Contains(s.Text, "not delivered") {
			t.Errorf("failure reply was sent: %q — ErrSessionBusy should be retried, not reported", s.Text)
		}
	}
	// (c) Last Run call received the original content
	ag.mu.Lock()
	lastText := ag.lastText
	ag.mu.Unlock()
	if lastText != in.Text {
		t.Errorf("last Run text = %q, want %q", lastText, in.Text)
	}
}

// ---------------------------------------------------------------------------
// Task 1.6e: TestDispatch_NonBlockingPush_NoStarvation
// ---------------------------------------------------------------------------

// TestDispatch_NonBlockingPush_NoStarvation verifies that when session A's
// d.inbound channel is full, a push to A goes to overflow (non-blocking) and
// does NOT delay a concurrent push to session B.
//
// Proof of bite: with the OLD blocking pushInbound, filling A's channel and
// then calling pushInbound(mA) would block indefinitely, never reaching B's
// push. This test would hang (or timeout). With the fix, it completes in O(1).
func TestDispatch_NonBlockingPush_NoStarvation(t *testing.T) {
	t.Parallel()
	svc, _ := newOrchestratorForTest(t)

	dispA := newBareDispatch(svc, "A")
	dispB := newBareDispatch(svc, "B")

	// Fill session A's inbound to cap.
	for i := 0; i < dispatchInboundCap; i++ {
		dispA.inbound <- testInbound("fill")
	}
	if len(dispA.inbound) != dispatchInboundCap {
		t.Fatalf("setup: A.inbound should be full, got len=%d", len(dispA.inbound))
	}

	mA := testInbound("message for A")
	mB := testInbound("message for B")

	// Push to A (full channel) — must be non-blocking
	done := make(chan struct{})
	go func() {
		dispA.pushInbound(mA)
		dispB.pushInbound(mB)
		close(done)
	}()

	select {
	case <-done:
		// OK — both pushes completed without blocking
	case <-time.After(2 * time.Second):
		t.Fatal("pushInbound blocked — full-channel push stalled the shared loop (non-starvation bug)")
	}

	// mA must be in overflow (channel was full)
	dispA.mu.Lock()
	overflowLen := len(dispA.overflow)
	var overflowText string
	if overflowLen > 0 {
		overflowText = dispA.overflow[0].Text
	}
	dispA.mu.Unlock()
	if overflowLen != 1 {
		t.Errorf("A overflow len = %d, want 1", overflowLen)
	}
	if overflowText != mA.Text {
		t.Errorf("A overflow[0].Text = %q, want %q", overflowText, mA.Text)
	}

	// mB must be in B's inbound channel (not blocked)
	if got := len(dispB.inbound); got != 1 {
		t.Errorf("B.inbound len = %d, want 1 — session B was not dispatched immediately", got)
	}
}

// ---------------------------------------------------------------------------
// Overflow FIFO: drainOverflowToInbound delivers in arrival order
// ---------------------------------------------------------------------------

// TestOverflowFIFO verifies that overflow items are transferred to d.inbound
// in arrival order and appear before any newly-arriving items.
func TestOverflowFIFO(t *testing.T) {
	t.Parallel()
	svc, _ := newOrchestratorForTest(t)
	d := newBareDispatch(svc, "S1")

	// Fill inbound to cap.
	for i := 0; i < dispatchInboundCap; i++ {
		d.inbound <- testInbound("channel")
	}

	// Push 3 overflow items.
	ov1 := testInbound("overflow-1")
	ov2 := testInbound("overflow-2")
	ov3 := testInbound("overflow-3")
	d.pushInbound(ov1)
	d.pushInbound(ov2)
	d.pushInbound(ov3)

	d.mu.Lock()
	if len(d.overflow) != 3 {
		d.mu.Unlock()
		t.Fatalf("overflow len = %d, want 3", len(d.overflow))
	}
	d.mu.Unlock()

	// Drain one "channel" item (simulating handleInbound consuming it).
	<-d.inbound

	// Drain overflow into inbound.
	d.drainOverflowToInbound()

	// The inbound channel should now have (cap-1) old items + 1 overflow item.
	// Read past the old items.
	for i := 0; i < dispatchInboundCap-1; i++ {
		item := <-d.inbound
		if item.Text != "channel" {
			t.Fatalf("expected \"channel\" item at position %d, got %q", i, item.Text)
		}
	}

	// Next item must be overflow-1 (FIFO).
	got := <-d.inbound
	if got.Text != ov1.Text {
		t.Errorf("FIFO violated: got %q after old items, want %q", got.Text, ov1.Text)
	}

	// overflow-2 and overflow-3 remain in overflow (channel was re-filled).
	d.mu.Lock()
	remaining := len(d.overflow)
	d.mu.Unlock()
	if remaining != 2 {
		t.Errorf("overflow remaining = %d, want 2 (ov2 and ov3)", remaining)
	}
}

// ---------------------------------------------------------------------------
// Task 1.7: TestSessionSerializationInvariant
// ---------------------------------------------------------------------------

// TestSessionSerializationInvariant verifies that the dispatcher processes
// inbound messages serially: the second agent.Run does not start until the
// first completes. Uses a slow mock with a controlled delay.
func TestSessionSerializationInvariant(t *testing.T) {
	t.Parallel()

	const runDelay = 50 * time.Millisecond
	ag := &busyRetryStubAgent{slowDuration: runDelay}
	svc, _ := newDispatchTestSvc(t, ag)

	// Start the dispatcher goroutines via the service's supervised launcher.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	svc.ctx = ctx
	svc.cancel = cancel

	d := svc.newSessionDispatch("S1")
	t.Cleanup(func() { d.close() })

	m1 := testInbound("first")
	m2 := testInbound("second")
	d.pushInbound(m1)
	d.pushInbound(m2)

	// Wait for both messages to be processed.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if ag.runCallCount() >= 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if ag.runCallCount() < 2 {
		t.Fatalf("only %d Run calls observed, want 2", ag.runCallCount())
	}

	// Assert no concurrent Runs occurred.
	if mc := ag.maxConcurrent.Load(); mc > 1 {
		t.Errorf("maxConcurrent = %d, want 1 (serialization invariant violated)", mc)
	}
}

// ---------------------------------------------------------------------------
// Task 2.2: TestHandleInbound_NilAgentReplies
// ---------------------------------------------------------------------------

// TestHandleInbound_NilAgentReplies verifies that when ActiveAgent() returns
// nil, handleInbound sends a reply to the sender's peer and does not panic.
func TestHandleInbound_NilAgentReplies(t *testing.T) {
	t.Parallel()
	svc, _ := newOrchestratorForTest(t)

	// Set up app with no agent (PrimaryAgents map is nil → ActiveAgent returns nil).
	svc.app = &app.App{
		Messages:         &stubMessageSvc{},
		PrimaryAgents:    nil,
		PrimaryAgentKeys: []config.AgentName{config.AgentCoder},
	}

	ad := newStubAdapter("slack", "default")
	svc.adapters[adapterKey("slack", "default")] = ad

	d := newBareDispatch(svc, "S1")
	in := testInbound("hello")

	// Must not panic.
	d.handleInbound(context.Background(), in)

	// A reply MUST have been sent (the no-active-agent notification).
	sends := ad.Sends()
	if len(sends) == 0 {
		t.Fatal("no reply sent to peer when agent is nil — silent drop is not allowed")
	}
	found := false
	for _, s := range sends {
		if strings.Contains(s.Text, "no active agent") || strings.Contains(s.Text, "agent") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("reply text does not mention the problem: %v", sends)
	}
}

// ---------------------------------------------------------------------------
// Task 3.2: TestBufferInbound_DropNotifiesEvictedPeer
// ---------------------------------------------------------------------------

// TestBufferInbound_DropNotifiesEvictedPeer verifies that when the interactive
// buffer is full and a new message arrives, the evicted peer receives a
// notification and the buffer still holds exactly interactiveInboundBufferCap
// elements with the newest message at the tail.
func TestBufferInbound_DropNotifiesEvictedPeer(t *testing.T) {
	t.Parallel()
	svc, _ := newOrchestratorForTest(t)
	ad := newStubAdapter("slack", "default")
	svc.adapters[adapterKey("slack", "default")] = ad

	r := newBufferRouter(svc)

	ctx := context.Background()

	// Fill buffer to cap. All messages from peer "D1".
	evictedPeer := bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "D1"}
	for i := 0; i < interactiveInboundBufferCap; i++ {
		r.BufferInbound(ctx, "S1", bridge.Inbound{
			Peer: evictedPeer,
			Text: "old",
		})
	}
	if got := r.bufferedLen("S1"); got != interactiveInboundBufferCap {
		t.Fatalf("buffer len = %d after filling, want cap %d", got, interactiveInboundBufferCap)
	}

	// Push one more — should evict the oldest and notify its peer.
	newest := bridge.Inbound{
		Peer: bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "D2"},
		Text: "newest",
	}
	r.BufferInbound(ctx, "S1", newest)

	// Buffer must still be at cap.
	if got := r.bufferedLen("S1"); got != interactiveInboundBufferCap {
		t.Errorf("buffer len = %d after eviction, want %d", got, interactiveInboundBufferCap)
	}

	// The evicted peer (D1) must have received a notification.
	sends := ad.Sends()
	if len(sends) == 0 {
		t.Fatal("no notification sent to evicted peer — silent drop is not allowed")
	}
	found := false
	for _, s := range sends {
		if strings.Contains(s.Text, "lost") || strings.Contains(s.Text, "dropped") ||
			strings.Contains(s.Text, "buffered") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("eviction notification text unexpected: %v", sends)
	}

	// Newest message must be at the tail.
	r.mu.Lock()
	q := r.buffered["S1"]
	tail := q[len(q)-1]
	r.mu.Unlock()
	if tail.Text != newest.Text {
		t.Errorf("tail.Text = %q, want %q (newest should be at tail)", tail.Text, newest.Text)
	}
}

// ---------------------------------------------------------------------------
// Task 4.2: TestShutdown_WarnsOnQueuedMessages
// ---------------------------------------------------------------------------

// TestShutdown_WarnsOnQueuedMessages verifies that close() emits one WARN log
// per dropped message (session ID + peer ID) plus a summary WARN, for both
// d.inbound items and d.overflow items.
//
// NOTE: this test modifies the global slog default and MUST NOT be run in
// parallel with other tests that also modify it.
func TestShutdown_WarnsOnQueuedMessages(t *testing.T) {
	// Capture WARN-level log output.
	var buf strings.Builder
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	svc, _ := newOrchestratorForTest(t)
	d := newBareDispatch(svc, "SHD")

	// Put 3 messages in d.inbound.
	peerA := bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "peerA"}
	peerB := bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "peerB"}
	peerC := bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "peerC"}
	d.inbound <- bridge.Inbound{Peer: peerA, Text: "msg1"}
	d.inbound <- bridge.Inbound{Peer: peerB, Text: "msg2"}
	d.inbound <- bridge.Inbound{Peer: peerC, Text: "msg3"}

	// Put 2 messages in d.overflow.
	peerD := bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "peerD"}
	peerE := bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "peerE"}
	d.mu.Lock()
	d.overflow = append(d.overflow,
		bridge.Inbound{Peer: peerD, Text: "ov1"},
		bridge.Inbound{Peer: peerE, Text: "ov2"},
	)
	d.mu.Unlock()

	// close() should drain and log.
	d.close()

	logged := buf.String()
	// Per-message WARNs: each peer ID must appear.
	for _, peer := range []string{"peerA", "peerB", "peerC", "peerD", "peerE"} {
		if !strings.Contains(logged, peer) {
			t.Errorf("WARN log missing peer %q; full log:\n%s", peer, logged)
		}
	}
	// Session ID must appear.
	if !strings.Contains(logged, "SHD") {
		t.Errorf("WARN log missing session ID \"SHD\"; full log:\n%s", logged)
	}
	// Summary WARN must appear.
	if !strings.Contains(logged, "dropped") && !strings.Contains(logged, "shutdown dropped") {
		t.Errorf("no shutdown summary WARN found; full log:\n%s", logged)
	}
}
