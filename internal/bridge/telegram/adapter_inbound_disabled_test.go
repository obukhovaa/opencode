package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/opencode-ai/opencode/internal/bridge"
)

// TestStartSkipsLongPollWhenInboundDisabled verifies that an adapter
// configured with Identity.Inbound == bridge.InboundDisabled does NOT
// poll getUpdates on Start, and does NOT call getMe (the lib's
// per-startup probe). Outbound sendMessage still works.
func TestStartSkipsLongPollWhenInboundDisabled(t *testing.T) {
	t.Parallel()

	var getMeCount, getUpdatesCount, sendMessageCount atomic.Int64

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		idx := strings.LastIndex(path, "/")
		method := path[idx+1:]
		switch method {
		case "getMe":
			getMeCount.Add(1)
			respondJSON(w, models.User{ID: 999, Username: "routerbot", IsBot: true})
		case "getUpdates":
			getUpdatesCount.Add(1)
			respondJSON(w, []models.Update{})
		case "sendMessage":
			sendMessageCount.Add(1)
			respondJSON(w, models.Message{ID: 1})
		default:
			respondJSON(w, map[string]any{"ok": true})
		}
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	a, err := New(Identity{
		ID:      "default",
		Token:   "tg-token",
		Inbound: bridge.InboundDisabled,
	}, Options{
		ServerURL:   server.URL,
		MediaDir:    t.TempDir(),
		HTTPClient:  &http.Client{Timeout: 5 * time.Second},
		PollTimeout: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	inbound := make(chan bridge.Inbound, 1)
	if err := a.Start(context.Background(), inbound); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = a.Stop() })

	// Long-poll would issue getUpdates within ~one PollTimeout window;
	// 250ms is comfortably past the first poll cycle.
	time.Sleep(250 * time.Millisecond)

	if got := getMeCount.Load(); got != 0 {
		t.Errorf("getMe called %d times in disabled mode (want 0 — startup probe must be skipped)", got)
	}
	if got := getUpdatesCount.Load(); got != 0 {
		t.Errorf("getUpdates called %d times in disabled mode (want 0 — long-poll loop must be skipped)", got)
	}

	// Outbound still works.
	res := a.Send(context.Background(), bridge.Outbound{
		Peer: bridge.PeerRef{Channel: "telegram", Identity: "default", PeerID: "12345"},
		Text: "outbound works",
	})
	if !res.Delivered {
		t.Fatalf("Send: not delivered, err=%v", res.Err)
	}
	if got := sendMessageCount.Load(); got != 1 {
		t.Errorf("sendMessage called %d times, want 1", got)
	}

	if got := a.Status().Status; got != "running" {
		t.Errorf("Status = %q, want running", got)
	}
}

func respondJSON(w http.ResponseWriter, payload any) {
	body, _ := json.Marshal(map[string]any{"ok": true, "result": payload})
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}
