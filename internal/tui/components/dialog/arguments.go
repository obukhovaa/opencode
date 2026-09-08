package dialog

import (
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"fmt"

	"github.com/opencode-ai/opencode/internal/tui/styles"
	"github.com/opencode-ai/opencode/internal/tui/theme"
	"github.com/opencode-ai/opencode/internal/tui/util"
)

type argumentsDialogKeyMap struct {
	Enter  key.Binding
	Escape key.Binding
}

// ShortHelp implements key.Map.
func (k argumentsDialogKeyMap) ShortHelp() []key.Binding {
	return []key.Binding{
		key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("enter", "confirm"),
		),
		key.NewBinding(
			key.WithKeys("esc"),
			key.WithHelp("esc", "cancel"),
		),
	}
}

// FullHelp implements key.Map.
func (k argumentsDialogKeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{k.ShortHelp()}
}

// ArgsMode says how the values collected by the argument dialog become the
// argument string of a staged invocation.
type ArgsMode int

const (
	// ArgsModePositional quotes each value and joins them with spaces so the
	// expander recovers them as individual positional arguments. Used for
	// $0/$ARGUMENTS[N] placeholders and for named $FOO placeholders, which bind
	// positionally by declaration order.
	ArgsModePositional ArgsMode = iota
	// ArgsModeWhole writes the single collected value verbatim: the content
	// declares only $ARGUMENTS, which binds the whole unsplit argument string,
	// so quoting it would put the quotes into the prompt.
	ArgsModeWhole
)

// ShowMultiArgumentsDialogMsg is a message that is sent to show the multi-arguments dialog.
type ShowMultiArgumentsDialogMsg struct {
	CommandID string
	Content   string
	ArgNames  []string
	ArgHints  map[string]string // Optional hints for argument placeholders
	Mode      ArgsMode
	// InitialValues pre-fills the fields, parallel to ArgNames. It carries the
	// arguments an action command was invoked with inline (`/loop 5m check the
	// build`) so the user reviews what they typed instead of retyping it into
	// blank inputs. Shorter than ArgNames, or nil, leaves the rest empty.
	InitialValues []string
}

// CloseMultiArgumentsDialogMsg is a message that is sent when the multi-arguments dialog is closed.
type CloseMultiArgumentsDialogMsg struct {
	Submit    bool
	CommandID string
	Content   string
	Args      map[string]string
	// ArgNames and Values are the collected fields in dialog order. Args is the
	// same data keyed by name, kept for the action commands (/loop, /rename)
	// that read their arguments by name; staging needs the order.
	ArgNames []string
	Values   []string
	Mode     ArgsMode
}

// StageInvocationMsg asks the editor to insert Text at the cursor. It is how a
// resolved slash command reaches the user's message: staged as editable text,
// not sent.
type StageInvocationMsg struct {
	Text string
}

// MultiArgumentsDialogCmp is a component that asks the user for multiple command arguments.
type MultiArgumentsDialogCmp struct {
	width, height int
	inputs        []textinput.Model
	focusIndex    int
	keys          argumentsDialogKeyMap
	commandID     string
	content       string
	argNames      []string
	mode          ArgsMode
}

// NewMultiArgumentsDialogCmp creates a new MultiArgumentsDialogCmp. It takes
// the whole request rather than a parameter list so a new field on
// ShowMultiArgumentsDialogMsg does not ripple through every call site.
func NewMultiArgumentsDialogCmp(msg ShowMultiArgumentsDialogMsg) MultiArgumentsDialogCmp {
	t := theme.CurrentTheme()
	argNames := msg.ArgNames
	inputs := make([]textinput.Model, len(argNames))

	for i, name := range argNames {
		ti := textinput.New()
		if hint, ok := msg.ArgHints[name]; ok && hint != "" {
			ti.Placeholder = hint
		} else {
			ti.Placeholder = fmt.Sprintf("Enter value for %s...", name)
		}
		if i < len(msg.InitialValues) && msg.InitialValues[i] != "" {
			ti.SetValue(msg.InitialValues[i])
		}
		ti.SetWidth(40)
		ti.Prompt = ""

		s := ti.Styles()
		s.Focused.Placeholder = s.Focused.Placeholder.Background(t.Background())
		s.Blurred.Placeholder = s.Blurred.Placeholder.Background(t.Background())
		s.Focused.Prompt = s.Focused.Prompt.Background(t.Background())
		s.Blurred.Prompt = s.Blurred.Prompt.Background(t.Background())
		s.Focused.Text = s.Focused.Text.Background(t.Background())
		s.Blurred.Text = s.Blurred.Text.Background(t.Background())

		// Only focus the first input initially
		if i == 0 {
			ti.Focus()
			s.Focused.Prompt = s.Focused.Prompt.Foreground(t.Primary())
			s.Focused.Text = s.Focused.Text.Foreground(t.Primary())
		} else {
			ti.Blur()
		}
		ti.SetStyles(s)

		inputs[i] = ti
	}

	return MultiArgumentsDialogCmp{
		inputs:     inputs,
		keys:       argumentsDialogKeyMap{},
		commandID:  msg.CommandID,
		content:    msg.Content,
		argNames:   argNames,
		mode:       msg.Mode,
		focusIndex: 0,
	}
}

// Init implements tea.Model.
func (m MultiArgumentsDialogCmp) Init() tea.Cmd {
	// Make sure only the first input is focused
	for i := range m.inputs {
		if i == 0 {
			m.inputs[i].Focus()
		} else {
			m.inputs[i].Blur()
		}
	}

	return textinput.Blink
}

