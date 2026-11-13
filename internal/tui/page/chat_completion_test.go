package page

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/opencode-ai/opencode/internal/completions"
	"github.com/opencode-ai/opencode/internal/slashcmd"
	"github.com/opencode-ai/opencode/internal/tui/components/dialog"
	"github.com/opencode-ai/opencode/internal/tui/layout"
)

type completionInputLayout struct {
	layout.SplitPaneLayout
	input strings.Builder
	enter bool
}

func (l *completionInputLayout) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {
	case tea.KeyPressMsg:
		if m.Code == tea.KeyEnter {
			l.enter = true
		} else {
			l.input.WriteString(m.Text)
		}
	case tea.PasteMsg:
		l.input.WriteString(m.Content)
	}
	return l, nil
}
func TestFlowGoalDismissesCompletionWithoutWaitingForCloseEvent(t *testing.T) {
	for _, paste := range []bool{false, true} {
		t.Run(map[bool]string{false: "typed", true: "pasted"}[paste], func(t *testing.T) {
			input := &completionInputLayout{}
			p := &chatPage{layout: input, commandCompletionDialog: dialog.NewCompletionDialogCmp(completions.NewCommandCompletionProvider([]dialog.Command{{CommandInfo: slashcmd.CommandInfo{ID: "flow", Title: "Flow"}}}))}
			for _, r := range "/flow" {
				p.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
			}
			if !p.showCommandCompletionDialog {
				t.Fatal("missing command popup")
			}
			// Deliberately do not process asynchronous close commands: the next
			// key must already go to the editor, even during a fast typing burst.
			if paste {
				p.Update(tea.PasteMsg{Content: " review code"})
			} else {
				for _, r := range " review code" {
					p.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
				}
			}
			if p.showCommandCompletionDialog || p.showCompletionDialog {
				t.Fatal("goal kept a completion popup open")
			}
			p.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			if input.input.String() != "/flow review code" || !input.enter {
				t.Fatalf("input lost or Enter intercepted: %q, enter=%v", input.input.String(), input.enter)
			}
		})
	}
}
