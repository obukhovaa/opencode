package mattermost

import (
	"context"
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/bridge"
)

func testMattermostAdapter(t *testing.T) (*Adapter, *mockServer) {
	t.Helper()
	bot := User{ID: "bot1", Username: "testbot"}
	mock := newMockServer(t, bot)
	a, _, stop := startAdapter(t,
		Identity{ID: "local", ServerURL: mock.URL(), AccessToken: "tok"},
		mock, 4)
	t.Cleanup(stop)
	return a, mock
}

// TestSendQueuedAck_Mattermost verifies SendQueuedAck creates a post and
// returns the post ID as the token.
func TestSendQueuedAck_Mattermost(t *testing.T) {
	t.Parallel()
	a, mock := testMattermostAdapter(t)

	peer := bridge.PeerRef{Channel: "mattermost", Identity: "local", PeerID: "channel1"}
	tok, err := a.SendQueuedAck(context.Background(), peer, 1)
	if err != nil {
		t.Fatalf("SendQueuedAck: %v", err)
	}
	if tok == "" {
		t.Fatal("SendQueuedAck returned empty token")
	}
	// mock createPostResp returns ID "new_post"
	if tok != "new_post" {
		t.Errorf("token = %q, want \"new_post\"", tok)
	}

	mock.mu.Lock()
	posts := mock.createPostCalls
	mock.mu.Unlock()
	if len(posts) == 0 {
		t.Fatal("CreatePost was not called")
	}
	if !strings.Contains(posts[len(posts)-1].Message, "⏳") {
		t.Errorf("ack text missing ⏳ glyph: %q", posts[len(posts)-1].Message)
	}
}

// TestUpdateQueuedAck_Mattermost verifies UpdateQueuedAck calls UpdatePost
// and resolves with the "▶" text when position == 0.
func TestUpdateQueuedAck_Mattermost(t *testing.T) {
	t.Parallel()
	a, mock := testMattermostAdapter(t)

	peer := bridge.PeerRef{Channel: "mattermost", Identity: "local", PeerID: "channel1"}
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
	updates := mock.updatePosts
	mock.mu.Unlock()

	if len(updates) < 2 {
		t.Fatalf("UpdatePost called %d times, want ≥2", len(updates))
	}
	last := updates[len(updates)-1]
	if !strings.Contains(last.Message, "▶") {
		t.Errorf("resolve update text missing ▶ glyph: %q", last.Message)
	}
	if last.PostID != tok {
		t.Errorf("UpdatePost PostID = %q, want token %q", last.PostID, tok)
	}
}

// TestUpdateQueuedAck_Mattermost_EmptyToken rejects an empty token.
func TestUpdateQueuedAck_Mattermost_EmptyToken(t *testing.T) {
	t.Parallel()
	a, _ := testMattermostAdapter(t)
	err := a.UpdateQueuedAck(context.Background(),
		bridge.PeerRef{Channel: "mattermost", Identity: "local", PeerID: "channel1"},
		"", 1)
	if err == nil {
		t.Error("expected error for empty token")
	}
}
