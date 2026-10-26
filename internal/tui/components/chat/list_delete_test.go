package chat

import (
	"context"
	"testing"

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
