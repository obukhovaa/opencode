package mattermost

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/bridge"
)

// TestStartSkipsWebSocketWhenInboundDisabled verifies that an adapter
// configured with Identity.Inbound == bridge.InboundDisabled does NOT
// call /users/me on startup and does NOT attempt the WebSocket upgrade.
// Outbound CreatePost still works.
func TestStartSkipsWebSocketWhenInboundDisabled(t *testing.T) {
	t.Parallel()

	var getMeCount, wsUpgradeCount, createPostCount atomic.Int64

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/users/me", func(w http.ResponseWriter, r *http.Request) {
		getMeCount.Add(1)
		_ = json.NewEncoder(w).Encode(User{ID: "bot123", Username: "testbot"})
	})
	mux.HandleFunc("/api/v4/websocket", func(w http.ResponseWriter, r *http.Request) {
		wsUpgradeCount.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	})
	mux.HandleFunc("/api/v4/posts", func(w http.ResponseWriter, r *http.Request) {
		createPostCount.Add(1)
		// Echo a minimal new-post response.
		var in CreatePostInput
		_ = json.NewDecoder(r.Body).Decode(&in)
		_ = json.NewEncoder(w).Encode(Post{
			ID: "new_post", ChannelID: in.ChannelID, Message: in.Message,
		})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Fall-through (channels/direct, users/<id>/typing). Not called in
		// this test; if hit, log so the failure is observable.
		t.Logf("unexpected request: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusOK)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	a, err := New(Identity{
		ID:          "default",
		ServerURL:   server.URL,
		AccessToken: "tok-test",
		Inbound:     bridge.InboundDisabled,
	}, Options{
		MediaDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	inbound := make(chan bridge.Inbound, 1)
	if err := a.Start(context.Background(), inbound); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = a.Stop() })

	// 100ms is plenty for the WebSocket dial to fire if Start mistakenly
	// took the enabled branch.
	time.Sleep(100 * time.Millisecond)

	if got := getMeCount.Load(); got != 0 {
		t.Errorf("/users/me hit %d times in disabled mode (want 0)", got)
	}
	if got := wsUpgradeCount.Load(); got != 0 {
		t.Errorf("/api/v4/websocket hit %d times in disabled mode (want 0)", got)
	}

	// Outbound still works.
	res := a.Send(context.Background(), bridge.Outbound{
		Peer: bridge.PeerRef{Channel: "mattermost", Identity: "default", PeerID: "C0123FAKEXX"},
		Text: "outbound works",
	})
	if !res.Delivered {
		t.Fatalf("Send: not delivered, err=%v", res.Err)
	}
	if got := createPostCount.Load(); got != 1 {
		t.Errorf("/api/v4/posts called %d times, want 1", got)
	}

	if got := a.Status().Status; got != "running" {
		t.Errorf("Status = %q, want running", got)
	}
}
