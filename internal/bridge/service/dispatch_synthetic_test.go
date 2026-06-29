package service

import (
	"context"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/pubsub"
)

// TestHandlePartEvent_SyntheticToolCallSkipped verifies the bridge's
// chat-bridge spec guard: a PartEvent whose Synthetic flag is true must
// short-circuit handlePartEvent BEFORE recordToolCallStart is invoked,
// so no outbound tool-update indicator is dispatched.
//
// We construct the sessionDispatch by hand (avoiding newSessionDispatch
// which launches goroutines that depend on a fully-wired Service.app).
// This isolates the early-return guard from the wider dispatch loop.
func TestHandlePartEvent_SyntheticToolCallSkipped(t *testing.T) {
	d := &sessionDispatch{
		svc: &Service{
			cfg: &bridge.Config{ToolUpdatesEnabled: true},
			ctx: context.Background(),
		},
		sessionID: "S1",
	}

	syntheticEv := pubsub.Event[message.PartEvent]{
		Type: pubsub.UpdatedEvent,
		Payload: message.PartEvent{
			SessionID: "S1",
			MessageID: "msg-syn",
			Part: message.ToolCall{
				ID:       "tcall-syn",
				Name:     "bash",
				Input:    `{"command":"echo hi"}`,
				Finished: true,
			},
			Synthetic: true,
			Time:      time.Now().UnixMilli(),
		},
	}
	d.handlePartEvent(syntheticEv)
	if _, ok := d.toolCallStart.Load("tcall-syn"); ok {
		t.Error("synthetic ToolCall should not record toolCallStart (guard bypassed)")
	}

	// Negative control: a non-synthetic ToolCall must record toolCallStart.
	// recordToolCallStart runs synchronously BEFORE emitToolRender's
	// fire-and-forget goroutine, so we observe the side effect even
	// without a fully-wired Service.app.
	d.handlePartEvent(pubsub.Event[message.PartEvent]{
		Type: pubsub.UpdatedEvent,
		Payload: message.PartEvent{
			SessionID: "S1",
			MessageID: "msg-real",
			Part: message.ToolCall{
				ID:       "tcall-real",
				Name:     "bash",
				Input:    `{"command":"echo hi"}`,
				Finished: true,
			},
			Synthetic: false,
			Time:      time.Now().UnixMilli(),
		},
	})
	if _, ok := d.toolCallStart.Load("tcall-real"); !ok {
		t.Error("real ToolCall should record toolCallStart (guard mis-fired)")
	}
}