// Update implements tea.Model.
func (m MultiArgumentsDialogCmp) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	t := theme.CurrentTheme()

	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, key.NewBinding(key.WithKeys("esc"))):
			return m, util.CmdHandler(CloseMultiArgumentsDialogMsg{
				Submit:    false,
				CommandID: m.commandID,
				Content:   m.content,
				Args:      nil,
			})
		case key.Matches(msg, key.NewBinding(key.WithKeys("enter"))):
			// If we're on the last input, submit the form
			if m.focusIndex == len(m.inputs)-1 {
				args := make(map[string]string, len(m.argNames))
				values := make([]string, len(m.argNames))
				for i, name := range m.argNames {
					args[name] = m.inputs[i].Value()
					values[i] = m.inputs[i].Value()
				}
				return m, util.CmdHandler(CloseMultiArgumentsDialogMsg{
					Submit:    true,
					CommandID: m.commandID,
					Content:   m.content,
					Args:      args,
					ArgNames:  m.argNames,
					Values:    values,
					Mode:      m.mode,
				})
			}
			// Otherwise, move to the next input
			m.inputs[m.focusIndex].Blur()
			m.focusIndex++
			m.inputs[m.focusIndex].Focus()
			s := m.inputs[m.focusIndex].Styles()
			s.Focused.Prompt = s.Focused.Prompt.Foreground(t.Primary())
			s.Focused.Text = s.Focused.Text.Foreground(t.Primary())
			m.inputs[m.focusIndex].SetStyles(s)
		case key.Matches(msg, key.NewBinding(key.WithKeys("tab"))):
			// Move to the next input
			m.inputs[m.focusIndex].Blur()
			m.focusIndex = (m.focusIndex + 1) % len(m.inputs)
			m.inputs[m.focusIndex].Focus()
			s := m.inputs[m.focusIndex].Styles()
			s.Focused.Prompt = s.Focused.Prompt.Foreground(t.Primary())
			s.Focused.Text = s.Focused.Text.Foreground(t.Primary())
			m.inputs[m.focusIndex].SetStyles(s)
		case key.Matches(msg, key.NewBinding(key.WithKeys("shift+tab"))):
			// Move to the previous input
			m.inputs[m.focusIndex].Blur()
			m.focusIndex = (m.focusIndex - 1 + len(m.inputs)) % len(m.inputs)
			m.inputs[m.focusIndex].Focus()
			s := m.inputs[m.focusIndex].Styles()
			s.Focused.Prompt = s.Focused.Prompt.Foreground(t.Primary())
			s.Focused.Text = s.Focused.Text.Foreground(t.Primary())
			m.inputs[m.focusIndex].SetStyles(s)
		}
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	}

	// Update the focused input
	var cmd tea.Cmd
	m.inputs[m.focusIndex], cmd = m.inputs[m.focusIndex].Update(msg)
	cmds = append(cmds, cmd)

	return m, tea.Batch(cmds...)
}

// View implements tea.Model.
func (m MultiArgumentsDialogCmp) View() tea.View {
	t := theme.CurrentTheme()
	baseStyle := styles.BaseStyle()

	// Calculate width needed for content
	maxWidth := 60 // Width for explanation text

	title := lipgloss.NewStyle().
		Foreground(t.Primary()).
		Bold(true).
		Width(maxWidth).
		Padding(0, 1).
		Background(t.Background()).
		Render("Command Arguments")

	explanation := lipgloss.NewStyle().
		Foreground(t.Text()).
		Width(maxWidth).
		Padding(0, 1).
		Background(t.Background()).
		Render("This command requires multiple arguments. Please enter values for each:")

	// Create input fields for each argument
	inputFields := make([]string, len(m.inputs))
	for i, input := range m.inputs {
		// Highlight the label of the focused input
		labelStyle := lipgloss.NewStyle().
			Width(maxWidth).
			Padding(1, 1, 0, 1).
			Background(t.Background())

		if i == m.focusIndex {
			labelStyle = labelStyle.Foreground(t.Primary()).Bold(true)
		} else {
			labelStyle = labelStyle.Foreground(t.TextMuted())
		}

		label := labelStyle.Render(m.argNames[i] + ":")

		field := lipgloss.NewStyle().
			Foreground(t.Text()).
			Width(maxWidth).
			Padding(0, 1).
			Background(t.Background()).
			Render(input.View())

		inputFields[i] = lipgloss.JoinVertical(lipgloss.Left, label, field)
	}

	maxWidth = min(maxWidth, m.width-10)

	// Join all elements vertically
	elements := []string{title, explanation}
	elements = append(elements, inputFields...)

	content := lipgloss.JoinVertical(
		lipgloss.Left,
		elements...,
	)

	return tea.NewView(baseStyle.Padding(1, 2).
		Border(lipgloss.RoundedBorder()).
		BorderBackground(t.Background()).
		BorderForeground(t.TextMuted()).
		Background(t.Background()).
		Width(lipgloss.Width(content) + 6).
		Render(content))
}

// SetSize sets the size of the component.
func (m *MultiArgumentsDialogCmp) SetSize(width, height int) {
	m.width = width
	m.height = height
}

// Bindings implements layout.Bindings.
func (m MultiArgumentsDialogCmp) Bindings() []key.Binding {
	return m.keys.ShortHelp()
}
