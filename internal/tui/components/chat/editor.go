package chat

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"unicode"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/llm/tools/shell"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/session"
	"github.com/opencode-ai/opencode/internal/tui/components/dialog"
	"github.com/opencode-ai/opencode/internal/tui/layout"
	"github.com/opencode-ai/opencode/internal/tui/styles"
	"github.com/opencode-ai/opencode/internal/tui/theme"
	"github.com/opencode-ai/opencode/internal/tui/util"
	"github.com/opencode-ai/opencode/internal/tui/vim"
)

type editorMode string

const (
	modeNormal editorMode = "normal"
	modeShell  editorMode = "shell"
)

type editorCmp struct {
	width           int
	height          int
	app             *app.App
	session         session.Session
	textarea        textarea.Model
	attachments     []message.Attachment
	deleteMode      bool
	mode            editorMode
	shellHistory    []string
	shellHistoryIdx int
	shellExecuting  bool
	vimHandler      *vim.Handler // nil when vim mode is disabled
}

type EditorKeyMaps struct {
	Send       key.Binding
	OpenEditor key.Binding
}

type DeleteAttachmentKeyMaps struct {
	AttachmentDeleteMode key.Binding
	Escape               key.Binding
	DeleteAllAttachments key.Binding
}

var editorMaps = EditorKeyMaps{
	Send: key.NewBinding(
		key.WithKeys("enter", "ctrl+s"),
		key.WithHelp("enter", "send message"),
	),
	OpenEditor: key.NewBinding(
		key.WithKeys("ctrl+e"),
		key.WithHelp("ctrl+e", "open editor"),
	),
}

var DeleteKeyMaps = DeleteAttachmentKeyMaps{
	AttachmentDeleteMode: key.NewBinding(
		key.WithKeys("ctrl+r"),
		key.WithHelp("ctrl+r+{i}", "delete attachment at index i"),
	),
	Escape: key.NewBinding(
		key.WithKeys("esc"),
		key.WithHelp("esc", "cancel delete mode"),
	),
	DeleteAllAttachments: key.NewBinding(
		key.WithKeys("r"),
		key.WithHelp("ctrl+r+r", "delete all attchments"),
	),
}

const (
	maxAttachments = 5
)

func (m *editorCmp) openEditor() tea.Cmd {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "nvim"
	}

	tmpfile, err := os.CreateTemp("", "msg_*.md")
	if err != nil {
		return util.ReportError(err)
	}
	tmpfile.Close()
	c := exec.Command(editor, tmpfile.Name()) //nolint:gosec
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return tea.ExecProcess(c, func(err error) tea.Msg {
		if err != nil {
			return util.ReportError(err)
		}
		content, err := os.ReadFile(tmpfile.Name())
		if err != nil {
			return util.ReportError(err)
		}
		if len(content) == 0 {
			return util.ReportWarn("Message is empty")
		}
		os.Remove(tmpfile.Name())
		attachments := m.attachments
		m.attachments = nil
		return SendMsg{
			Text:        string(content),
			Attachments: attachments,
		}
	})
}

func (m *editorCmp) Init() tea.Cmd {
	cmds := []tea.Cmd{textarea.Blink}
	if m.vimHandler != nil {
		cmds = append(cmds, util.CmdHandler(VimModeChangedMsg{
			Mode: string(m.vimHandler.Mode()),
		}))
	}
	return tea.Batch(cmds...)
}

func (m *editorCmp) send() tea.Cmd {
	if m.app.ActiveAgent().IsSessionBusy(m.session.ID) {
		return util.ReportWarn("Agent is working, please wait...")
	}

	value := m.textarea.Value()
	m.textarea.Reset()
	attachments := m.attachments

	m.attachments = nil
	if value == "" {
		return nil
	}
	return tea.Batch(
		util.CmdHandler(SendMsg{
			Text:        value,
			Attachments: attachments,
		}),
	)
}

func (m *editorCmp) enterShellMode() {
	m.mode = modeShell
	m.shellHistoryIdx = len(m.shellHistory)
	m.textarea.Placeholder = "Enter shell command..."
}

func (m *editorCmp) exitShellMode() {
	m.mode = modeNormal
	m.textarea.Placeholder = ""
	m.textarea.Reset()
}

