package slack

import (
	"context"
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/bridge"
)

// TestSlackInteractiveQuestionCustomHintAppendsContextBlock asserts that
// the rendered Block Kit message contains a 3rd block (context) when the
// prompt allows custom answers.
func TestSlackInteractiveQuestionCustomHintAppendsContextBlock(t *testing.T) {
	resetCapturedBlocks()
	mock := newMockServer(t)
	mock.server.Config.Handler = blockCapturingHandler(mock.server.Config.Handler, t, mock)
	a, err := New(Identity{ID: "default", BotToken: "xoxb", AppToken: "xapp"}, Options{APIURL: mock.URL() + "/"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a.SetBotUserID("UBOT")
	t.Cleanup(func() { _ = a.Stop() })

	_, err = a.SendInteractiveQuestion(context.Background(),
		bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "D012345"},
		"Ship it?",
		[]bridge.QuestionChoice{
			{Label: "Yes", Value: "Yes", Custom: true},
			{Label: "No", Value: "No", Custom: true},
		},
	)
	if err != nil {
		t.Fatalf("SendInteractiveQuestion: %v", err)
	}
	caps := capturedBlocks()
	if len(caps) == 0 {
		t.Fatalf("no blocks captured")
	}
	bs := caps[len(caps)-1]
	if len(bs) != 3 {
		t.Fatalf("expected 3 blocks (section+actions+context) for Custom=true, got %d", len(bs))
	}
	if got, _ := bs[2]["type"].(string); got != "context" {
		t.Errorf("third block should be context, got %q", got)
	}
	// Verify the hint text is the canonical phrase.
	elems, _ := bs[2]["elements"].([]any)
	if len(elems) == 0 {
		t.Fatal("context block has no elements")
	}
	elem, _ := elems[0].(map[string]any)
	text, _ := elem["text"].(string)
	if !strings.Contains(text, "type your own answer") {
		t.Errorf("hint missing canonical phrase, got %q", text)
	}
}

func TestSlackInteractiveQuestionCustomFalseSkipsHint(t *testing.T) {
	resetCapturedBlocks()
	mock := newMockServer(t)
	mock.server.Config.Handler = blockCapturingHandler(mock.server.Config.Handler, t, mock)
	a, err := New(Identity{ID: "default", BotToken: "xoxb", AppToken: "xapp"}, Options{APIURL: mock.URL() + "/"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a.SetBotUserID("UBOT")
	t.Cleanup(func() { _ = a.Stop() })

	_, err = a.SendInteractiveQuestion(context.Background(),
		bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "D012345"},
		"Ship it?",
		[]bridge.QuestionChoice{
			{Label: "Yes", Value: "Yes", Custom: false},
			{Label: "No", Value: "No", Custom: false},
		},
	)
	if err != nil {
		t.Fatalf("SendInteractiveQuestion: %v", err)
	}
	caps := capturedBlocks()
	if len(caps) == 0 {
		t.Fatalf("no blocks captured")
	}
	bs := caps[len(caps)-1]
	if len(bs) != 2 {
		t.Fatalf("expected 2 blocks (section+actions) for Custom=false, got %d", len(bs))
	}
}
