package page

import (
	"context"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/completions"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/session"
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
			// Execute the selected command
			if cmd, ok := p.findCommand(msg.CompletionValue); ok {
				cmds = append(cmds, util.CmdHandler(dialog.CommandSelectedMsg{Command: cmd}))
			}
			return p, tea.Batch(cmds...)
		}
	case chat.SendMsg:
		cmd := p.sendMessage(msg.Text, msg.Attachments)
		if cmd != nil {
			return p, cmd
		}
	case dialog.CommandRunCustomMsg:
		if p.app.ActiveAgent().IsBusy() {
			return p, util.ReportWarn("Agent is busy, please wait before executing a command...")
		}

		content := msg.Content
		if msg.Args != nil {
			for name, value := range msg.Args {
				placeholder := "$" + name
				content = strings.ReplaceAll(content, placeholder, value)
			}
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
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, keyMap.ShowCompletionDialog):
			p.showCompletionDialog = true
		case key.Matches(msg, keyMap.ShowCommandCompletionDialog):
			p.showCommandCompletionDialog = true
		case key.Matches(msg, keyMap.NewSession):
			p.session = session.Session{}
			return p, tea.Batch(
				p.clearSidebar(),
				util.CmdHandler(chat.SessionClearedMsg{}),
			)
		case key.Matches(msg, keyMap.Cancel):
			if p.session.ID != "" {
				p.app.ActiveAgent().Cancel(p.session.ID)
				return p, nil
			}
		}
	}

	// Route to command completion dialog if active
	if p.showCommandCompletionDialog {
		context, contextCmd := p.commandCompletionDialog.Update(msg)
		p.commandCompletionDialog = context.(dialog.CompletionDialog)
		cmds = append(cmds, contextCmd)

		if keyMsg, ok := msg.(tea.KeyMsg); ok {
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

		if keyMsg, ok := msg.(tea.KeyMsg); ok {
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
		chat.NewSidebarCmp(p.session, p.app.Sessions, p.app.History),
		layout.WithPadding(1, 1, 1, 1),
	)
	return tea.Batch(p.layout.SetRightPanel(sidebarContainer), sidebarContainer.Init())
}

func (p *chatPage) clearSidebar() tea.Cmd {
	return p.layout.ClearRightPanel()
}

func (p *chatPage) sendMessage(text string, attachments []message.Attachment) tea.Cmd {
	var cmds []tea.Cmd
	if p.session.ID == "" {
		var sess session.Session
		var err error
		if p.app.InitialSessionID != "" {
			sess, err = p.app.Sessions.CreateWithID(context.Background(), p.app.InitialSessionID, "New Session")
			p.app.InitialSessionID = ""
		} else {
			sess, err = p.app.Sessions.Create(context.Background(), "New Session")
		}
		if err != nil {
			return util.ReportError(err)
		}

		p.session = sess
		cmd := p.setSidebar()
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
		cmds = append(cmds, util.CmdHandler(chat.SessionSelectedMsg(sess)))
	}

	_, err := p.app.ActiveAgent().Run(context.Background(), p.session.ID, text, attachments...)
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

func (p *chatPage) View() string {
	layoutView := p.layout.View()

	activeDialog := p.activeCompletionDialog()
	if activeDialog != nil {
		_, layoutHeight := p.layout.GetSize()
		editorWidth, editorHeight := p.editor.GetSize()

		activeDialog.SetWidth(editorWidth)
		overlay := activeDialog.View()

		layoutView = layout.PlaceOverlay(
			0,
			layoutHeight-editorHeight-lipgloss.Height(overlay),
			overlay,
			layoutView,
			false,
		)
	}

	return layoutView
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
		layout.WithPadding(1, 1, 0, 1),
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