func (m *editorCmp) executeShell() tea.Cmd {
	command := strings.TrimSpace(m.textarea.Value())
	if command == "" {
		return nil
	}

	m.shellHistory = append(m.shellHistory, command)
	m.shellHistoryIdx = len(m.shellHistory)
	m.textarea.Reset()
	m.shellExecuting = true

	workdir := config.WorkingDirectory()

	return func() tea.Msg {
		sh := shell.GetPersistentShell(workdir)
		if sh == nil {
			return ShellResultMsg{
				Command:  command,
				ExitCode: 1,
				Err:      fmt.Errorf("failed to create shell instance"),
			}
		}

		ctx := context.Background()
		stdout, stderr, exitCode, _, err := sh.Exec(ctx, command, tools.DefaultTimeout)

		return ShellResultMsg{
			Command:  command,
			Stdout:   stdout,
			Stderr:   stderr,
			ExitCode: exitCode,
			Err:      err,
		}
	}
}

func (m *editorCmp) shellHistoryUp() {
	if len(m.shellHistory) == 0 {
		return
	}
	if m.shellHistoryIdx > 0 {
		m.shellHistoryIdx--
		m.textarea.SetValue(m.shellHistory[m.shellHistoryIdx])
	}
}

func (m *editorCmp) shellHistoryDown() {
	if len(m.shellHistory) == 0 {
		return
	}
	if m.shellHistoryIdx < len(m.shellHistory)-1 {
		m.shellHistoryIdx++
		m.textarea.SetValue(m.shellHistory[m.shellHistoryIdx])
	} else {
		m.shellHistoryIdx = len(m.shellHistory)
		m.textarea.Reset()
	}
}

func (m *editorCmp) IsShellMode() bool {
	return m.mode == modeShell
}

func (m *editorCmp) ConsumesCtrlC() bool {
	if m.vimHandler != nil {
		return m.vimHandler.ConsumesCtrlC()
	}
	return false
}

func (m *editorCmp) VimMode() string {
	if m.vimHandler != nil {
		return string(m.vimHandler.Mode())
	}
	return ""
}

