package page

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/tui/components/chat"
)

type fakeFlowController struct{ controls []string }

func (f *fakeFlowController) DynamicFlowSnapshot(context.Context, string) (app.FlowSnapshot, error) {
	return app.FlowSnapshot{FlowID: "test"}, nil
}
func (f *fakeFlowController) ControlFlow(_ context.Context, root, step, action string) error {
	f.controls = append(f.controls, root+":"+step+":"+action)
	return nil
}
func TestFlowPageRejectsStaleSessionResults(t *testing.T) {
	p := NewFlowsPage(&fakeFlowController{}).(*flowsPage)
	_, old := p.Update(chat.SessionSelectedMsg{ID: "old"})
	p.Update(chat.SessionSelectedMsg{ID: "new"})
	p.Update(old())
	if p.snapshot.FlowID != "" {
		t.Fatal("old session snapshot applied to new session")
	}
}
func TestFlowPageCancellationOffersExplicitNextAction(t *testing.T) {
	controller := &fakeFlowController{}
	p := NewFlowsPage(controller).(*flowsPage)
	p.root = "root"
	p.snapshot = app.FlowSnapshot{FlowID: "flow", Steps: []app.FlowStepSnapshot{{ID: "review"}}}
	_, cmd := p.Update(tea.KeyPressMsg{Code: 'c', Text: "c"})
	if cmd == nil || !p.choosing {
		t.Fatal("cancel must offer next action")
	}
	cmd()
	_, cmd = p.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
	if cmd == nil {
		t.Fatal("skip missing")
	}
	cmd()
	if len(controller.controls) != 2 || controller.controls[0] != "root:review:cancel" || controller.controls[1] != "root:review:skip" {
		t.Fatalf("controls: %v", controller.controls)
	}
}
