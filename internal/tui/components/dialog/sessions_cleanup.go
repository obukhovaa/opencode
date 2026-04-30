package dialog

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/opencode-ai/opencode/internal/tui/layout"
	"github.com/opencode-ai/opencode/internal/tui/styles"
	"github.com/opencode-ai/opencode/internal/tui/theme"
	"github.com/opencode-ai/opencode/internal/tui/util"
)

type ConfirmSessionsCleanupMsg struct{}
type CloseSessionsCleanupMsg struct{}

type SessionsCleanupDialog interface {
	tea.Model
	layout.Bindings
	SetCount(count int)
	SetMaxAge(maxAge string)
}

type sessionsCleanupDialogCmp struct {
	selectedNo bool
	count      int
	maxAge     string
}

func (d *sessionsCleanupDialogCmp) SetCount(count int) {
	d.count = count
}

func (d *sessionsCleanupDialogCmp) SetMaxAge(maxAge string) {
	d.maxAge = maxAge
}

func (d *sessionsCleanupDialogCmp) Init() tea.Cmd {
	return nil
}

func (d *sessionsCleanupDialogCmp) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, helpKeys.LeftRight) || key.Matches(msg, helpKeys.Tab):
			d.selectedNo = !d.selectedNo
			return d, nil
		case key.Matches(msg, helpKeys.EnterSpace):
			if !d.selectedNo {
				return d, util.CmdHandler(ConfirmSessionsCleanupMsg{})
			}
			return d, util.CmdHandler(CloseSessionsCleanupMsg{})
		case key.Matches(msg, helpKeys.Yes):
			return d, util.CmdHandler(ConfirmSessionsCleanupMsg{})
		case key.Matches(msg, helpKeys.No):
			return d, util.CmdHandler(CloseSessionsCleanupMsg{})
		case key.Matches(msg, sessionKeys.Escape):
			return d, util.CmdHandler(CloseSessionsCleanupMsg{})
		}
	}
	return d, nil
}

func (d *sessionsCleanupDialogCmp) View() tea.View {
	t := theme.CurrentTheme()
	baseStyle := styles.BaseStyle()

	msg := fmt.Sprintf("Delete %d session(s) not updated in the last %s?", d.count, d.maxAge)

	yesStyle := baseStyle
	noStyle := baseStyle
	spacerStyle := baseStyle.Background(t.Background())

	if d.selectedNo {
		noStyle = noStyle.Background(t.Primary()).Foreground(t.Background())
		yesStyle = yesStyle.Background(t.Background()).Foreground(t.Primary())
	} else {
		yesStyle = yesStyle.Background(t.Primary()).Foreground(t.Background())
		noStyle = noStyle.Background(t.Background()).Foreground(t.Primary())
	}

	yesButton := yesStyle.Padding(0, 1).Render("Yes")
	noButton := noStyle.Padding(0, 1).Render("No")

	buttons := lipgloss.JoinHorizontal(lipgloss.Left, yesButton, spacerStyle.Render("  "), noButton)

	width := lipgloss.Width(msg)
	remainingWidth := width - lipgloss.Width(buttons)
	if remainingWidth > 0 {
		buttons = spacerStyle.Render(strings.Repeat(" ", remainingWidth)) + buttons
	}

	content := baseStyle.Render(
		lipgloss.JoinVertical(
			lipgloss.Center,
			msg,
			"",
			buttons,
		),
	)

	return tea.NewView(baseStyle.Padding(1, 2).
		Border(lipgloss.RoundedBorder()).
		BorderBackground(t.Background()).
		BorderForeground(t.TextMuted()).
		Width(lipgloss.Width(content) + 6).
		Render(content))
}

func (d *sessionsCleanupDialogCmp) BindingKeys() []key.Binding {
	return layout.KeyMapToSlice(helpKeys)
}

func NewSessionsCleanupDialogCmp() SessionsCleanupDialog {
	return &sessionsCleanupDialogCmp{
		selectedNo: true,
	}
}
