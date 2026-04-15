package page

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/completions"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/format"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/session"
	"github.com/opencode-ai/opencode/internal/skill"
	"github.com/opencode-ai/opencode/internal/slashcmd"
	"github.com/opencode-ai/opencode/internal/tui/components/chat"
	"github.com/opencode-ai/opencode/internal/tui/components/dialog"
	"github.com/opencode-ai/opencode/internal/tui/layout"
	"github.com/opencode-ai/opencode/internal/tui/util"
)

var ChatPage PageID = "chat"

type chatPage struct {
	app                         *app.App
	editor                      layout.Container
	messages                    layout.Container
	layout                      layout.SplitPaneLayout
	session                     session.Session
	completionDialog            dialog.CompletionDialog
	showCompletionDialog        bool
	commandCompletionDialog     dialog.CompletionDialog
	showCommandCompletionDialog bool
	commands                    []dialog.Command
	shellMode                   bool
	vimMode                     string // "" when disabled, "INSERT" or "NORMAL" when active
}

type ChatKeyMap struct {
	ShowCompletionDialog        key.Binding
	ShowCommandCompletionDialog key.Binding
	NewSession                  key.Binding
	Cancel                      key.Binding
}

var keyMap = ChatKeyMap{
	ShowCompletionDialog: key.NewBinding(
		key.WithKeys("@"),
		key.WithHelp("@", "Complete"),
	),
	ShowCommandCompletionDialog: key.NewBinding(
		key.WithKeys("/"),
		key.WithHelp("/", "Commands"),
	),
	NewSession: key.NewBinding(
		key.WithKeys("ctrl+n"),
		key.WithHelp("ctrl+n", "new session"),
	),
	Cancel: key.NewBinding(
		key.WithKeys("esc"),
		key.WithHelp("esc", "cancel"),
	),
}

func (p *chatPage) Init() tea.Cmd {
	cmds := []tea.Cmd{
		p.layout.Init(),
		p.completionDialog.Init(),
		p.commandCompletionDialog.Init(),
	}
	if p.session.ID != "" {
		cmds = append(cmds, p.setSidebar())
		cmds = append(cmds, util.CmdHandler(chat.SessionSelectedMsg(p.session)))
	}
	return tea.Batch(cmds...)
}

func (p *chatPage) findCommand(id string) (dialog.Command, bool) {
	for _, cmd := range p.commands {
		if cmd.ID == id {
			return cmd, true
		}
	}
	return dialog.Command{}, false
}

