package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agentpkg "github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/pubsub"

	"github.com/opencode-ai/opencode/internal/config"
)

// ---- fakeAgent stub ---------------------------------------------------------

// fakeAgent is a minimal stub of agent.Service. Only the methods called by the
// drain loop need real implementations; the rest panic if called unexpectedly.
type fakeAgent struct {
	mu      sync.Mutex
	results []runResult
}

type runResult struct {
	err error
}

func (f *fakeAgent) setResults(rs ...runResult) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.results = rs
}

func (f *fakeAgent) IsSessionBusy(_ string) bool { return false }

func (f *fakeAgent) Run(_ context.Context, _ string, _ string, _ int, _ ...message.Attachment) (<-chan agentpkg.AgentEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.results) == 0 {
		ch := make(chan agentpkg.AgentEvent)
		close(ch)
		return ch, nil
	}
	r := f.results[0]
	f.results = f.results[1:]
	if r.err != nil {
		return nil, r.err
	}
	ch := make(chan agentpkg.AgentEvent)
	close(ch)
	return ch, nil
}

// Satisfy the rest of the interface:
func (f *fakeAgent) Subscribe(_ context.Context) <-chan pubsub.Event[agentpkg.AgentEvent] {
	ch := make(chan pubsub.Event[agentpkg.AgentEvent])
	close(ch)
	return ch
}
func (f *fakeAgent) AgentID() config.AgentName               { return "" }
func (f *fakeAgent) Model() models.Model                     { return models.Model{} }
func (f *fakeAgent) Tools() []tools.BaseTool                 { return nil }
func (f *fakeAgent) ResolvedTools() ([]tools.BaseTool, bool) { return nil, false }
func (f *fakeAgent) RunWith(_ context.Context, _ string, _ string, _ int, _ agentpkg.RunOptions, _ ...message.Attachment) (<-chan agentpkg.AgentEvent, error) {
	return nil, nil
}
func (f *fakeAgent) Cancel(_ string)              {}
func (f *fakeAgent) IsBusy() bool                 { return false }
func (f *fakeAgent) TryLockSession(_ string) bool { return true }
func (f *fakeAgent) UnlockSession(_ string)       {}
func (f *fakeAgent) Update(_ config.AgentName, _ models.ModelID) (models.Model, error) {
	return models.Model{}, nil
}
func (f *fakeAgent) Summarize(_ context.Context, _ string) error               { return nil }
func (f *fakeAgent) SummarizeSync(_ context.Context, _ string) error           { return nil }
func (f *fakeAgent) GenerateRecap(_ context.Context, _ string) (string, error) { return "", nil }

// ---- recordAgent ------------------------------------------------------------

// recordAgent extends fakeAgent to record Run call texts in order.
type recordAgent struct {
	fakeAgent
	callMu sync.Mutex
	calls  []string
}

func (r *recordAgent) Run(ctx context.Context, sid string, content string, max int, a ...message.Attachment) (<-chan agentpkg.AgentEvent, error) {
	r.callMu.Lock()
	r.calls = append(r.calls, content)
	r.callMu.Unlock()
	return r.fakeAgent.Run(ctx, sid, content, max, a...)
}

// ---- pauseAgent -------------------------------------------------------------

// pauseAgent blocks each Run until its release channel is closed.
type pauseAgent struct {
	fakeAgent
	callMu  sync.Mutex
	calls   []string
	release chan struct{}
}

func (p *pauseAgent) Run(ctx context.Context, _ string, content string, _ int, _ ...message.Attachment) (<-chan agentpkg.AgentEvent, error) {
	p.callMu.Lock()
	p.calls = append(p.calls, content)
	p.callMu.Unlock()

	select {
	case <-p.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	ch := make(chan agentpkg.AgentEvent)
	close(ch)
	return ch, nil
}

// ---- countRunAgent ----------------------------------------------------------

// countRunAgent counts Run calls and consumes from a preset result list.
type countRunAgent struct {
	fakeAgent
	count  atomic.Int32
	resMu  sync.Mutex
	preset []runResult
}

func (c *countRunAgent) Run(ctx context.Context, sid string, content string, max int, a ...message.Attachment) (<-chan agentpkg.AgentEvent, error) {
	c.count.Add(1)
	c.resMu.Lock()
	if len(c.preset) > 0 {
		r := c.preset[0]
		c.preset = c.preset[1:]
		c.resMu.Unlock()
		if r.err != nil {
			return nil, r.err
		}
		ch := make(chan agentpkg.AgentEvent)
		close(ch)
		return ch, nil
	}
	c.resMu.Unlock()
	return c.fakeAgent.Run(ctx, sid, content, max, a...)
}

// ---- drainCapture -----------------------------------------------------------

type drainCapture struct {
	mu     sync.Mutex
	events []DrainEvent
}

func newDrainApp(ctx context.Context, ag agentpkg.Service) (*App, *drainCapture) {
	a := newTestApp(ctx)
	a.activeAgent = ag
	cap := &drainCapture{}
	a.SetDrainNotifier(func(e DrainEvent) {
		cap.mu.Lock()
		defer cap.mu.Unlock()
		cap.events = append(cap.events, e)
	})
	return a, cap
}

func (c *drainCapture) errors() []error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var errs []error
	for _, e := range c.events {
		if e.Err != nil {
			errs = append(errs, e.Err)
		}
	}
	return errs
}

