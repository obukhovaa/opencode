package dialog

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

type completionTestProvider struct{ queries []string }

func (p *completionTestProvider) GetId() string { return "command" }
func (p *completionTestProvider) GetEntry() CompletionItemI {
	return NewCompletionItem(CompletionItem{Title: "Commands", Value: "commands"})
}
func (p *completionTestProvider) GetChildEntries(query string) ([]CompletionItemI, error) {
	p.queries = append(p.queries, query)
	if query == "" || query == "flow" {
		return []CompletionItemI{NewCompletionItem(CompletionItem{Title: "Flow", Value: "flow"})}, nil
	}
	return nil, nil
}
func typeCompletion(c CompletionDialog, value string) {
	for _, r := range value {
		c.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}
func TestCommandCompletionClosesBeforeGoal(t *testing.T) {
	for _, tc := range []struct {
		name string
		msg  tea.Msg
	}{
		{"space", tea.KeyPressMsg{Code: ' ', Text: " "}},
		{"paste", tea.PasteMsg{Content: " review the code"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &completionTestProvider{}
			c := NewCompletionDialogCmp(provider)
			typeCompletion(c, "/flow")
			if !c.Active() {
				t.Fatal("command suggestions missing")
			}
			_, cmd := c.Update(tc.msg)
			if c.Active() || cmd == nil {
				t.Fatal("goal input left completion active")
			}
			if _, ok := cmd().(CompletionDialogCloseMsg); !ok {
				t.Fatal("missing close event")
			}
			for _, q := range provider.queries {
				if strings.Contains(q, " ") {
					t.Fatalf("goal became completion query: %q", q)
				}
			}
			typeCompletion(c, "/flow")
			if !strings.Contains(c.View().Content, "flow") {
				t.Fatal("same command did not refresh after reopening")
			}
		})
	}
}
func TestCommandCompletionEmptyStateDoesNotClaimFileLookup(t *testing.T) {
	c := NewCompletionDialogCmp(&completionTestProvider{})
	typeCompletion(c, "/unknown")
	if view := c.View().Content; !strings.Contains(view, "No matches found") || strings.Contains(view, "file matches") {
		t.Fatalf("wrong empty state: %s", view)
	}
}
