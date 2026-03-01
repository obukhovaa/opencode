package agents

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/tui/layout"
	"github.com/opencode-ai/opencode/internal/tui/styles"
	"github.com/opencode-ai/opencode/internal/tui/theme"
)

type DetailComponent interface {
	tea.Model
	layout.Sizeable
	layout.Bindings
}

type detailCmp struct {
	width, height int
	current       agentregistry.AgentInfo
	viewport      viewport.Model
}

func (d *detailCmp) Init() tea.Cmd {
	return nil
}

func (d *detailCmp) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case selectedAgentMsg:
		d.current = agentregistry.AgentInfo(msg)
		d.updateContent()
	}

	var cmd tea.Cmd
	d.viewport, cmd = d.viewport.Update(msg)
	return d, cmd
}

func (d *detailCmp) updateContent() {
	var content strings.Builder
	t := theme.CurrentTheme()

	labelStyle := lipgloss.NewStyle().Foreground(t.Primary()).Bold(true)
	valueStyle := lipgloss.NewStyle().Foreground(t.Text())
	mutedStyle := lipgloss.NewStyle().Foreground(t.TextMuted())

	availableWidth := d.width - 4
	if availableWidth < 1 {
		availableWidth = 1
	}

	header := lipgloss.NewStyle().Bold(true).Foreground(t.TextEmphasized()).
		Render(fmt.Sprintf("%s (%s)", d.current.Name, d.current.ID))
	content.WriteString(header)
	content.WriteString("\n")

	content.WriteString(labelStyle.Render("Type:"))
	content.WriteString(" ")
	content.WriteString(valueStyle.Render(string(d.current.Mode)))
	content.WriteString("\n")

	if d.current.Description != "" {
		content.WriteString(labelStyle.Render("Description:"))
		content.WriteString("\n")
		wrapped := lipgloss.NewStyle().Width(availableWidth).Padding(0, 2).
			Render(valueStyle.Render(d.current.Description))
		content.WriteString(wrapped)
		content.WriteString("\n")
	}

	content.WriteString(labelStyle.Render("Model:"))
	content.WriteString(" ")
	model := d.current.Model
	if model == "" {
		model = "default"
	}
	content.WriteString(valueStyle.Render(model))
	content.WriteString("\n")

	if len(d.current.Tools) > 0 {
		content.WriteString(labelStyle.Render("Tools:"))
		content.WriteString("\n")
		names := make([]string, 0, len(d.current.Tools))
		for name := range d.current.Tools {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			enabled := d.current.Tools[name]
			status := mutedStyle.Render("disabled")
			if enabled {
				status = lipgloss.NewStyle().Foreground(t.Success()).Render("enabled")
			}
			line := fmt.Sprintf("  %s: %s", valueStyle.Render(name), status)
			content.WriteString(line)
			content.WriteString("\n")
		}
		content.WriteString("\n")
	}

	if len(d.current.Permission) > 0 {
		content.WriteString(labelStyle.Render("Permissions:"))
		content.WriteString("\n")
		for key, val := range d.current.Permission {
			line := fmt.Sprintf("  %s: %v", valueStyle.Render(key), val)
			content.WriteString(line)
			content.WriteString("\n")
		}
		content.WriteString("\n")
	}

	if d.current.Location != "" {
		content.WriteString(labelStyle.Render("Location:"))
		content.WriteString(" ")
		content.WriteString(mutedStyle.Render(d.current.Location))
		content.WriteString("\n")
	}

	d.viewport.SetContent(content.String())
}

func (d *detailCmp) View() string {
	t := theme.CurrentTheme()
	return styles.ForceReplaceBackgroundWithLipgloss(d.viewport.View(), t.Background())
}

func (d *detailCmp) GetSize() (int, int) {
	return d.width, d.height
}

func (d *detailCmp) SetSize(width int, height int) tea.Cmd {
	d.width = width
	d.height = height
	d.viewport.Width = width
	d.viewport.Height = height
	d.updateContent()
	return nil
}

func (d *detailCmp) BindingKeys() []key.Binding {
	return layout.KeyMapToSlice(d.viewport.KeyMap)
}

func NewAgentsDetails() DetailComponent {
	return &detailCmp{
		viewport: viewport.New(0, 0),
	}
}