func (m *editorCmp) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch msg := msg.(type) {
	case ToggleVimModeMsg:
		if m.vimHandler != nil {
			// Disable vim mode
			m.vimHandler = nil
			return m, util.CmdHandler(VimModeChangedMsg{Mode: ""})
		}
		// Enable vim mode — starts in INSERT
		m.vimHandler = vim.NewHandler()
		return m, util.CmdHandler(VimModeChangedMsg{Mode: string(m.vimHandler.Mode())})
	case dialog.ThemeChangedMsg:
		m.textarea = CreateTextArea(&m.textarea)
	case dialog.CompletionSelectedMsg:
		existingValue := m.textarea.Value()
		modifiedValue := strings.Replace(existingValue, msg.SearchString, msg.CompletionValue, 1)
		m.textarea.SetValue(modifiedValue)
		return m, nil
	case dialog.CompletionRemoveTextMsg:
		existingValue := m.textarea.Value()
		modifiedValue := strings.Replace(existingValue, msg.SearchString, "", 1)
		m.textarea.SetValue(modifiedValue)
		return m, nil
	case SessionClearedMsg:
		m.session = session.Session{}
		if m.mode == modeShell {
			m.exitShellMode()
			return m, util.CmdHandler(ShellModeChangedMsg{ShellMode: false})
		}
		return m, nil
	case SessionSelectedMsg:
		if msg.ID != m.session.ID {
			m.session = msg
			if m.mode == modeShell {
				m.exitShellMode()
				return m, util.CmdHandler(ShellModeChangedMsg{ShellMode: false})
			}
		}
		return m, nil
	case ShellResultMsg:
		m.shellExecuting = false
		return m, nil
	case dialog.AttachmentAddedMsg:
		if len(m.attachments) >= maxAttachments {
			logging.ErrorPersist(fmt.Sprintf("cannot add more than %d images", maxAttachments))
			return m, cmd
		}
		m.attachments = append(m.attachments, msg.Attachment)
	case tea.KeyPressMsg:
		if m.shellExecuting {
			return m, nil
		}

		if key.Matches(msg, DeleteKeyMaps.AttachmentDeleteMode) {
			m.deleteMode = true
			return m, nil
		}
		if key.Matches(msg, DeleteKeyMaps.DeleteAllAttachments) && m.deleteMode {
			m.deleteMode = false
			m.attachments = nil
			return m, nil
		}
		if m.deleteMode && len(msg.Text) > 0 && unicode.IsDigit(rune(msg.Text[0])) {
			num := int(rune(msg.Text[0]) - '0')
			m.deleteMode = false
			if num < 10 && len(m.attachments) > num {
				if num == 0 {
					m.attachments = m.attachments[num+1:]
				} else {
					m.attachments = slices.Delete(m.attachments, num, num+1)
				}
				return m, nil
			}
		}
		if key.Matches(msg, messageKeys.PageUp) || key.Matches(msg, messageKeys.PageDown) ||
			key.Matches(msg, messageKeys.HalfPageUp) || key.Matches(msg, messageKeys.HalfPageDown) {
			return m, nil
		}

		// Shell mode: detect "!" at position 0 on empty input
		// In vim mode, only trigger shell from INSERT mode
		if m.mode == modeNormal && msg.Text == "!" && m.textarea.Value() == "" {
			if m.vimHandler == nil || m.vimHandler.Mode() == vim.ModeInsert {
				m.enterShellMode()
				return m, util.CmdHandler(ShellModeChangedMsg{ShellMode: true})
			}
		}

		// Shell mode key handling
		if m.mode == modeShell {
			// Escape or Ctrl+C exits shell mode
			if key.Matches(msg, DeleteKeyMaps.Escape) || msg.String() == "ctrl+c" {
				m.exitShellMode()
				return m, util.CmdHandler(ShellModeChangedMsg{ShellMode: false})
			}
			// Backspace on empty exits shell mode
			if msg.String() == "backspace" && m.textarea.Value() == "" {
				m.exitShellMode()
				return m, util.CmdHandler(ShellModeChangedMsg{ShellMode: false})
			}
			// Up/Down navigate shell history
			if msg.String() == "up" {
				m.shellHistoryUp()
				return m, nil
			}
			if msg.String() == "down" {
				m.shellHistoryDown()
				return m, nil
			}
			// Enter executes shell command
			if m.textarea.Focused() && key.Matches(msg, editorMaps.Send) {
				return m, m.executeShell()
			}
			// Let other keys pass through to textarea
			m.textarea, cmd = m.textarea.Update(msg)
			return m, cmd
		}

		// Vim mode key handling
		if m.vimHandler != nil {
			handled, vimCmd, modeChanged := m.vimHandler.HandleKey(msg, &m.textarea)
			if modeChanged {
				return m, tea.Batch(vimCmd, util.CmdHandler(VimModeChangedMsg{
					Mode: string(m.vimHandler.Mode()),
				}))
			}
			if handled {
				return m, vimCmd
			}
			// Not handled by vim — fall through to normal handling
		}

		if key.Matches(msg, editorMaps.OpenEditor) {
			if m.app.ActiveAgent().IsSessionBusy(m.session.ID) {
				return m, util.ReportWarn("Agent is working, please wait...")
			}
			return m, m.openEditor()
		}
		if key.Matches(msg, DeleteKeyMaps.Escape) {
			m.deleteMode = false
			return m, nil
		}
		// Handle Enter key
		if m.textarea.Focused() && key.Matches(msg, editorMaps.Send) {
			value := m.textarea.Value()
			if len(value) > 0 && value[len(value)-1] == '\\' {
				// If the last character is a backslash, remove it and add a newline
				m.textarea.SetValue(value[:len(value)-1] + "\n")
				return m, nil
			} else {
				// Otherwise, send the message
				return m, m.send()
			}
		}

	}
	m.textarea, cmd = m.textarea.Update(msg)
	return m, cmd
}

