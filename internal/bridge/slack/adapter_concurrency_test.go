package slack

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/opencode-ai/opencode/internal/bridge"
)

// TestConcurrentSendIsRaceFreeAcrossSessions drives Send from many
// goroutines on ONE Adapter instance. Per AGENTS.md the bridge keys its
// adapter map on channel+identity, so every session bound to the same
// identity shares one *Adapter, and each bound session owns its own
// dispatch goroutine — concurrent Send on a single Adapter is the normal
// production shape, not an exotic one.
//
// The markdown-block work added per-Adapter mutable state (the
// markdownBlocksUnsupported latch). It is an atomic.Bool and therefore
// correct, but the suite had no concurrent-Send test at all, so a plain
// `go test -race` run proved nothing about it: -race only reports races it
// actually observes. This test gives the detector something to observe.
func TestConcurrentSendIsRaceFreeAcrossSessions(t *testing.T) {
	t.Parallel()
	a, mock, _ := newAdapter(t, Identity{ID: "default", BotToken: "xoxb-test", AppToken: "xapp-test"})

	const goroutines = 16
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(n int) {
			defer wg.Done()
			r := a.Send(context.Background(), bridge.Outbound{
				Peer: bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "D123"},
				Text: "## Heading\n\nmessage with `code` and **bold**",
			})
			if !r.Delivered {
				t.Errorf("goroutine %d: send failed: %v", n, r.Err)
			}
		}(i)
	}
	wg.Wait()

	if got := len(mock.Posts()); got != goroutines {
		t.Errorf("posts = %d, want %d", got, goroutines)
	}
	if a.markdownBlocksUnsupported.Load() {
		t.Error("latch set despite every send succeeding")
	}
}

// TestConcurrentSendLatchesExactlyOnce races many sends against a
// workspace whose first postMessage rejects markdown blocks, asserting the
// sticky latch converges under contention (rather than flapping) and that
// no message is dropped along the way.
func TestConcurrentSendLatchesExactlyOnce(t *testing.T) {
	t.Parallel()
	a, mock, _ := newAdapter(t, Identity{ID: "default", BotToken: "xoxb-test", AppToken: "xapp-test"})

	// Only the FIRST postMessage fails. The mock injects by absolute call
	// index and cannot tell a blocks attempt from its plain-text retry, so
	// a longer error prefix would also fail the retries and manufacture a
	// drop that the adapter is not responsible for. Index 0 is always a
	// blocks attempt (the latch starts clear), which is enough to make one
	// goroutine take the latch path while the others race it.
	mock.mu.Lock()
	mock.postMessageErrors = []string{"invalid_blocks"}
	mock.mu.Unlock()

	const goroutines = 8
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(n int) {
			defer wg.Done()
			r := a.Send(context.Background(), bridge.Outbound{
				Peer: bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "D123"},
				Text: "prose **bold**",
			})
			if !r.Delivered {
				t.Errorf("goroutine %d: message dropped instead of degrading to plain text: %v", n, r.Err)
			}
		}(i)
	}
	wg.Wait()

	if !a.markdownBlocksUnsupported.Load() {
		t.Error("latch not set after an unambiguous invalid_blocks rejection")
	}

	// At least one plain-text post (no blocks) must exist: the degradation
	// path for the rejected send. Sends that won the race before the latch
	// was set legitimately used blocks, so this is a floor, not a count.
	var plain int
	for _, p := range mock.Posts() {
		if p.Blocks == "" && strings.Contains(p.Text, "prose") {
			plain++
		}
	}
	if plain == 0 {
		t.Error("no plain-text post recorded — the degradation path never ran")
	}
}