func (p *chatPage) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		cmd := p.layout.SetSize(msg.Width, msg.Height)
		cmds = append(cmds, cmd)
	case dialog.CompletionDialogCloseMsg:
		if msg.ProviderID == completions.CommandCompletionProviderID {
			p.showCommandCompletionDialog = false
		} else {
			p.showCompletionDialog = false
		}
	case dialog.CompletionSelectedMsg:
		if msg.ProviderID == completions.CommandCompletionProviderID {
			p.showCommandCompletionDialog = false
			// Remove the /query text from the editor
			cmds = append(cmds, util.CmdHandler(dialog.CompletionRemoveTextMsg{
				SearchString: msg.SearchString,
			}))
			// Check if it's a skill selection (value starts with "skill:")
			if strings.HasPrefix(msg.CompletionValue, "skill:") {
				skillName := strings.TrimPrefix(msg.CompletionValue, "skill:")
				if s, err := skill.Get(skillName); err == nil && s.IsUserInvocable() {
					// Check if skill content has $PLACEHOLDER patterns — show argument dialog
					argCmd := dialog.ParameterizedSkillHandler(s)
					if argCmd != nil {
						cmds = append(cmds, argCmd)
					} else {
						baseDir := filepath.Dir(s.Location)
						content := skill.SubstituteContent(s.Content, skill.SubstituteParams{
							SkillDir:  baseDir,
							SessionID: p.session.ID,
						})
						content = format.ExpandShellMarkup(context.Background(), content, config.WorkingDirectory())
						content = skill.WrapSkillContent(s.Name, content)
						cmd := p.sendMessage(content, nil)
						if cmd != nil {
							cmds = append(cmds, cmd)
						}
					}
				}
				return p, tea.Batch(cmds...)
			}
			// Execute the selected command
			if cmd, ok := p.findCommand(msg.CompletionValue); ok {
				cmds = append(cmds, util.CmdHandler(dialog.CommandSelectedMsg{Command: cmd}))
			}
			return p, tea.Batch(cmds...)
		}
	case chat.ShellModeChangedMsg:
		p.shellMode = msg.ShellMode
		return p, nil
	case chat.VimModeChangedMsg:
		p.vimMode = msg.Mode
		return p, nil
	case chat.ShellResultMsg:
		cmds = append(cmds, p.handleShellResult(msg))
	case chat.SendMsg:
		if resolved := p.resolveInlineSlash(msg.Text); resolved != nil {
			return p, resolved
		}
		cmd := p.sendMessage(msg.Text, msg.Attachments)
		if cmd != nil {
			return p, cmd
		}
	case dialog.CommandRunCustomMsg:
		if p.app.ActiveAgent().IsBusy() {
			return p, util.ReportWarn("Agent is busy, please wait before executing a command...")
		}

		content := msg.Content
		if strings.HasPrefix(msg.CommandID, "skill:") {
			// Skill: use SubstituteContent for proper $0, $ARGUMENTS, ${SKILL_DIR} handling
			skillName := strings.TrimPrefix(msg.CommandID, "skill:")
			s, err := skill.Get(skillName)
			if err != nil {
				return p, util.ReportError(err)
			}
			// Build combined args string from the dialog values
			args := ""
			if msg.Args != nil {
				if v, ok := msg.Args["ARGUMENTS"]; ok {
					args = v
				} else if positional := joinPositionalArgs(msg.Args); positional != "" {
					args = positional
				} else {
					for name, value := range msg.Args {
						placeholder := "$" + name
						content = strings.ReplaceAll(content, placeholder, value)
					}
				}
			}
			baseDir := filepath.Dir(s.Location)
			content = skill.SubstituteContent(content, skill.SubstituteParams{
				Args:      args,
				SkillDir:  baseDir,
				SessionID: p.session.ID,
			})
		} else if msg.Args != nil {
			for name, value := range msg.Args {
				placeholder := "$" + name
				content = strings.ReplaceAll(content, placeholder, value)
			}
		}

		// Expand !`cmd` shell markup after argument substitution
		content = format.ExpandShellMarkup(context.Background(), content, config.WorkingDirectory())

		// Wrap skill content so the LLM won't re-invoke the skill tool
		if strings.HasPrefix(msg.CommandID, "skill:") {
			skillName := strings.TrimPrefix(msg.CommandID, "skill:")
			content = skill.WrapSkillContent(skillName, content)
		}

		cmd := p.sendMessage(content, nil)
		if cmd != nil {
			return p, cmd
		}
	case chat.SessionClearedMsg:
		p.session = session.Session{}
		cmds = append(cmds, p.clearSidebar())
	case chat.SessionSelectedMsg:
		if p.session.ID == "" {
			cmd := p.setSidebar()
			if cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
		p.session = msg
	case tea.PasteMsg:
		// Paste events go directly to the editor, skip dialog triggers
		u, cmd := p.layout.Update(msg)
		p.layout = u.(layout.SplitPaneLayout)
		return p, cmd
	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, keyMap.Cancel):
			// In shell mode, ESC should exit shell mode (handled by editor)
			if p.shellMode {
				break
			}
			// When a completion dialog is open, close it first (dialog takes priority over vim)
			if p.showCompletionDialog || p.showCommandCompletionDialog {
				break // let ESC flow to dialog routing below
			}
			// In vim mode, ESC flows to the editor for vim mode switching
			if p.isVimMode() {
				break
			}
			if p.session.ID != "" {
				p.app.ActiveAgent().Cancel(p.session.ID)
				return p, nil
			}
		case key.Matches(msg, keyMap.ShowCompletionDialog):
			if !p.showCommandCompletionDialog && !p.shellMode {
				p.showCompletionDialog = true
			}
		case key.Matches(msg, keyMap.ShowCommandCompletionDialog):
			if !p.showCompletionDialog && !p.shellMode {
				p.showCommandCompletionDialog = true
			}
		case key.Matches(msg, keyMap.NewSession):
			p.session = session.Session{}
			return p, tea.Batch(
				p.clearSidebar(),
				util.CmdHandler(chat.SessionClearedMsg{}),
			)
		}
	}

	// Route to command completion dialog if active
	if p.showCommandCompletionDialog {
		context, contextCmd := p.commandCompletionDialog.Update(msg)
		p.commandCompletionDialog = context.(dialog.CompletionDialog)
		cmds = append(cmds, contextCmd)

		if keyMsg, ok := msg.(tea.KeyPressMsg); ok {
			if keyMsg.String() == "enter" || keyMsg.String() == "tab" || keyMsg.String() == "shift+tab" {
				return p, tea.Batch(cmds...)
			}
		}
	}

	// Route to file completion dialog if active
	if p.showCompletionDialog {
		context, contextCmd := p.completionDialog.Update(msg)
		p.completionDialog = context.(dialog.CompletionDialog)
		cmds = append(cmds, contextCmd)

		if keyMsg, ok := msg.(tea.KeyPressMsg); ok {
			if keyMsg.String() == "enter" || keyMsg.String() == "tab" || keyMsg.String() == "shift+tab" {
				return p, tea.Batch(cmds...)
			}
		}
	}

	u, cmd := p.layout.Update(msg)
	cmds = append(cmds, cmd)
	p.layout = u.(layout.SplitPaneLayout)

	return p, tea.Batch(cmds...)
}

