package mattermost

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/bridge"
)

// E.3 (openspec change multi-platform-bridge-providers): fork-side tests
// for SendInteractiveQuestion's button render and the fallback-on-
// missing-URL / fallback-on-multi-select paths, per
// specs/mattermost-question-actions/spec.md.

func TestMattermostSendInteractiveQuestionPostsButtons(t *testing.T) {
	t.Parallel()
	mock := newMockServer(t, mattermostTestBotUser())
	a, err := New(Identity{ID: "default", ServerURL: mock.URL(), AccessToken: "tok"}, Options{
		ActionURLBase: "https://orchestrator.example.com",
		ActionSecret:  "shared-secret",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	resolved, err := a.SendInteractiveQuestion(context.Background(), bridge.PeerRef{PeerID: "ch1"}, "Proceed?", []bridge.QuestionChoice{
		{Label: "Yes", Value: "Yes"},
		{Label: "No", Value: "No"},
	})
	if err != nil {
		t.Fatalf("SendInteractiveQuestion: %v", err)
	}
	if resolved == "" {
		t.Error("resolved peer should be the new post's composite id (top-level post)")
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
	actions, ok := att["actions"].([]any)
	if !ok || len(actions) != 2 {
		t.Fatalf("actions = %+v, want 2 (one per choice)", att["actions"])
	}
	for i, want := range []string{"Yes", "No"} {
		action, ok := actions[i].(map[string]any)
		if !ok {
			t.Fatalf("action[%d] shape = %T", i, actions[i])
		}
		if action["name"] != want {
			t.Errorf("action[%d].name = %v, want %q", i, action["name"], want)
		}
		integ, ok := action["integration"].(map[string]any)
		if !ok {
			t.Fatalf("action[%d].integration shape = %T", i, action["integration"])
		}
		if integ["url"] != "https://orchestrator.example.com/router/mattermost/attachment-action" {
			t.Errorf("action[%d].integration.url = %v", i, integ["url"])
		}
		ctxMap, ok := integ["context"].(map[string]any)
		if !ok {
			t.Fatalf("action[%d].integration.context shape = %T", i, integ["context"])
		}
		if ctxMap["peerId"] != "ch1" {
			t.Errorf("action[%d].context.peerId = %v", i, ctxMap["peerId"])
		}
		if ctxMap["choice"] != want {
			t.Errorf("action[%d].context.choice = %v, want %q", i, ctxMap["choice"], want)
		}
		if s, _ := ctxMap["requestId"].(string); s == "" {
			t.Errorf("action[%d].context.requestId is empty", i)
		}
		token, _ := ctxMap["token"].(string)
		if token == "" {
			t.Errorf("action[%d].context.token is empty", i)
		}
	}
	// Different choices under the same request MUST carry different
	// tokens — the MAC covers the choice, so a token minted for "Yes"
	// must not validate "No" (see mattermost-question-actions spec:
	// "A click whose token was computed for a different choice is rejected").
	action0 := actions[0].(map[string]any)["integration"].(map[string]any)["context"].(map[string]any)
	action1 := actions[1].(map[string]any)["integration"].(map[string]any)["context"].(map[string]any)
	if action0["token"] == action1["token"] {
		t.Error("tokens for different choices must differ")
	}
}

// TestMattermostActionTokenRoundTrips pins the exact construction
// (HMAC-SHA256 over NUL-joined fields) so a change here is caught rather
// than silently diverging from the orchestrator's verifying half.
func TestMattermostActionTokenRoundTrips(t *testing.T) {
	t.Parallel()
	token := computeActionToken("secret", "mattermost", "default", "ch1|root1", "req-1", "Yes")
	if token == "" {
		t.Fatal("token is empty")
	}
	if !verifyActionToken("secret", "mattermost", "default", "ch1|root1", "req-1", "Yes", token) {
		t.Error("verifyActionToken should accept the token it was computed for")
	}
	if verifyActionToken("secret", "mattermost", "default", "ch1|root1", "req-1", "No", token) {
		t.Error("verifyActionToken must reject a token presented for a different choice")
	}
	if verifyActionToken("wrong-secret", "mattermost", "default", "ch1|root1", "req-1", "Yes", token) {
		t.Error("verifyActionToken must reject a token keyed by the wrong secret")
	}
}

func TestMattermostSendInteractiveQuestionFailsWithoutActionURL(t *testing.T) {
	t.Parallel()
	mock := newMockServer(t, mattermostTestBotUser())
	a, err := New(Identity{ID: "default", ServerURL: mock.URL(), AccessToken: "tok"}, Options{
		// ActionURLBase deliberately unset — pod with no orchestrator URL.
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = a.SendInteractiveQuestion(context.Background(), bridge.PeerRef{PeerID: "ch1"}, "Proceed?", []bridge.QuestionChoice{
		{Label: "Yes", Value: "Yes"},
	})
	if !errors.Is(err, errActionURLUnavailable) {
		t.Errorf("err = %v, want errActionURLUnavailable", err)
	}
	mock.mu.Lock()
	calls := len(mock.createPostCalls)
	mock.mu.Unlock()
	if calls != 0 {
		t.Errorf("createPostCalls = %d, want 0 (send must fail before posting anything)", calls)
	}
}

func TestMattermostSendInteractiveMultiSelectAlwaysFallsBackToText(t *testing.T) {
	t.Parallel()
	mock := newMockServer(t, mattermostTestBotUser())
	a, err := New(Identity{ID: "default", ServerURL: mock.URL(), AccessToken: "tok"}, Options{
		ActionURLBase: "https://orchestrator.example.com",
		ActionSecret:  "shared-secret",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = a.SendInteractiveMultiSelect(context.Background(), bridge.PeerRef{PeerID: "ch1"}, "Pick some", []bridge.QuestionChoice{
		{Label: "auth", Value: "auth"},
		{Label: "billing", Value: "billing"},
	})
	if !errors.Is(err, errMultiSelectNotSupported) {
		t.Errorf("err = %v, want errMultiSelectNotSupported", err)
	}
	mock.mu.Lock()
	calls := len(mock.createPostCalls)
	mock.mu.Unlock()
	if calls != 0 {
		t.Errorf("createPostCalls = %d, want 0 — no dead widget should ever be posted", calls)
	}
}

func TestMattermostSendInteractiveQuestionRequiresChoices(t *testing.T) {
	t.Parallel()
	a, err := New(Identity{ID: "default", ServerURL: "https://mm.example.com", AccessToken: "tok"}, Options{
		ActionURLBase: "https://orchestrator.example.com",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = a.SendInteractiveQuestion(context.Background(), bridge.PeerRef{PeerID: "ch1"}, "Proceed?", nil)
	if err == nil || !strings.Contains(err.Error(), "at least one choice") {
		t.Errorf("err = %v, want 'at least one choice'", err)
	}
}

func TestMattermostSendInteractiveQuestionInvalidPeerID(t *testing.T) {
	t.Parallel()
	a, err := New(Identity{ID: "default", ServerURL: "https://mm.example.com", AccessToken: "tok"}, Options{
		ActionURLBase: "https://orchestrator.example.com",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = a.SendInteractiveQuestion(context.Background(), bridge.PeerRef{PeerID: ""}, "Proceed?", []bridge.QuestionChoice{
		{Label: "Yes", Value: "Yes"},
	})
	if err == nil || !strings.Contains(err.Error(), "invalid peer-id") {
		t.Errorf("err = %v, want invalid peer-id", err)
	}
}

func mattermostTestBotUser() User {
	return User{ID: "bot123", Username: "testbot"}
}
