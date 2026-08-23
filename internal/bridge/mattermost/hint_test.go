package mattermost

import (
	"context"
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/bridge"
)

// These assert the bridge-question-custom-answer-hint contract against
// SendInteractiveQuestion — the code path that actually runs. They
// previously drove buildMultiSelectAttachment, which became unreachable
// when multi-select was descoped to the numbered-text fallback (design.md
// D11): the footer behaviour was still covered, but on a builder no
// production call site reached any more.

// questionAttachment drives one SendInteractiveQuestion and returns the
// single attachment dict it posted.
func questionAttachment(t *testing.T, choices []bridge.QuestionChoice) map[string]any {
	t.Helper()
	mock := newMockServer(t, mattermostTestBotUser())
	a, err := New(Identity{ID: "default", ServerURL: mock.URL(), AccessToken: "tok"}, Options{
		ActionURLBase: "https://orchestrator.example.com",
		ActionSecret:  "shared-secret",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := a.SendInteractiveQuestion(
		context.Background(), bridge.PeerRef{PeerID: "ch1"}, "Pick capabilities", choices,
	); err != nil {
		t.Fatalf("SendInteractiveQuestion: %v", err)
	}

	mock.mu.Lock()
	calls := mock.createPostCalls
	mock.mu.Unlock()
	if len(calls) != 1 {
		t.Fatalf("createPostCalls = %d, want 1", len(calls))
	}
	atts, ok := calls[0].Props["attachments"].([]any)
	if !ok || len(atts) != 1 {
		t.Fatalf("attachments = %+v", calls[0].Props["attachments"])
	}
	att, ok := atts[0].(map[string]any)
	if !ok {
		t.Fatalf("attachment shape = %T", atts[0])
	}
	return att
}

func TestSendInteractiveQuestionAddsFooterWhenCustom(t *testing.T) {
	t.Parallel()
	att := questionAttachment(t, []bridge.QuestionChoice{
		{Label: "auth", Value: "auth", Custom: true},
		{Label: "billing", Value: "billing", Custom: true},
	})
	footer, _ := att["footer"].(string)
	if !strings.Contains(footer, "reply in this thread") {
		t.Errorf("expected discoverability footer, got %q", footer)
	}
	pretext, _ := att["pretext"].(string)
	if pretext != "Pick capabilities" {
		t.Errorf("pretext = %q; want prompt", pretext)
	}
}

func TestSendInteractiveQuestionOmitsFooterWhenCustomFalse(t *testing.T) {
	t.Parallel()
	att := questionAttachment(t, []bridge.QuestionChoice{
		{Label: "auth", Value: "auth", Custom: false},
	})
	if _, ok := att["footer"]; ok {
		t.Errorf("custom=false MUST NOT add footer; got %+v", att)
	}
}
