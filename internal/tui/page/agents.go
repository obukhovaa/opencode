package page

import (
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/tui/components/agents"
	"github.com/opencode-ai/opencode/internal/tui/layout"
	"github.com/opencode-ai/opencode/internal/tui/styles"
)

var AgentsPage PageID = "agents"

type agentsPage struct {
	width, height int
	table         layout.Container
	details       layout.Container
}

func (p *agentsPage) Init() tea.Cmd {
	return tea.Batch(
		p.table.Init(),
		p.details.Init(),
	)
}

func (p *agentsPage) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		p.width = msg.Width
		p.height = msg.Height
		return p, p.SetSize(msg.Width, msg.Height)
	}

	tbl, cmd := p.table.Update(msg)
	cmds = append(cmds, cmd)
	p.table = tbl.(layout.Container)
	det, cmd := p.details.Update(msg)
	cmds = append(cmds, cmd)
	p.details = det.(layout.Container)

	return p, tea.Batch(cmds...)
}

func (p *agentsPage) View() tea.View {
	style := styles.BaseStyle().Width(p.width).Height(p.height)
	return tea.NewView(style.Render(lipgloss.JoinVertical(lipgloss.Top,
		p.table.View().Content,
		p.details.View().Content,
	)))
}

func (p *agentsPage) BindingKeys() []key.Binding {
	return p.table.BindingKeys()
}

func (p *agentsPage) GetSize() (int, int) {
	return p.width, p.height
}

func (p *agentsPage) SetSize(width int, height int) tea.Cmd {
	p.width = width
	p.height = height
	return tea.Batch(
		p.table.SetSize(width, height/2),
		p.details.SetSize(width, height/2),
	)
}

func NewAgentsPage(registry agentregistry.Registry) tea.Model {
	return &agentsPage{
		table:   layout.NewContainer(agents.NewAgentsTable(registry), layout.WithBorderAll()),
		details: layout.NewContainer(agents.NewAgentsDetails(), layout.WithBorderAll()),
	}
}
