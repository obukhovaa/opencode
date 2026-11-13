package tui

import (
	tea "charm.land/bubbletea/v2"
	"github.com/opencode-ai/opencode/internal/tui/components/chat"
	"github.com/opencode-ai/opencode/internal/tui/page"
	"testing"
)

type historyRoutingPage struct {
	tea.Model
	messages []tea.Msg
}

func (p *historyRoutingPage) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	p.messages = append(p.messages, msg)
	return p, func() tea.Msg { return "next save" }
}
func TestHistoryPersistenceResultsReachHiddenEditor(t *testing.T) {
	editor, flowPage := &historyRoutingPage{}, &historyRoutingPage{}
	m := appModel{currentPage: page.FlowsPage, pages: map[page.PageID]tea.Model{page.ChatPage: editor, page.FlowsPage: flowPage}}
	for _, msg := range []tea.Msg{chat.InputHistoryLoadedMsg{}, chat.InputHistorySavedMsg{}} {
		updated, cmd := m.Update(msg)
		m = updated.(appModel)
		if cmd == nil || cmd() != "next save" {
			t.Fatal("history save queue not continued")
		}
	}
	if len(editor.messages) != 2 || len(flowPage.messages) != 0 || m.currentPage != page.FlowsPage {
		t.Fatal("history result routed to the visible flow page")
	}
}
