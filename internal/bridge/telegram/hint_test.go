package telegram

import (
	"context"
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"

	"github.com/opencode-ai/opencode/internal/bridge"
)

// TestTelegramInteractiveQuestionAppendsCustomHint asserts the prompt
// text gets an italic suffix when Custom is enabled.
func TestTelegramInteractiveQuestionAppendsCustomHint(t *testing.T) {
	t.Parallel()
	mock := newMockServer(t)
	a, err := New(Identity{ID: "default", Token: "tg-token"}, Options{ServerURL: mock.URL()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a.SetMe(&models.User{ID: 999, Username: "routerbot", IsBot: true})
	t.Cleanup(func() { _ = a.Stop() })

	_, err = a.SendInteractiveQuestion(context.Background(),
		bridge.PeerRef{Channel: "telegram", Identity: "default", PeerID: "555"},
		"Ship it?",
		[]bridge.QuestionChoice{
			{Label: "Yes", Value: "Yes", Custom: true},
			{Label: "No", Value: "No", Custom: true},
		},
	)
	if err != nil {
		t.Fatalf("SendInteractiveQuestion: %v", err)
	}
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.sendMsg) == 0 {
		t.Fatal("no sendMessage captured")
	}
	got := mock.sendMsg[0].Text
	if !strings.Contains(got, "Or reply with your own answer") {
		t.Errorf("expected custom-hint suffix, got %q", got)
	}
}

func TestTelegramInteractiveQuestionCustomFalseSkipsHint(t *testing.T) {
	t.Parallel()
	mock := newMockServer(t)
	a, err := New(Identity{ID: "default", Token: "tg-token"}, Options{ServerURL: mock.URL()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a.SetMe(&models.User{ID: 999, Username: "routerbot", IsBot: true})
	t.Cleanup(func() { _ = a.Stop() })

	_, err = a.SendInteractiveQuestion(context.Background(),
		bridge.PeerRef{Channel: "telegram", Identity: "default", PeerID: "555"},
		"Ship it?",
		[]bridge.QuestionChoice{
			{Label: "Yes", Value: "Yes", Custom: false},
			{Label: "No", Value: "No", Custom: false},
		},
	)
	if err != nil {
		t.Fatalf("SendInteractiveQuestion: %v", err)
	}
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.sendMsg) == 0 {
		t.Fatal("no sendMessage captured")
	}
	got := mock.sendMsg[0].Text
	if strings.Contains(got, "Or reply with your own answer") {
		t.Errorf("custom-disabled prompt MUST NOT carry the hint, got %q", got)
	}
}