// ---- helpers ----------------------------------------------------------------

// waitFor polls cond until it returns true or times out.
func waitFor(t *testing.T, desc string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for: %s", desc)
}

// ---- tests ------------------------------------------------------------------

// TestDrain_FIFO_Simple asserts that three queued messages are delivered in
// enqueue order (FIFO).
func TestDrain_FIFO_Simple(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ra := &recordAgent{}
	a, _ := newDrainApp(ctx, ra)

	sid := "fifo-simple"
	a.queueMu.Lock()
	a.queues[sid] = []QueuedMessage{{Text: "first"}, {Text: "second"}, {Text: "third"}}
	a.startDrainWorker(sid)
	a.queueMu.Unlock()

	waitFor(t, "queue empties", func() bool { return a.QueueLen(sid) == 0 })
	a.queueWg.Wait()

	ra.callMu.Lock()
	got := ra.calls
	ra.callMu.Unlock()

	want := []string{"first", "second", "third"}
	if len(got) != len(want) {
		t.Fatalf("Run calls = %v, want %v", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("call[%d] = %q, want %q (FIFO violation)", i, got[i], w)
		}
	}
}

// TestDrain_ErrSessionBusy_Retry_Count asserts ErrSessionBusy triggers a retry
// without surfacing an error to the user.
func TestDrain_ErrSessionBusy_Retry_Count(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ca := &countRunAgent{}
	ca.preset = []runResult{
		{err: agentpkg.ErrSessionBusy},
		{err: nil},
	}

	a, cap := newDrainApp(ctx, ca)

	sid := "busy-retry-count"
	a.queueMu.Lock()
	a.queues[sid] = []QueuedMessage{{Text: "retried"}}
	a.startDrainWorker(sid)
	a.queueMu.Unlock()

	waitFor(t, "queue empties after retry", func() bool { return a.QueueLen(sid) == 0 })
	a.queueWg.Wait()

	if got := ca.count.Load(); got < 2 {
		t.Errorf("expected ≥2 Run calls (busy+retry), got %d", got)
	}
	if errs := cap.errors(); len(errs) != 0 {
		t.Errorf("unexpected drain errors after ErrSessionBusy: %v", errs)
	}
}

// TestDrain_NonBusyError_HaltsWorker asserts a non-ErrSessionBusy error from
// Run surfaces an attributed error, halts the drain worker, preserves the
// remaining queue (including the failed message re-prepended at head), and
// allows a fresh EnqueueMessage to start a new worker.
func TestDrain_NonBusyError_HaltsWorker(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	providerErr := errors.New("provider API down")
	ca := &countRunAgent{}
	ca.preset = []runResult{{err: providerErr}}

	a, cap := newDrainApp(ctx, ca)

	sid := "halt-test"
	a.queueMu.Lock()
	a.queues[sid] = []QueuedMessage{{Text: "fails"}, {Text: "survivor"}}
	a.startDrainWorker(sid)
	a.queueMu.Unlock()

	// Worker halts after one error.
	waitFor(t, "worker halts after error", func() bool {
		a.queueMu.Lock()
		_, running := a.queueCancels[sid]
		a.queueMu.Unlock()
		return !running
	})
	a.queueWg.Wait()

	// Error must be attributed and wrap providerErr.
	errs := cap.errors()
	if len(errs) == 0 {
		t.Fatal("expected an attributed error notification, got none")
	}
	if !errors.Is(errs[0], providerErr) {
		t.Errorf("error should wrap providerErr, got: %v", errs[0])
	}
	const prefix = "queued message could not be delivered"
	if errStr := errs[0].Error(); len(errStr) < len(prefix) || errStr[:len(prefix)] != prefix {
		t.Errorf("error attribution missing: %q", errStr)
	}

	// Both messages must still be in the queue.
	if remaining := a.QueueLen(sid); remaining < 2 {
		t.Errorf("expected ≥2 messages remaining, got %d", remaining)
	}

	// Fresh enqueue starts a new worker.
	a.activeAgent = &fakeAgent{}
	a.EnqueueMessage(sid, QueuedMessage{Text: "new-trigger"})
	a.queueMu.Lock()
	_, running := a.queueCancels[sid]
	a.queueMu.Unlock()
	if !running {
		t.Error("expected a fresh drain worker after re-enqueue post-halt")
	}
	a.ShutdownQueues()
}

