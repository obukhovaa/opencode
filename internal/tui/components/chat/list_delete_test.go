package chat

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/pubsub"
)

// The agent deletes the partial assistant message of a turn whose stream the
// peer reset, then resends the turn: the list drops it rather than showing a
// half-written message that never finishes.
func TestMessages_DeletedMessageLeavesTheList(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := newMessagesForTest(t, app.NewForTest(ctx, &editorFakeAgent{}), 80)
	partial := message.Message{ID: "m2", SessionID: "test-session", Role: message.Assistant}
	m.messages = append(m.messages, partial)
	m.currentMsgID = partial.ID

	m.Update(pubsub.Event[message.Message]{Type: pubsub.DeletedEvent, Payload: partial})

	if len(m.messages) != 1 || m.messages[0].ID != "m1" {
		t.Fatalf("messages = %+v, want only m1", m.messages)
	}
	if m.currentMsgID != "m1" {
		t.Errorf("currentMsgID = %q, want m1", m.currentMsgID)
	}

	resent := message.Message{ID: "m3", SessionID: "test-session", Role: message.Assistant}
	m.Update(pubsub.Event[message.Message]{Type: pubsub.CreatedEvent, Payload: resent})
	if len(m.messages) != 2 || m.messages[1].ID != "m3" || m.currentMsgID != "m3" {
		t.Errorf("after the resend: messages = %+v, currentMsgID = %q", m.messages, m.currentMsgID)
	}
}

// A delete for another session's message leaves the list alone.
func TestMessages_DeletedEventOtherSessionIgnored(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := newMessagesForTest(t, app.NewForTest(ctx, &editorFakeAgent{}), 80)

	m.Update(pubsub.Event[message.Message]{Type: pubsub.DeletedEvent, Payload: message.Message{ID: "m1", SessionID: "other"}})

	if len(m.messages) != 1 {
		t.Fatalf("messages = %+v, want m1 kept", m.messages)
	}
}

// renderResults runs cmd and returns every renderFinishedMsg it yields,
// flattening batches.
func renderResults(t *testing.T, cmd tea.Cmd) []renderFinishedMsg {
	t.Helper()
	if cmd == nil {
		return nil
	}
	switch msg := cmd().(type) {
	case renderFinishedMsg:
		return []renderFinishedMsg{msg}
	case tea.BatchMsg:
		var out []renderFinishedMsg
		for _, c := range msg {
			out = append(out, renderResults(t, c)...)
		}
		return out
	}
	return nil
}

func uiMessageIDs(m *messagesCmp) map[string]bool {
	ids := make(map[string]bool)
	for _, u := range m.uiMessages {
		ids[u.ID] = true
	}
	return ids
}

// An async render started before the partial message was deleted still holds
// it. Its result must be dropped even after the delete starts a replacement
// render, whichever of the two lands first, and the replacement's result must
// be applied.
func TestMessages_RenderStartedBeforeDeleteIsDropped(t *testing.T) {
	tests := []struct {
		name       string
		staleFirst bool
	}{
		{name: "stale result lands before the replacement", staleFirst: true},
		{name: "stale result lands after the replacement", staleFirst: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			m := newMessagesForTest(t, app.NewForTest(ctx, &editorFakeAgent{}), 80)
			partial := message.Message{
				ID:        "m2",
				SessionID: "test-session",
				Role:      message.Assistant,
				Parts:     []message.ContentPart{message.TextContent{Text: "half-written answer"}},
			}
			m.messages = append(m.messages, partial)
			m.currentMsgID = partial.ID

			previousRender := m.renderViewAsync()
			_, cmd := m.Update(pubsub.Event[message.Message]{Type: pubsub.DeletedEvent, Payload: partial})
			replacements := renderResults(t, cmd)
			if len(replacements) != 1 {
				t.Fatalf("delete started %d replacement renders, want 1", len(replacements))
			}

			if tt.staleFirst {
				m.Update(previousRender())
				if ids := uiMessageIDs(m); ids["m2"] {
					t.Fatalf("stale render restored the deleted message: uiMessages = %v", ids)
				}
				if !m.rendering {
					t.Errorf("stale render cleared the in-flight flag of the replacement render")
				}
				m.Update(replacements[0])
			} else {
				m.Update(replacements[0])
				m.Update(previousRender())
			}

			ids := uiMessageIDs(m)
			if ids["m2"] || !ids["m1"] {
				t.Fatalf("uiMessages = %v, want m1 from the replacement render and no m2", ids)
			}
			if _, ok := m.cachedContent["m2"]; ok {
				t.Errorf("stale render cached the deleted message")
			}
			if _, ok := m.cachedContent["m1"]; !ok {
				t.Errorf("replacement render did not cache m1")
			}
			if m.rendering {
				t.Errorf("rendering still set after the replacement render landed")
			}
		})
	}
}
