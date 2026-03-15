package logs

import (
	"encoding/json"
	"slices"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/table"
	tea "charm.land/bubbletea/v2"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/pubsub"
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
	viewDirty  bool
	cachedView string
}

type selectedLogMsg logging.LogMessage

// LogsPageActivatedMsg is sent when the logs page becomes active,
// triggering a full refresh to catch any dropped pubsub events.
type LogsPageActivatedMsg struct{}

func (i *tableCmp) Init() tea.Cmd {
	i.setRows()
	i.updateStyles()
	i.viewDirty = true
	return nil
}

func (i *tableCmp) updateStyles() {
	t := theme.CurrentTheme()
	defaultStyles := table.DefaultStyles()
	defaultStyles.Selected = defaultStyles.Selected.Foreground(t.Primary())
	i.table.SetStyles(defaultStyles)
}

func (i *tableCmp) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	switch msg := msg.(type) {
	case dialog.ThemeChangedMsg:
		i.updateStyles()
		i.viewDirty = true
	case LogsPageActivatedMsg:
		i.setRows()
		i.viewDirty = true
	case pubsub.Event[logging.LogMessage]:
		i.appendRow(msg.Payload)
		i.viewDirty = true
		return i, nil
	}
	prevSelectedRow := i.table.SelectedRow()
	t, cmd := i.table.Update(msg)
	cmds = append(cmds, cmd)
	i.table = t
	selectedRow := i.table.SelectedRow()
	if selectedRow != nil {
		if prevSelectedRow == nil || selectedRow[0] == prevSelectedRow[0] {
			var log logging.LogMessage
			for _, row := range logging.List() {
				if row.ID == selectedRow[0] {
					log = row
					break
				}
			}
			if log.ID != "" {
				cmds = append(cmds, util.CmdHandler(selectedLogMsg(log)))
			}
		}
	}
	i.viewDirty = true
	return i, tea.Batch(cmds...)
}

func (i *tableCmp) View() tea.View {
	if i.viewDirty {
		t := theme.CurrentTheme()
		i.cachedView = styles.ForceReplaceBackgroundWithLipgloss(i.table.View(), t.Background())
		i.viewDirty = false
	}
	return tea.NewView(i.cachedView)
}

func (i *tableCmp) GetSize() (int, int) {
	return i.table.Width(), i.table.Height()
}

func (i *tableCmp) SetSize(width int, height int) tea.Cmd {
	i.table.SetWidth(width)
	i.table.SetHeight(height)
	columns := i.table.Columns()
	for i, col := range columns {
		col.Width = (width / len(columns)) - 2
		columns[i] = col
	}
	i.table.SetColumns(columns)
	i.viewDirty = true
	return nil
}

func (i *tableCmp) BindingKeys() []key.Binding {
	return layout.KeyMapToSlice(i.table.KeyMap)
}

func logToRow(log logging.LogMessage) table.Row {
	bm, _ := json.Marshal(log.Attributes)
	return table.Row{
		log.ID,
		log.Time.Format("15:04:05"),
		log.Level,
		log.Message,
		string(bm),
	}
}

func (i *tableCmp) appendRow(log logging.LogMessage) {
	newRow := logToRow(log)
	rows := i.table.Rows()
	pos, _ := slices.BinarySearchFunc(rows, log, func(row table.Row, target logging.LogMessage) int {
		// Rows are sorted newest-first (descending). Parse the row time string
		// for comparison, but also compare by ID for same-second entries.
		rowTime, _ := time.Parse("15:04:05", row[1])
		if rowTime.After(target.Time.Truncate(time.Second)) {
			return -1
		}
		if rowTime.Before(target.Time.Truncate(time.Second)) {
			return 1
		}
		return 0
	})
	rows = slices.Insert(rows, pos, newRow)
	i.table.SetRows(rows)
}

func (i *tableCmp) setRows() {
	logs := logging.List()
	slices.SortFunc(logs, func(a, b logging.LogMessage) int {
		if a.Time.Before(b.Time) {
			return 1
		}
		if a.Time.After(b.Time) {
			return -1
		}
		return 0
	})

	rows := make([]table.Row, 0, len(logs))
	for _, log := range logs {
		rows = append(rows, logToRow(log))
	}
	i.table.SetRows(rows)
}

func NewLogsTable() TableComponent {
	columns := []table.Column{
		{Title: "ID", Width: 4},
		{Title: "Time", Width: 4},
		{Title: "Level", Width: 10},
		{Title: "Message", Width: 10},
		{Title: "Attributes", Width: 10},
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
		table: tableModel,
	}
}
