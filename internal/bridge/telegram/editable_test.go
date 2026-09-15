package telegram

import (
	"context"
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/bridge"
)

// TestSendEditableThenEditMessage: SendEditable sends once and returns
// the message id; EditMessage edits that message via editMessageText —
// the mechanism behind the per-run progress card.
func TestSendEditableThenEditMessage(t *testing.T) {
	t.Parallel()
	a, mock, _ := newAdapter(t, Identity{ID: "tg", Token: "tok"})

	peer := bridge.PeerRef{Channel: "telegram", Identity: "tg", PeerID: "12345"}
	tok, err := a.SendEditable(context.Background(), peer, "⏳ Thinking...")
	if err != nil {
		t.Fatalf("SendEditable: %v", err)
	}
	if tok != "1" {
		t.Errorf("token = %q; want the mock's message id \"1\"", tok)
	}
	if err := a.EditMessage(context.Background(), peer, tok, "⏳ 5 tool calls done · 1m12s"); err != nil {
		t.Fatalf("EditMessage: %v", err)
	}

	mock.mu.Lock()
	sends, edits := mock.sendMsg, mock.editMessageText
	mock.mu.Unlock()
	if len(sends) != 1 || !strings.Contains(sends[0].Text, "Thinking") {
		t.Errorf("sendMessage calls = %+v; want exactly one carrying Thinking...", sends)
	}
	if len(edits) != 1 || edits[0].MessageID != "1" || !strings.Contains(edits[0].Text, "5 tool calls done") {
		t.Errorf("editMessageText calls = %+v; want one edit of message 1 with the count", edits)
	}
}

func TestEditMessage_RejectsBadToken(t *testing.T) {
	t.Parallel()
	a, _, _ := newAdapter(t, Identity{ID: "tg", Token: "tok"})
	peer := bridge.PeerRef{Channel: "telegram", Identity: "tg", PeerID: "12345"}
	if err := a.EditMessage(context.Background(), peer, "not-a-number", "x"); err == nil {
		t.Error("EditMessage with a non-numeric token should fail")
	}
}
