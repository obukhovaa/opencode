package dialog

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	questionpkg "github.com/opencode-ai/opencode/internal/question"
)

func TestQuestionDialogWidthIncludesBordersAndWrapsLongText(t *testing.T) {
	for _, width := range []int{40, 80, 121, 256} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			q := NewQuestionDialogCmp()
			// The terminal normally resizes before any question has arrived.
			q.Update(tea.WindowSizeMsg{Width: width, Height: 100})
			q.SetQuestion(questionpkg.Request{ID: "q", Questions: []questionpkg.Prompt{{
				Question: strings.Repeat("Long question with 日本語 and emoji 🧭. ", 12),
				Options:  []questionpkg.Option{{Label: strings.Repeat("label", 30), Description: strings.Repeat("A description with spaces. ", 15)}},
			}}})
			for _, custom := range []bool{false, true} {
				if custom {
					q.Update(tea.KeyPressMsg{Code: tea.KeyTab})
					q.(*questionDialogCmp).customInput.SetValue(strings.Repeat("custom answer ", 30))
				}
				view := ansi.Strip(q.View().Content)
				want := width * 4 / 5
				if got := lipgloss.Width(view); got != want {
					t.Fatalf("custom=%v: dialog width %d, want %d", custom, got, want)
				}
				lines := strings.Split(view, "\n")
				for i, line := range lines {
					if lipgloss.Width(line) != want {
						t.Fatalf("row %d has wrong width: %q", i, line)
					}
					if i > 0 && i < len(lines)-1 && (!strings.HasPrefix(line, "│") || !strings.HasSuffix(line, "│")) {
						t.Fatalf("row %d lost a border: %q", i, line)
					}
				}
			}
			q.Update(tea.WindowSizeMsg{Width: 100, Height: 100})
			if got := lipgloss.Width(q.View().Content); got != 80 {
				t.Fatalf("resize: width %d", got)
			}
		})
	}
}