func (p *chatPage) setSidebar() tea.Cmd {
	sidebarContainer := layout.NewContainer(
		chat.NewSidebarCmp(p.session, p.app.Sessions, p.app.History, p.app),
		layout.WithPadding(1, 1, 1, 1),
	)
	return tea.Batch(p.layout.SetRightPanel(sidebarContainer), sidebarContainer.Init())
}

func (p *chatPage) clearSidebar() tea.Cmd {
	return p.layout.ClearRightPanel()
}

func (p *chatPage) ensureSession() (tea.Cmd, error) {
	if p.session.ID != "" {
		return nil, nil
	}
	var sess session.Session
	var err error
	if p.app.InitialSessionID != "" {
		sess, err = p.app.Sessions.CreateWithID(context.Background(), p.app.InitialSessionID, "New Session")
		p.app.InitialSessionID = ""
	} else {
		sess, err = p.app.Sessions.Create(context.Background(), "New Session")
	}
	if err != nil {
		return nil, err
	}
	p.session = sess
	var cmds []tea.Cmd
	cmd := p.setSidebar()
	if cmd != nil {
		cmds = append(cmds, cmd)
	}
	cmds = append(cmds, util.CmdHandler(chat.SessionSelectedMsg(sess)))
	return tea.Batch(cmds...), nil
}

