package page

import (
	"fmt"
	"strings"
	"testing"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/opencode-ai/opencode/internal/app"
)

func TestManagementPagesFitTerminalAfterResize(t *testing.T) {
	for _, size := range []struct{ w, h int }{{20, 8}, {40, 14}, {80, 24}, {120, 30}, {1, 1}} {
		t.Run(fmt.Sprintf("%dx%d", size.w, size.h), func(t *testing.T) {
			service := &servicesPage{kind: MCPPage, selected: 1, rows: []serviceRow{{"first", "connected", "search\nget"}, {"selected-server", "disabled", "tool_with_a_very_long_name\n" + strings.Repeat("output\n", 100)}}}
			tasks := &activitiesPage{viewport: viewport.New(), items: []activity{{title: "running command", state: "running"}}}
			flows := NewFlowsPage(nil).(*flowsPage)
			flows.snapshot = app.FlowSnapshot{FlowID: "test-flow", Status: "running", Steps: []app.FlowStepSnapshot{{ID: "implement", Agent: "coder", Status: "running", Prompt: strings.Repeat("long prompt ", 30), Output: strings.Repeat("output\n", 100)}}}
			for _, p := range []interface {
				SetSize(int, int) tea.Cmd
				View() tea.View
			}{service, tasks, flows} {
				p.SetSize(size.w, size.h)
				content := p.View().Content
				lines := strings.Split(content, "\n")
				if len(lines) > size.h {
					t.Fatalf("%T rendered %d rows, height %d", p, len(lines), size.h)
				}
				for _, line := range lines {
					if ansi.StringWidth(line) > size.w {
						t.Fatalf("%T width %d exceeds %d", p, ansi.StringWidth(line), size.w)
					}
				}
			}
		})
	}
}
func TestServicePresentationKeepsSelectedStatusAndSidebarIndicators(t *testing.T) {
	p := &servicesPage{kind: MCPPage, selected: 1, rows: []serviceRow{{"first", "connected", "one"}, {"selected-server", "disabled", "two"}}}
	p.SetSize(100, 24)
	text := ansi.Strip(p.View().Content)
	t.Log("\n" + text)
	for _, want := range []string{"MCP servers", "▸ selected-server", "● connected", "○ disabled", "Tools · 1", "Esc back"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in:\n%s", want, text)
		}
	}
}
func TestFlowPresentationKeepsLateSelectionVisible(t *testing.T) {
	p := NewFlowsPage(nil).(*flowsPage)
	p.snapshot = app.FlowSnapshot{FlowID: "large-flow", Status: "running"}
	for i := 0; i < 24; i++ {
		p.snapshot.Steps = append(p.snapshot.Steps, app.FlowStepSnapshot{ID: fmt.Sprintf("step-%02d", i), Agent: "coder", Status: "pending", Prompt: "Inspect this task"})
	}
	p.selected = 23
	p.SetSize(100, 24)
	if !strings.Contains(ansi.Strip(p.View().Content), "step-23") {
		t.Fatal("selected step scrolled out of list")
	}
}

func TestCompactFooterRetainsEscapeHint(t *testing.T) {
	text := pageFooter("↑↓ select · r reconnect · e enable · d disable · f defer · Esc back", 30)
	if !strings.Contains(text, "Esc back") || ansi.StringWidth(text) > 30 {
		t.Fatalf("footer=%q", text)
	}
}
