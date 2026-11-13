package chat

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"

	"github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/session"
	"github.com/opencode-ai/opencode/internal/tui/util"
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

func TestFlowSteeringAcceptedShowsAcknowledgement(t *testing.T) {
	m := &editorCmp{session: session.Session{ID: "root"}, textarea: textarea.New(), flowSteeringQueue: []flowSteeringResult{{sessionID: "root", text: "new direction"}}}
	m.textarea.SetValue("next draft")
	_, cmd := m.update(flowSteeringResult{sessionID: "root", text: "new direction"})
	if cmd == nil {
		t.Fatal("no acceptance acknowledgement")
	}
	result := cmd()
	// Batch collapses to the only command when no additional save is pending.
	if batch, ok := result.(tea.BatchMsg); ok {
		for _, c := range batch {
			if c != nil {
				result = c()
			}
		}
	}
	info, ok := result.(util.InfoMsg)
	if !ok || !strings.Contains(info.Msg, "queued") || !strings.Contains(info.Msg, "ctrl+g") {
		t.Fatalf("missing queue acknowledgement: %#v", result)
	}
	if m.textarea.Value() != "next draft" || m.hasPendingFlowSteering("root") {
		t.Fatal("acceptance changed draft or retained the saving entry")
	}
}
