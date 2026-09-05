package telegram

import (
	"context"
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/bridge"
)

// TestSendQueuedAck verifies that SendQueuedAck calls sendMessage and returns
// the message ID (string-encoded) as the token.
func TestSendQueuedAck(t *testing.T) {
	t.Parallel()
	a, mock, _ := newAdapter(t, Identity{ID: "tg", Token: "tok"})

	peer := bridge.PeerRef{Channel: "telegram", Identity: "tg", PeerID: "12345"}
	tok, err := a.SendQueuedAck(context.Background(), peer, 1)
	if err != nil {
		t.Fatalf("SendQueuedAck: %v", err)
	}
	if tok == "" {
		t.Fatal("SendQueuedAck returned empty token")
	}

	// The mock returns message ID 1 for sendMessage.
	if tok != "1" {
		t.Errorf("token = %q, want \"1\"", tok)
	}

	mock.mu.Lock()
	sends := mock.sendMsg
	mock.mu.Unlock()
	if len(sends) == 0 {
		t.Fatal("sendMessage was not called")
	}
	if !strings.Contains(sends[len(sends)-1].Text, "⏳") {
		t.Errorf("ack text missing ⏳ glyph: %q", sends[len(sends)-1].Text)
	}
}

// TestUpdateQueuedAck_InPlace verifies that UpdateQueuedAck calls editMessageText
// with the correct message ID and position text.
func TestUpdateQueuedAck_InPlace(t *testing.T) {
	t.Parallel()
	a, mock, _ := newAdapter(t, Identity{ID: "tg", Token: "tok"})

	peer := bridge.PeerRef{Channel: "telegram", Identity: "tg", PeerID: "12345"}
	// Send first, then update.
	tok, err := a.SendQueuedAck(context.Background(), peer, 1)
	if err != nil {
		t.Fatalf("SendQueuedAck: %v", err)
	}

	if err := a.UpdateQueuedAck(context.Background(), peer, tok, 1); err != nil {
		t.Fatalf("UpdateQueuedAck position=1: %v", err)
	}
	if err := a.UpdateQueuedAck(context.Background(), peer, tok, 0); err != nil {
		t.Fatalf("UpdateQueuedAck position=0 (resolve): %v", err)
	}

	mock.mu.Lock()
	edits := mock.editMessageText
	mock.mu.Unlock()

	if len(edits) < 2 {
		t.Fatalf("editMessageText called %d times, want ≥2", len(edits))
	}
	// Last edit must be the resolve text.
	if !strings.Contains(edits[len(edits)-1].Text, "▶") {
		t.Errorf("resolve edit text missing ▶ glyph: %q", edits[len(edits)-1].Text)
	}
	// Message ID must round-trip through the token.
	if edits[len(edits)-1].MessageID != tok {
		t.Errorf("message_id in edit = %q, want token %q", edits[len(edits)-1].MessageID, tok)
	}
}

// TestSendQueuedAck_InvalidPeer rejects a non-numeric peer ID.
func TestSendQueuedAck_InvalidPeer(t *testing.T) {
	t.Parallel()
	a, _, _ := newAdapter(t, Identity{ID: "tg", Token: "tok"})
	_, err := a.SendQueuedAck(context.Background(),
		bridge.PeerRef{Channel: "telegram", Identity: "tg", PeerID: "@notanumber"},
		1)
	if err == nil {
		t.Error("expected error for invalid peer ID")
	}
}

// TestUpdateQueuedAck_InvalidToken rejects a non-numeric token.
func TestUpdateQueuedAck_InvalidToken(t *testing.T) {
	t.Parallel()
	a, _, _ := newAdapter(t, Identity{ID: "tg", Token: "tok"})
	err := a.UpdateQueuedAck(context.Background(),
		bridge.PeerRef{Channel: "telegram", Identity: "tg", PeerID: "12345"},
		"notanumber", 1)
	if err == nil {
		t.Error("expected error for invalid token")
	}
}

// TestQueuedAckPosition verifies position==2 text differs from position==1.
func TestQueuedAckPosition(t *testing.T) {
	t.Parallel()
	text1 := bridge.QueueAckText(1)
	text2 := bridge.QueueAckText(2)
	if text1 == text2 {
		t.Errorf("position 1 and 2 produced identical text: %q", text1)
	}
	if !strings.Contains(text1, "⏳") || !strings.Contains(text2, "⏳") {
		t.Errorf("ack text missing ⏳ glyph: %q / %q", text1, text2)
	}
	if !strings.Contains(bridge.ResolvedAckText, "▶") {
		t.Errorf("resolved text missing ▶ glyph: %q", bridge.ResolvedAckText)
	}
}
