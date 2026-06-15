package slack

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/bridge"
)

// TestStartSkipsListenerWhenInboundDisabled verifies that an adapter
// configured with Identity.Inbound == bridge.InboundDisabled does NOT
// hit Slack's auth.test (the first call the Socket Mode startup path
// makes) and does NOT attempt to open the Socket Mode connection. The
// outbound REST surface stays functional — Send still POSTs through
// the mock chat.postMessage endpoint. This is the core Phase A.5
// guarantee.
//
// We also verify the app token requirement is relaxed in disabled
// mode (the orchestrator owns the app token; runners shouldn't need
// it). New() with an empty AppToken + Inbound=="disabled" must succeed.
func TestStartSkipsListenerWhenInboundDisabled(t *testing.T) {
	t.Parallel()
	mock := newMockServer(t)
	a, err := New(Identity{
		ID:       "default",
		BotToken: "xoxb-test",
		// AppToken intentionally empty — inbound-disabled mode does
		// not require the app token.
		AppToken: "",
		Inbound:  bridge.InboundDisabled,
	}, Options{
		APIURL:     mock.URL() + "/",
		HTTPClient: &http.Client{Timeout: 5 * time.Second},
		MediaDir:   t.TempDir(),
	})
	if err != nil {
		t.Fatalf("New: %v (Inbound=disabled should not require AppToken)", err)
	}

	inbound := make(chan bridge.Inbound, 1)
	if err := a.Start(context.Background(), inbound); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = a.Stop() })

	// Give the listener goroutine a chance to fire IF it were going to.
	// 50ms is enough for any production code path to call auth.test or
	// connect to Socket Mode if Start mistakenly took the enabled branch.
	time.Sleep(50 * time.Millisecond)

	mock.mu.Lock()
	authCalls := mock.authTest
	mock.mu.Unlock()
	if authCalls != 0 {
		t.Errorf("auth.test was called %d times in disabled mode (want 0 — Socket Mode startup path must be skipped)", authCalls)
	}

	// Inbound channel must still be empty — no listener pushed onto it.
	select {
	case got := <-inbound:
		t.Errorf("inbound delivered unexpected message: %+v", got)
	default:
	}

	// Status reports "running" — outbound is alive even though the
	// listener is skipped.
	if got := a.Status().Status; got != "running" {
		t.Errorf("Status = %q, want running", got)
	}
}

// TestSendStillWorksWhenInboundDisabled verifies that the outbound REST
// path is unaffected by inbound-disabled mode. A bare Send to a DM peer
// must still POST chat.postMessage on the mock server.
func TestSendStillWorksWhenInboundDisabled(t *testing.T) {
	t.Parallel()
	mock := newMockServer(t)
	a, err := New(Identity{
		ID:       "default",
		BotToken: "xoxb-test",
		Inbound:  bridge.InboundDisabled,
	}, Options{
		APIURL:     mock.URL() + "/",
		HTTPClient: &http.Client{Timeout: 5 * time.Second},
		MediaDir:   t.TempDir(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := a.Start(context.Background(), make(chan bridge.Inbound, 1)); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = a.Stop() })

	res := a.Send(context.Background(), bridge.Outbound{
		Peer: bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "D012345"},
		Text: "outbound works",
	})
	if !res.Delivered {
		t.Fatalf("Send: not delivered, err=%v", res.Err)
	}

	posts := mock.Posts()
	if len(posts) != 1 {
		t.Fatalf("posts = %d, want 1", len(posts))
	}
	if posts[0].Text != "outbound works" {
		t.Errorf("post.Text = %q, want %q", posts[0].Text, "outbound works")
	}
}
