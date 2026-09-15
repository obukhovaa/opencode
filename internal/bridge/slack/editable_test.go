package slack

import (
	"context"
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/bridge"
)

// TestSendEditableThenEditMessage_Slack: SendEditable posts once and
// EditMessage edits that same message (chat.update on the token's
// channel and ts) — the mechanism behind the per-run progress card.
func TestSendEditableThenEditMessage_Slack(t *testing.T) {
	t.Parallel()
	a, mock, _ := newAdapter(t, Identity{ID: "default", BotToken: "xoxb-test", AppToken: "xapp-test"})

	peer := bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "C0123|1700000000.000100"}
	tok, err := a.SendEditable(context.Background(), peer, "⏳ Thinking...")
	if err != nil {
		t.Fatalf("SendEditable: %v", err)
	}
	parts := strings.SplitN(tok, "\x00", 2)
	if len(parts) != 2 || parts[0] != "C0123" || parts[1] == "" {
		t.Fatalf("token = %q; want C0123\\x00<ts>", tok)
	}

	for _, text := range []string{"⏳ 5 tool calls done · 1m12s", "✓ Done · 8 tool calls · 3m40s"} {
		if err := a.EditMessage(context.Background(), peer, tok, text); err != nil {
			t.Fatalf("EditMessage(%q): %v", text, err)
		}
	}

	mock.mu.Lock()
	posts, updates := mock.posts, mock.updates
	mock.mu.Unlock()
	if len(posts) != 1 {
		t.Fatalf("chat.postMessage calls = %d; want exactly 1", len(posts))
	}
	if posts[0].Text != "⏳ Thinking..." || posts[0].ThreadTS != "1700000000.000100" {
		t.Errorf("post = %+v; want Thinking... in the peer's thread", posts[0])
	}
	if len(updates) != 2 {
		t.Fatalf("chat.update calls = %d; want 2", len(updates))
	}
	if updates[0].Channel != "C0123" || updates[0].TS != parts[1] {
		t.Errorf("update target = %s/%s; want %s/%s", updates[0].Channel, updates[0].TS, "C0123", parts[1])
	}
	if updates[1].Text != "✓ Done · 8 tool calls · 3m40s" {
		t.Errorf("last update text = %q", updates[1].Text)
	}
}

func TestEditMessage_Slack_RejectsBadToken(t *testing.T) {
	t.Parallel()
	a, _, _ := newAdapter(t, Identity{ID: "default", BotToken: "xoxb-test", AppToken: "xapp-test"})
	peer := bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "D0123"}
	if err := a.EditMessage(context.Background(), peer, "no-separator", "x"); err == nil {
		t.Error("EditMessage with a malformed token should fail")
	}
}
