package slack

import (
	"context"
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/bridge"
)

// TestSendQueuedAck_Slack verifies SendQueuedAck calls PostMessageContext and
// returns a token encoding the channel ID and ts.
func TestSendQueuedAck_Slack(t *testing.T) {
	t.Parallel()
	a, mock, _ := newAdapter(t, Identity{ID: "default", BotToken: "xoxb-test", AppToken: "xapp-test"})

	peer := bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "D0123TEST"}
	tok, err := a.SendQueuedAck(context.Background(), peer, 1)
	if err != nil {
		t.Fatalf("SendQueuedAck: %v", err)
	}
	if tok == "" {
		t.Fatal("SendQueuedAck returned empty token")
	}

	// Token must encode channelID and ts separated by null byte.
	parts := strings.SplitN(tok, "\x00", 2)
	if len(parts) != 2 {
		t.Fatalf("token format wrong, got %q (want channelID\\x00ts)", tok)
	}
	if parts[0] != "D0123TEST" {
		t.Errorf("token channel = %q, want %q", parts[0], "D0123TEST")
	}
	if parts[1] == "" {
		t.Error("token ts is empty")
	}

	mock.mu.Lock()
	posts := mock.posts
	mock.mu.Unlock()
	if len(posts) == 0 {
		t.Fatal("PostMessageContext was not called")
	}
	if !strings.Contains(posts[len(posts)-1].Text, "⏳") {
		t.Errorf("ack text missing ⏳ glyph: %q", posts[len(posts)-1].Text)
	}
}

// TestUpdateQueuedAck_Slack verifies UpdateQueuedAck calls UpdateMessageContext
// with the channel and ts from the token.
func TestUpdateQueuedAck_Slack(t *testing.T) {
	t.Parallel()
	a, mock, _ := newAdapter(t, Identity{ID: "default", BotToken: "xoxb-test", AppToken: "xapp-test"})

	peer := bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "D0123TEST"}
	tok, err := a.SendQueuedAck(context.Background(), peer, 1)
	if err != nil {
		t.Fatalf("SendQueuedAck: %v", err)
	}

	// In-place update with position.
	if err := a.UpdateQueuedAck(context.Background(), peer, tok, 1); err != nil {
		t.Fatalf("UpdateQueuedAck position=1: %v", err)
	}
	// Resolve.
	if err := a.UpdateQueuedAck(context.Background(), peer, tok, 0); err != nil {
		t.Fatalf("UpdateQueuedAck position=0 (resolve): %v", err)
	}

	mock.mu.Lock()
	updates := mock.updates
	mock.mu.Unlock()

	if len(updates) < 2 {
		t.Fatalf("chat.update called %d times, want ≥2", len(updates))
	}
	last := updates[len(updates)-1]
	if !strings.Contains(last.Text, "▶") {
		t.Errorf("resolve update text missing ▶ glyph: %q", last.Text)
	}
}

// TestUpdateQueuedAck_Slack_InvalidToken rejects a malformed token.
func TestUpdateQueuedAck_Slack_InvalidToken(t *testing.T) {
	t.Parallel()
	a, _, _ := newAdapter(t, Identity{ID: "default", BotToken: "xoxb-test", AppToken: "xapp-test"})
	err := a.UpdateQueuedAck(context.Background(),
		bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "D0123TEST"},
		"no-null-byte-token", 1)
	if err == nil {
		t.Error("expected error for malformed token")
	}
}