func (p *chatPage) handleShellResult(msg chat.ShellResultMsg) tea.Cmd {
	var cmds []tea.Cmd

	sessionCmd, err := p.ensureSession()
	if err != nil {
		return util.ReportError(err)
	}
	if sessionCmd != nil {
		cmds = append(cmds, sessionCmd)
	}

	ctx := context.Background()

	// Create command echo message
	cmdText := fmt.Sprintf("$ %s", msg.Command)
	_, err = p.app.Messages.Create(ctx, p.session.ID, message.CreateMessageParams{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: cmdText}},
	})
	if err != nil {
		return util.ReportError(err)
	}

	// Build output text
	var output string
	if msg.Err != nil {
		output = fmt.Sprintf("[error: %s]", msg.Err.Error())
	} else {
		stdout := msg.Stdout
		stderr := msg.Stderr

		// Apply truncation like bash tool
		if len(stdout) > tools.MaxOutputBytes || len(strings.Split(stdout, "\n")) > tools.MaxOutputLines {
			lines := strings.Split(stdout, "\n")
			if len(lines) > tools.MaxOutputLines {
				head := strings.Join(lines[:500], "\n")
				tail := strings.Join(lines[len(lines)-500:], "\n")
				stdout = fmt.Sprintf("%s\n\n... (%d lines truncated) ...\n\n%s", head, len(lines)-1000, tail)
			}
		}

		output = stdout
		if stderr != "" {
			if output != "" {
				output += "\n"
			}
			output += stderr
		}
		if msg.ExitCode != 0 {
			if output != "" {
				output += "\n"
			}
			output += fmt.Sprintf("[exit code: %d]", msg.ExitCode)
		}
		if output == "" {
			output = "(no output)"
		}
	}

	// Create output message
	outputText := fmt.Sprintf("```\n%s\n```", output)
	_, err = p.app.Messages.Create(ctx, p.session.ID, message.CreateMessageParams{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: outputText}},
	})
	if err != nil {
		return util.ReportError(err)
	}

	return tea.Batch(cmds...)
}

func (p *chatPage) sendMessage(text string, attachments []message.Attachment) tea.Cmd {
	var cmds []tea.Cmd
	sessionCmd, err := p.ensureSession()
	if err != nil {
		return util.ReportError(err)
	}
	if sessionCmd != nil {
		cmds = append(cmds, sessionCmd)
	}

	_, err = p.app.ActiveAgent().Run(context.Background(), p.session.ID, text, attachments...)
	if err != nil {
		return util.ReportError(err)
	}
	return tea.Batch(cmds...)
}

func (p *chatPage) SetSize(width, height int) tea.Cmd {
	return p.layout.SetSize(width, height)
}

func (p *chatPage) GetSize() (int, int) {
	return p.layout.GetSize()
}

func (p *chatPage) View() tea.View {
	layoutView := p.layout.View().Content

	activeDialog := p.activeCompletionDialog()
	if activeDialog != nil {
		_, layoutHeight := p.layout.GetSize()
		editorWidth, editorHeight := p.editor.GetSize()

		activeDialog.SetWidth(editorWidth)
		overlay := activeDialog.View().Content

		layoutView = layout.PlaceOverlay(
			0,
			layoutHeight-editorHeight-lipgloss.Height(overlay),
			overlay,
			layoutView,
			false,
		)
	}

	return tea.NewView(layoutView)
}

func (p *chatPage) activeCompletionDialog() dialog.CompletionDialog {
	if p.showCommandCompletionDialog {
		return p.commandCompletionDialog
	}
	if p.showCompletionDialog {
		return p.completionDialog
	}
	return nil
}

func (p *chatPage) HasActiveOverlay() bool {
	return p.showCompletionDialog || p.showCommandCompletionDialog
}

func (p *chatPage) IsShellMode() bool {
	return p.shellMode
}

func (p *chatPage) ConsumesCtrlC() bool {
	// Vim INSERT mode or pending NORMAL command should consume Ctrl+C
	return p.vimMode == "INSERT"
}

func (p *chatPage) isVimMode() bool {
	return p.vimMode != ""
}

func (p *chatPage) BindingKeys() []key.Binding {
	bindings := layout.KeyMapToSlice(keyMap)
	bindings = append(bindings, p.messages.BindingKeys()...)
	bindings = append(bindings, p.editor.BindingKeys()...)
	return bindings
}