func (m *editorCmp) View() tea.View {
	t := theme.CurrentTheme()

	promptChar := ">"
	promptColor := t.Primary()
	if m.mode == modeShell {
		promptChar = "$"
		promptColor = t.Warning()
	} else if m.vimHandler != nil && m.vimHandler.Mode() == vim.ModeNormal {
		promptColor = t.Secondary()
	}

	style := lipgloss.NewStyle().
		Padding(0, 0, 0, 1).
		Bold(true).
		Foreground(promptColor)

	if m.shellExecuting {
		spinnerText := lipgloss.NewStyle().
			Padding(0, 0, 0, 1).
			Foreground(t.Warning()).
			Bold(true).
			Render("$ running...")
		return tea.NewView(spinnerText)
	}

	if len(m.attachments) == 0 {
		return tea.NewView(lipgloss.JoinHorizontal(lipgloss.Top, style.Render(promptChar), m.textarea.View()))
	}
	m.textarea.SetHeight(m.height - 1)
	return tea.NewView(lipgloss.JoinVertical(lipgloss.Top,
		m.attachmentsContent(),
		lipgloss.JoinHorizontal(lipgloss.Top, style.Render(promptChar),
			m.textarea.View()),
	))
}

func (m *editorCmp) SetSize(width, height int) tea.Cmd {
	m.width = width
	m.height = height
	m.textarea.SetWidth(width - 3) // account for the prompt and padding right
	m.textarea.SetHeight(height)
	m.textarea.SetWidth(width)
	return nil
}

func (m *editorCmp) GetSize() (int, int) {
	return m.textarea.Width(), m.textarea.Height()
}

func (m *editorCmp) attachmentsContent() string {
	var styledAttachments []string
	t := theme.CurrentTheme()
	attachmentStyles := styles.BaseStyle().
		MarginLeft(1).
		Background(t.TextMuted()).
		Foreground(t.Text())
	for i, attachment := range m.attachments {
		var filename string
		if len(attachment.FileName) > 10 {
			filename = fmt.Sprintf(" %s %s...", styles.DocumentIcon, attachment.FileName[0:7])
		} else {
			filename = fmt.Sprintf(" %s %s", styles.DocumentIcon, attachment.FileName)
		}
		if m.deleteMode {
			filename = fmt.Sprintf("%d%s", i, filename)
		}
		styledAttachments = append(styledAttachments, attachmentStyles.Render(filename))
	}
	content := lipgloss.JoinHorizontal(lipgloss.Left, styledAttachments...)
	return content
}

func (m *editorCmp) BindingKeys() []key.Binding {
	bindings := []key.Binding{}
	bindings = append(bindings, layout.KeyMapToSlice(editorMaps)...)
	bindings = append(bindings, layout.KeyMapToSlice(DeleteKeyMaps)...)
	return bindings
}

func CreateTextArea(existing *textarea.Model) textarea.Model {
	t := theme.CurrentTheme()
	bgColor := t.Background()
	textColor := t.Text()
	textMutedColor := t.TextMuted()

	ta := textarea.New()
	s := ta.Styles()
	s.Blurred.Base = styles.BaseStyle().Background(bgColor).Foreground(textColor)
	s.Blurred.CursorLine = styles.BaseStyle().Background(bgColor)
	s.Blurred.Placeholder = styles.BaseStyle().Background(bgColor).Foreground(textMutedColor)
	s.Blurred.Text = styles.BaseStyle().Background(bgColor).Foreground(textColor)
	s.Focused.Base = styles.BaseStyle().Background(bgColor).Foreground(textColor)
	s.Focused.CursorLine = styles.BaseStyle().Background(bgColor)
	s.Focused.Placeholder = styles.BaseStyle().Background(bgColor).Foreground(textMutedColor)
	s.Focused.Text = styles.BaseStyle().Background(bgColor).Foreground(textColor)
	ta.SetStyles(s)

	ta.Prompt = " "
	ta.ShowLineNumbers = false
	ta.CharLimit = -1

	if existing != nil {
		ta.SetValue(existing.Value())
		ta.SetWidth(existing.Width())
		ta.SetHeight(existing.Height())
	}

	ta.Focus()
	return ta
}

func NewEditorCmp(app *app.App) tea.Model {
	ta := CreateTextArea(nil)
	var vimH *vim.Handler
	if config.Get().TUI.VimMode {
		vimH = vim.NewHandler()
	}
	return &editorCmp{
		app:        app,
		textarea:   ta,
		mode:       modeNormal,
		vimHandler: vimH,
	}
}
