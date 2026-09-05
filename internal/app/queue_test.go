package app

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/message"
)

// newTestApp returns a minimal App for queue tests. It has an explicit context
// so tests can cancel it to stop drain workers.
func newTestApp(ctx context.Context) *App {
	return &App{
		ctx:          ctx,
		queues:       make(map[string][]QueuedMessage),
		queueCancels: make(map[string]context.CancelFunc),
	}
}

// TestQueue_EnqueueDequeue_FIFO asserts that DequeueMessage returns messages in
// enqueue order.
func TestQueue_EnqueueDequeue_FIFO(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := newTestApp(ctx)

	// Manually enqueue without starting the drain worker (we hold queueMu).
	a.queueMu.Lock()
	a.queues["s1"] = append(a.queues["s1"], QueuedMessage{Text: "first"})
	a.queues["s1"] = append(a.queues["s1"], QueuedMessage{Text: "second"})
	a.queues["s1"] = append(a.queues["s1"], QueuedMessage{Text: "third"})
	a.queueMu.Unlock()

	m1, ok1 := a.DequeueMessage("s1")
	m2, ok2 := a.DequeueMessage("s1")
	m3, ok3 := a.DequeueMessage("s1")
	_, ok4 := a.DequeueMessage("s1") // empty

	if !ok1 || m1.Text != "first" {
		t.Errorf("want first got %q (ok=%v)", m1.Text, ok1)
	}
	if !ok2 || m2.Text != "second" {
		t.Errorf("want second got %q (ok=%v)", m2.Text, ok2)
	}
	if !ok3 || m3.Text != "third" {
		t.Errorf("want third got %q (ok=%v)", m3.Text, ok3)
	}
	if ok4 {
		t.Error("expected empty after three dequeues")
	}
}

// TestQueue_QueueLen tracks correctly.
func TestQueue_QueueLen(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := newTestApp(ctx)

	if got := a.QueueLen("s1"); got != 0 {
		t.Fatalf("initial QueueLen = %d, want 0", got)
	}

	a.queueMu.Lock()
	a.queues["s1"] = append(a.queues["s1"], QueuedMessage{Text: "a"})
	a.queues["s1"] = append(a.queues["s1"], QueuedMessage{Text: "b"})
	a.queueMu.Unlock()

	if got := a.QueueLen("s1"); got != 2 {
		t.Fatalf("QueueLen after 2 enqueues = %d, want 2", got)
	}

	a.DequeueMessage("s1")
	if got := a.QueueLen("s1"); got != 1 {
		t.Fatalf("QueueLen after 1 dequeue = %d, want 1", got)
	}
}

// TestQueue_DiscardQueue empties the queue for the target session only.
func TestQueue_DiscardQueue(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := newTestApp(ctx)

	a.queueMu.Lock()
	a.queues["s1"] = []QueuedMessage{{Text: "x"}, {Text: "y"}}
	a.queues["s2"] = []QueuedMessage{{Text: "z"}}
	a.queueMu.Unlock()

	a.DiscardQueue("s1")

	if got := a.QueueLen("s1"); got != 0 {
		t.Errorf("s1 QueueLen after discard = %d, want 0", got)
	}
	if got := a.QueueLen("s2"); got != 1 {
		t.Errorf("s2 QueueLen should be untouched, got %d", got)
	}
}

// TestQueue_Concurrent_NoRace enqueues and dequeues from many goroutines to
// detect data races under go test -race.
func TestQueue_Concurrent_NoRace(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := newTestApp(ctx)

	const goroutines = 16
	const perGoroutine = 20
	sid := "race-session"

	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perGoroutine; j++ {
				a.queueMu.Lock()
				a.queues[sid] = append(a.queues[sid], QueuedMessage{Text: "data"})
				a.queueMu.Unlock()
				a.DequeueMessage(sid)
				_ = a.QueueLen(sid)
			}
		}()
	}
	wg.Wait()
}

