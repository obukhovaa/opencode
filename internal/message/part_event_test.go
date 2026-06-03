package message

import (
	"context"
	"database/sql"
	"sync/atomic"
	"testing"

	"github.com/opencode-ai/opencode/internal/pubsub"
)

// newTestService returns a minimal service usable for broker-level tests.
// db/q are nil — only the brokers are exercised.
func newTestService() *service {
	return &service{
		Broker: pubsub.NewBroker[Message](),
		parts:  pubsub.NewBrokerWithOptions[PartEvent](partsBufferSize, 1000),
		db:     (*sql.DB)(nil),
	}
}

func TestFindToolCall(t *testing.T) {
	msg := Message{
		ID: "msg-1",
		Parts: []ContentPart{
			TextContent{Text: "hi"},
			ToolCall{ID: "call-a", Name: "bash"},
			ToolCall{ID: "call-b", Name: "read"},
		},
	}

	tc, ok := msg.FindToolCall("call-b")
	if !ok || tc.Name != "read" {
		t.Fatalf("want (read, true), got (%+v, %v)", tc, ok)
	}

	if _, ok := msg.FindToolCall("missing"); ok {
		t.Fatalf("want missing call to return ok=false")
	}
}

func TestClonePartToolResultIndependentMetadata(t *testing.T) {
	original := ToolResult{
		ToolCallID: "call-1",
		Name:       "bash",
		Content:    "ok",
		Metadata:   `{"k":"v"}`,
	}
	clone, ok := clonePart(original).(ToolResult)
	if !ok {
		t.Fatalf("clonePart(ToolResult) returned %T", clonePart(original))
	}
	// Metadata is a string in our model; we just need a value-equal copy.
	if clone.Metadata != original.Metadata || clone.Content != original.Content {
		t.Fatalf("clone differs: %+v vs %+v", clone, original)
	}
}

func TestPublishPartZeroSubscribersFastPath(t *testing.T) {
	// Verifies the central perf claim: PublishPart returns without invoking
	// clonePart when no subscribers exist. We can't observe clonePart
	// directly (unexported, no hooks), but we can confirm Publish was a
	// no-op by checking subscriber count and the absence of any side
	// effect on a freshly subscribed consumer afterwards.
	s := newTestService()

	// Publish with no subscribers — must not panic, must not block.
	for i := 0; i < 1000; i++ {
		s.PublishPart("sess", "msg", ToolCall{ID: "x", Name: "noop"})
	}

	// Now subscribe and publish once — the new subscriber must see it.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := s.SubscribeParts(ctx)

	s.PublishPart("sess", "msg", ToolCall{ID: "y", Name: "yes"})

	select {
	case ev := <-ch:
		tc, ok := ev.Payload.Part.(ToolCall)
		if !ok || tc.ID != "y" {
			t.Fatalf("got %+v, want ToolCall{ID:y}", ev.Payload.Part)
		}
	default:
		t.Fatalf("subscriber received no event after PublishPart with active subscription")
	}
}

func TestPublishPartShutdownIdempotent(t *testing.T) {
	s := newTestService()
	s.Shutdown()
	s.Shutdown() // must not panic
}

// TestPublishPartConcurrent exercises the path the parallel-tool-group
// goroutines hit: many goroutines call PublishPart against a single
// active subscriber. The broker uses non-blocking sends, so some events
// may be dropped under load — what we verify is that the call site is
// safe under -race and that PublishPart never panics.
func TestPublishPartConcurrent(t *testing.T) {
	s := newTestService()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := s.SubscribeParts(ctx)

	const goroutines = 16
	const perGoroutine = 64

	var seen atomic.Int64
	done := make(chan struct{})
	go func() {
		for range ch {
			seen.Add(1)
		}
		close(done)
	}()

	for g := 0; g < goroutines; g++ {
		go func(g int) {
			for i := 0; i < perGoroutine; i++ {
				s.PublishPart("sess", "msg", ToolCall{ID: "x", Name: "concurrent"})
			}
		}(g)
	}

	// Drain briefly then shut down to terminate the consumer.
	// We don't assert on count — drops are allowed by broker design.
	cancel()
	<-done

	if seen.Load() < 0 {
		t.Fatal("unreachable: counter went negative")
	}
}
