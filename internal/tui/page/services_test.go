package page

import (
	tea "charm.land/bubbletea/v2"
	"strings"
	"testing"
)

func TestServicesPageNavigationAndBounds(t *testing.T) {
	p := &servicesPage{kind: MCPPage, width: 60, height: 15, rows: []serviceRow{{"one", "connected", "tool_a\ntool_b"}, {"two", "disabled", ""}}}
	_, _ = p.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if p.selected != 1 {
		t.Fatal("selection did not move")
	}
	_, _ = p.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if p.selected != 1 {
		t.Fatal("selection escaped rows")
	}
	_, cmd := p.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	msg := cmd().(PageChangeMsg)
	if msg.ID != ChatPage {
		t.Fatal("Escape did not return to chat")
	}
	if !strings.Contains(p.View().Content, "two") {
		t.Fatal("selected server missing")
	}
}

func TestServicePageRejectsOldSessionSnapshot(t *testing.T) {
	p := &servicesPage{kind: ModifiedFilesPage, sessionID: "new", generation: 3}
	_, _ = p.Update(servicesLoaded{kind: ModifiedFilesPage, sessionID: "old", generation: 2, rows: []serviceRow{{name: "old-secret.txt"}}})
	if len(p.rows) != 0 {
		t.Fatal("old session rows displayed")
	}
	_, _ = p.Update(servicesLoaded{kind: ModifiedFilesPage, sessionID: "new", generation: 3, rows: []serviceRow{{name: "current.txt"}}})
	if len(p.rows) != 1 || p.rows[0].name != "current.txt" {
		t.Fatal("current rows rejected")
	}
}
