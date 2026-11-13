package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/opencode-ai/opencode/internal/question"
	"github.com/opencode-ai/opencode/internal/tui/components/dialog"
	"github.com/opencode-ai/opencode/internal/tui/page"
)

type focusTestModel struct{ tea.Model }

func (focusTestModel) View() tea.View {
	return tea.NewView(lipgloss.NewStyle().Width(100).Height(30).Render("chat"))
}

type focusTestPermission struct {
	dialog.PermissionDialogCmp
	keys int
}

func (p *focusTestPermission) View() tea.View { return tea.NewView("VISIBLE PERMISSION") }
func (p *focusTestPermission) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := msg.(tea.KeyPressMsg); ok {
		p.keys++
	}
	return p, nil
}

func TestVisibleDialogOwnsInputWhenPermissionAndQuestionOverlap(t *testing.T) {
	permission := &focusTestPermission{}
	q := dialog.NewQuestionDialogCmp()
	q.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	q.SetQuestion(question.Request{ID: "pending", Questions: []question.Prompt{{Question: "PENDING QUESTION", Options: []question.Option{{Label: "answer"}}}}})
	m := appModel{
		width: 100, height: 30, currentPage: page.ChatPage,
		pages:  map[page.PageID]tea.Model{page.ChatPage: focusTestModel{}},
		topbar: focusTestModel{}, status: focusTestModel{},
		showPermissions: true, permissions: permission,
		showQuestionDialog: true, questionDialog: q,
		showInitDialog: true,
	}
	view := m.View().Content
	if !strings.Contains(view, "VISIBLE PERMISSION") || strings.Contains(view, "PENDING QUESTION") {
		t.Fatal("question obscures the permission dialog receiving input")
	}
	updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(appModel)
	if permission.keys != 1 {
		t.Fatal("visible permission did not receive Enter")
	}
	// Escape must also reach the visible prompt, without closing the hidden
	// initialization dialog (or touching its configuration).
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = updated.(appModel)
	if permission.keys != 2 || !m.showInitDialog {
		t.Fatal("Escape reached an obscured dialog")
	}
	m.showPermissions = false
	view = m.View().Content
	if !strings.Contains(view, "PENDING QUESTION") || strings.Contains(view, "VISIBLE PERMISSION") {
		t.Fatal("pending question did not become visible")
	}
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("question did not receive Enter")
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			if c != nil {
				msg = c()
			}
		}
	}
	answer, ok := msg.(dialog.QuestionResponseMsg)
	if !ok || answer.RequestID != "pending" || answer.Answers[0][0] != "answer" {
		t.Fatalf("wrong question answer: %#v", msg)
	}
}