// TestDrain_WorkerTerminatesAfterEmptyQueue asserts the worker goroutine exits
// after draining without leaking.
func TestDrain_WorkerTerminatesAfterEmptyQueue(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ra := &recordAgent{}
	a, _ := newDrainApp(ctx, ra)

	sid := "terminates"
	a.queueMu.Lock()
	a.queues[sid] = []QueuedMessage{{Text: "only"}}
	a.startDrainWorker(sid)
	a.queueMu.Unlock()

	done := make(chan struct{})
	go func() {
		a.queueWg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not exit after draining (goroutine leak)")
	}

	a.queueMu.Lock()
	_, running := a.queueCancels[sid]
	a.queueMu.Unlock()
	if running {
		t.Error("queueCancels entry should be removed after worker exits")
	}
}

// TestDrain_ContextCancellation_ExitsWorker asserts the worker exits promptly
// when its context is cancelled, even with a non-empty queue.
func TestDrain_ContextCancellation_ExitsWorker(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	pa := &pauseAgent{release: make(chan struct{})}
	a, _ := newDrainApp(ctx, pa)

	sid := "cancel-test"
	a.queueMu.Lock()
	a.queues[sid] = []QueuedMessage{{Text: "blocks"}, {Text: "never-runs"}}
	a.startDrainWorker(sid)
	a.queueMu.Unlock()

	// Give the worker a moment to start the first Run call.
	time.Sleep(20 * time.Millisecond)

	cancel() // context cancellation signals shutdown

	done := make(chan struct{})
	go func() {
		a.queueWg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not exit on context cancellation")
	}
}

// TestDrain_FIFO_QueueNonEmptySlotMomentarilyIdle verifies FIFO is preserved
// when a new message arrives while the queue has entries but the slot is
// momentarily free (between drain deliveries). The enqueue path
// (QueueLen > 0 → EnqueueMessage, not sendMessage) prevents FIFO inversion
// at the editor level. Here we verify the drain itself completes in order.
func TestDrain_FIFO_QueueNonEmptySlotMomentarilyIdle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const sid = "fifo-idle-window"

	pa := &pauseAgent{release: make(chan struct{})}
	a, _ := newDrainApp(ctx, pa)

	// Enqueue M1 and start the drain worker.
	a.queueMu.Lock()
	a.queues[sid] = []QueuedMessage{{Text: "M1"}}
	a.startDrainWorker(sid)
	a.queueMu.Unlock()

	// Wait for the worker to start processing M1 (it's blocked in pause.Run).
	waitFor(t, "M1 dequeued", func() bool {
		pa.callMu.Lock()
		defer pa.callMu.Unlock()
		return len(pa.calls) == 1
	})

	// While M1's Run is in-flight, enqueue M2 directly.
	// At this point the queue is empty (M1 was dequeued) but a worker is
	// active. EnqueueMessage will see an active worker and not start another.
	a.EnqueueMessage(sid, QueuedMessage{Text: "M2"})

	// Release M1's Run.
	close(pa.release)

	waitFor(t, "both delivered", func() bool {
		pa.callMu.Lock()
		defer pa.callMu.Unlock()
		return len(pa.calls) == 2
	})
	a.queueWg.Wait()

	pa.callMu.Lock()
	got := pa.calls
	pa.callMu.Unlock()

	if len(got) != 2 || got[0] != "M1" || got[1] != "M2" {
		t.Errorf("FIFO violation in idle window: got %v, want [M1 M2]", got)
	}
}

// TestDrain_ErrorAttribution verifies the attributed error message wraps the
// original error and contains an attribution prefix.
func TestDrain_ErrorAttribution(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	apiErr := fmt.Errorf("context length exceeded")
	ca := &countRunAgent{}
	ca.preset = []runResult{{err: apiErr}}

	a, cap := newDrainApp(ctx, ca)

	sid := "attribution"
	a.queueMu.Lock()
	a.queues[sid] = []QueuedMessage{{Text: "m"}}
	a.startDrainWorker(sid)
	a.queueMu.Unlock()

	waitFor(t, "error event received", func() bool {
		return len(cap.errors()) > 0
	})
	a.queueWg.Wait()

	err := cap.errors()[0]
	if !errors.Is(err, apiErr) {
		t.Errorf("error should wrap apiErr, got: %v", err)
	}
	const prefix = "queued message could not be delivered"
	if errStr := err.Error(); len(errStr) < len(prefix) || errStr[:len(prefix)] != prefix {
		t.Errorf("error attribution missing: %q", errStr)
	}
}
