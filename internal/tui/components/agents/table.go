package agents

import (
	"fmt"
	"sort"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/table"
	tea "charm.land/bubbletea/v2"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/tui/components/dialog"
	"github.com/opencode-ai/opencode/internal/tui/layout"
	"github.com/opencode-ai/opencode/internal/tui/styles"
	"github.com/opencode-ai/opencode/internal/tui/theme"
	"github.com/opencode-ai/opencode/internal/tui/util"
)

type TableComponent interface {
	tea.Model
	layout.Sizeable
	layout.Bindings
}

type tableCmp struct {
	table      table.Model
	agents     []agentregistry.AgentInfo
	registry   agentregistry.Registry
	viewDirty  bool
	cachedView string
}

type selectedAgentMsg agentregistry.AgentInfo

func (t *tableCmp) Init() tea.Cmd {
	t.loadAgents()
	t.setRows()
	t.updateStyles()
	t.viewDirty = true
	if len(t.agents) > 0 {
		return util.CmdHandler(selectedAgentMsg(t.agents[0]))
	}
	return nil
}

func (t *tableCmp) loadAgents() {
	primary := t.registry.ListByMode(config.AgentModeAgent)
	sub := t.registry.ListByMode(config.AgentModeSubagent)
	t.agents = make([]agentregistry.AgentInfo, 0, len(primary)+len(sub))
	t.agents = append(t.agents, primary...)
	t.agents = append(t.agents, sub...)
}

func (t *tableCmp) findAgent(id string) (agentregistry.AgentInfo, bool) {
	for _, a := range t.agents {
		if a.ID == id {
			return a, true
		}
	}
	return agentregistry.AgentInfo{}, false
}

func (t *tableCmp) updateStyles() {
	th := theme.CurrentTheme()
	defaultStyles := table.DefaultStyles()
	defaultStyles.Selected = defaultStyles.Selected.Foreground(th.Primary())
	t.table.SetStyles(defaultStyles)
}

func (t *tableCmp) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	switch msg.(type) {
	case dialog.ThemeChangedMsg:
		t.updateStyles()
		t.viewDirty = true
	}
	prevSelectedRow := t.table.SelectedRow()
	tbl, cmd := t.table.Update(msg)
	cmds = append(cmds, cmd)
	t.table = tbl

	selectedRow := t.table.SelectedRow()
	if selectedRow != nil {
		if prevSelectedRow == nil || selectedRow[0] != prevSelectedRow[0] {
			if a, ok := t.findAgent(selectedRow[0]); ok {
				cmds = append(cmds, util.CmdHandler(selectedAgentMsg(a)))
			}
		}
	}
	t.viewDirty = true
	return t, tea.Batch(cmds...)
}

func (t *tableCmp) View() tea.View {
	if t.viewDirty {
		th := theme.CurrentTheme()
		t.cachedView = styles.ForceReplaceBackgroundWithLipgloss(t.table.View(), th.Background())
		t.viewDirty = false
	}
	return tea.NewView(t.cachedView)
}

func (t *tableCmp) GetSize() (int, int) {
	return t.table.Width(), t.table.Height()
}

func (t *tableCmp) SetSize(width int, height int) tea.Cmd {
	t.table.SetWidth(width)
	t.table.SetHeight(height)
	columns := t.table.Columns()
	if len(columns) > 0 {
		colWidths := []int{
			width * 15 / 100,
			width * 10 / 100,
			width * 20 / 100,
			width * 20 / 100,
			width * 35 / 100,
		}
		for i, col := range columns {
			if i < len(colWidths) {
				col.Width = colWidths[i] - 2
			}
			columns[i] = col
		}
		t.table.SetColumns(columns)
	}
	t.viewDirty = true
	return nil
}

func (t *tableCmp) BindingKeys() []key.Binding {
	return layout.KeyMapToSlice(t.table.KeyMap)
}

func (t *tableCmp) setRows() {
	rows := make([]table.Row, 0, len(t.agents))
	for _, a := range t.agents {
		model := a.Model
		if model == "" {
			model = "default"
		}
		rows = append(rows, table.Row{
			a.ID,
			string(a.Mode),
			a.Name,
			model,
			formatTools(a.Tools),
		})
	}
	t.table.SetRows(rows)
}

func formatTools(tools map[string]bool) string {
	if len(tools) == 0 {
		return "default"
	}
	disabled := make([]string, 0)
	for name, enabled := range tools {
		if !enabled {
			disabled = append(disabled, name)
		}
	}
	sort.Strings(disabled)
	if len(disabled) == 0 {
		return "all enabled"
	}
	if len(disabled) == 1 && disabled[0] == "*" {
		return "none"
	}
	return fmt.Sprintf("disabled: %s", strings.Join(disabled, ", "))
}

func NewAgentsTable(registry agentregistry.Registry) TableComponent {
	columns := []table.Column{
		{Title: "ID", Width: 10},
		{Title: "Type", Width: 8},
		{Title: "Name", Width: 15},
		{Title: "Model", Width: 15},
		{Title: "Tools", Width: 20},
	}

	tableModel := table.New(
		table.WithColumns(columns),
	)
	tableModel.KeyMap.PageUp.SetEnabled(false)
	tableModel.KeyMap.PageDown.SetEnabled(false)
	tableModel.KeyMap.HalfPageUp.SetEnabled(false)
	tableModel.KeyMap.HalfPageDown.SetEnabled(false)
	tableModel.Focus()
	return &tableCmp{
		table:    tableModel,
		registry: registry,
	}
}
