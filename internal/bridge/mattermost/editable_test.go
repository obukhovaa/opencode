package mattermost

import (
	"context"
	"testing"

	"github.com/opencode-ai/opencode/internal/bridge"
)

// TestSendEditableThenEditMessage: SendEditable creates one post (threaded
// under the peer's root post) and EditMessage updates that post — the
// mechanism behind the per-run progress card.
func TestSendEditableThenEditMessage(t *testing.T) {
	t.Parallel()
	mock := newMockServer(t, mattermostTestBotUser())
	a, err := New(Identity{ID: "default", ServerURL: mock.URL(), AccessToken: "tok"}, Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	peer := bridge.PeerRef{Channel: "mattermost", Identity: "default", PeerID: "ch1|root9"}
	tok, err := a.SendEditable(context.Background(), peer, "⏳ Thinking...")
	if err != nil {
		t.Fatalf("SendEditable: %v", err)
	}
	if tok == "" {
		t.Fatal("SendEditable returned an empty token")
	}
	if err := a.EditMessage(context.Background(), peer, tok, "✓ Done · 3 tool calls · 40s"); err != nil {
		t.Fatalf("EditMessage: %v", err)
	}

	mock.mu.Lock()
	creates, updates := mock.createPostCalls, mock.updatePosts
	mock.mu.Unlock()
	if len(creates) != 1 || creates[0].Message != "⏳ Thinking..." || creates[0].RootID != "root9" {
		t.Errorf("createPost calls = %+v; want one Thinking... post under root9", creates)
	}
	if len(updates) != 1 || updates[0].PostID != tok || updates[0].Message != "✓ Done · 3 tool calls · 40s" {
		t.Errorf("updatePost calls = %+v; want one update of %s with the terminal text", updates, tok)
	}
}

func TestEditMessage_RejectsEmptyToken(t *testing.T) {
	t.Parallel()
	mock := newMockServer(t, mattermostTestBotUser())
	a, err := New(Identity{ID: "default", ServerURL: mock.URL(), AccessToken: "tok"}, Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := a.EditMessage(context.Background(), bridge.PeerRef{PeerID: "ch1"}, "", "x"); err == nil {
		t.Error("EditMessage with an empty token should fail")
	}
}