// TestQueue_Attachments verifies that Attachments are preserved through
// enqueue/dequeue.
func TestQueue_Attachments(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := newTestApp(ctx)

	att := message.Attachment{FileName: "test.png"}
	a.queueMu.Lock()
	a.queues["s1"] = append(a.queues["s1"], QueuedMessage{
		Text:        "with attachment",
		Attachments: []message.Attachment{att},
	})
	a.queueMu.Unlock()

	msg, ok := a.DequeueMessage("s1")
	if !ok {
		t.Fatal("expected a message")
	}
	if msg.Text != "with attachment" {
		t.Errorf("unexpected text %q", msg.Text)
	}
	if len(msg.Attachments) != 1 || msg.Attachments[0].FileName != "test.png" {
		t.Errorf("attachments not preserved: %+v", msg.Attachments)
	}
}

// TestQueue_ShutdownQueues_cancelsWorkers verifies that ShutdownQueues cancels
// drain workers without blocking indefinitely (no goroutine leak). We start a
// real drain worker and cancel it via ShutdownQueues.
func TestQueue_ShutdownQueues_cancelsWorkers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := newTestApp(ctx)

	sid := "shutdown-test"

	// Start a drain worker manually without actual agent (it will spin on the
	// busy-check path since ActiveAgent() returns nil). We just want to confirm
	// the worker terminates when ShutdownQueues is called.
	a.queueMu.Lock()
	a.queues[sid] = []QueuedMessage{{Text: "pending"}}
	a.startDrainWorker(sid)
	a.queueMu.Unlock()

	// Give the worker a moment to start and enter its wait loop.
	time.Sleep(10 * time.Millisecond)

	done := make(chan struct{})
	go func() {
		a.ShutdownQueues()
		close(done)
	}()

	select {
	case <-done:
		// OK — workers exited promptly.
	case <-time.After(2 * time.Second):
		t.Fatal("ShutdownQueues did not return within 2s (goroutine leak?)")
	}
}

// TestQueue_DequeueOrRelease_AtomicDeregistration locks in the invariant that
// makes the drain worker's exit safe: the worker is deregistered from
// queueCancels in the SAME critical section that observes the empty queue.
//
// Splitting the two (dequeue returns false, then a second lock deletes the
// cancel) lets an EnqueueMessage land in between: it sees the still-registered
// worker, declines to start one, and its message is stranded in the queue with
// nothing to drain it until the user submits again.
func TestQueue_DequeueOrRelease_AtomicDeregistration(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := newTestApp(ctx)

	a.queueMu.Lock()
	a.queues["s1"] = []QueuedMessage{{Text: "a"}}
	a.queueCancels["s1"] = func() {}
	a.queueMu.Unlock()

	msg, ok := a.dequeueOrRelease("s1")
	if !ok || msg.Text != "a" {
		t.Fatalf("dequeueOrRelease = (%q, %v), want (\"a\", true)", msg.Text, ok)
	}
	a.queueMu.Lock()
	_, registered := a.queueCancels["s1"]
	a.queueMu.Unlock()
	if !registered {
		t.Error("worker deregistered while it still holds a message")
	}

	if _, ok := a.dequeueOrRelease("s1"); ok {
		t.Fatal("dequeueOrRelease returned a message from an empty queue")
	}
	a.queueMu.Lock()
	_, registered = a.queueCancels["s1"]
	a.queueMu.Unlock()
	if registered {
		t.Error("worker not deregistered after observing an empty queue")
	}
}

// TestQueue_EnqueueDuringWorkerExit_AlwaysDelivered stresses the enqueue-while-
// worker-exiting window: every enqueued message must eventually be delivered,
// and the queue must never come to rest non-empty with no worker running.
func TestQueue_EnqueueDuringWorkerExit_AlwaysDelivered(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ag := &countRunAgent{}
	a := newTestApp(ctx)
	a.activeAgent = ag

	const total = 300
	for i := 0; i < total; i++ {
		a.EnqueueMessage("s1", QueuedMessage{Text: "m"})
		time.Sleep(50 * time.Microsecond)
	}

	waitFor(t, "all queued messages delivered", func() bool {
		return int(ag.count.Load()) == total && a.QueueLen("s1") == 0
	})
}
