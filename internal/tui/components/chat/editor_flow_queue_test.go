package chat

import (
	"charm.land/bubbles/v2/textarea"
	"github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/session"
	"testing"
)

func TestFlowSteeringSavesAreSerializedAndFailedBurstKeepsOrder(t *testing.T) {
	m := &editorCmp{app: &app.App{}, session: session.Session{ID: "root"}, textarea: textarea.New()}
	m.textarea.SetValue("new draft")
	first := m.enqueueFlowSteering("root", "first instruction")
	if first == nil {
		t.Fatal("first save missing")
	}
	if second := m.enqueueFlowSteering("root", "second instruction"); second != nil {
		t.Fatal("second save started before first completed")
	}
	// The flow ended before storage accepted the first submission. Restore the
	// complete burst ahead of text typed meanwhile, preserving its original order.
	m.update(first())
	if got := m.textarea.Value(); got != "first instruction\nsecond instruction\nnew draft" {
		t.Fatalf("restored draft: %q", got)
	}
	if m.hasPendingFlowSteering("root") {
		t.Fatal("failed burst still queued")
	}
}