func NewChatPage(app *app.App, commands []dialog.Command) tea.Model {
	cg := completions.NewFileAndFolderContextGroup()
	completionDialog := dialog.NewCompletionDialogCmp(cg)

	cmdProvider := completions.NewCommandCompletionProvider(commands)
	commandCompletionDialog := dialog.NewCompletionDialogCmp(cmdProvider)

	messagesContainer := layout.NewContainer(
		chat.NewMessagesCmp(app),
		layout.WithPadding(0, 1, 0, 1),
	)
	editorContainer := layout.NewContainer(
		chat.NewEditorCmp(app),
		layout.WithBorder(true, false, false, false),
	)

	var sess session.Session
	if app.InitialSession != nil {
		sess = *app.InitialSession
	}

	return &chatPage{
		app:                     app,
		editor:                  editorContainer,
		messages:                messagesContainer,
		session:                 sess,
		completionDialog:        completionDialog,
		commandCompletionDialog: commandCompletionDialog,
		commands:                commands,
		layout: layout.NewSplitPane(
			layout.WithLeftPanel(messagesContainer),
			layout.WithBottomPanel(editorContainer),
		),
	}
}

// joinPositionalArgs reconstructs a space-separated args string from
// per-index dialog values (keys "0", "1", "2", ...). Returns "" if
// the map doesn't contain numeric keys.
func joinPositionalArgs(args map[string]string) string {
	indices := make([]int, 0, len(args))
	values := make(map[int]string, len(args))
	for key, val := range args {
		idx, err := strconv.Atoi(key)
		if err != nil {
			return ""
		}
		indices = append(indices, idx)
		values[idx] = val
	}
	if len(indices) == 0 {
		return ""
	}
	sort.Ints(indices)
	parts := make([]string, len(indices))
	for i, idx := range indices {
		parts[i] = values[idx]
	}
	return strings.Join(parts, " ")
}

func (p *chatPage) resolveInlineSlash(text string) tea.Cmd {
	parsed := slashcmd.Parse(text)
	if parsed == nil {
		return nil
	}

	// Extract CommandInfo for resolve (Handler-free matching)
	infos := make([]slashcmd.CommandInfo, len(p.commands))
	for i, c := range p.commands {
		infos[i] = c.CommandInfo
	}

	skills := skill.All()
	action, err := slashcmd.Resolve(parsed, infos, skills, true)
	if err != nil {
		return util.ReportWarn(err.Error())
	}

	switch action.Type {
	case slashcmd.ActionCommand:
		info := action.Command
		// Inline args shortcut: only when content has $ARGUMENTS as the sole placeholder
		if info.Content != "" && parsed.Args != "" && slashcmd.HasOnlyArgumentsPlaceholder(info.Content) {
			content := slashcmd.SubstituteArgs(info.Content, parsed.Args)
			content = format.ExpandShellMarkup(context.Background(), content, config.WorkingDirectory())
			return util.CmdHandler(dialog.CommandRunCustomMsg{
				Content: content,
				Args:    map[string]string{"ARGUMENTS": parsed.Args},
			})
		}
		// No inline args or multiple placeholders — use handler (may show dialog)
		if cmd, ok := p.findCommand(info.ID); ok && cmd.Handler != nil {
			return cmd.Handler(cmd)
		}
		return nil

	case slashcmd.ActionSkill:
		s := action.Skill
		// If no inline args provided, check for named placeholders and show dialog
		if parsed.Args == "" {
			if argCmd := dialog.ParameterizedSkillHandler(s); argCmd != nil {
				return argCmd
			}
		}
		baseDir := filepath.Dir(s.Location)
		content := skill.SubstituteContent(s.Content, skill.SubstituteParams{
			Args:      parsed.Args,
			SkillDir:  baseDir,
			SessionID: p.session.ID,
		})
		content = format.ExpandShellMarkup(context.Background(), content, config.WorkingDirectory())
		content = skill.WrapSkillContent(s.Name, content)
		return util.CmdHandler(chat.SendMsg{Text: content})

	default:
		return nil
	}
}
