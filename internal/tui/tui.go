package tui

import (
	"context"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/pubsub"
	"github.com/opencode-ai/opencode/internal/session"
	"github.com/opencode-ai/opencode/internal/tui/components/chat"
	"github.com/opencode-ai/opencode/internal/tui/components/core"
	"github.com/opencode-ai/opencode/internal/tui/components/dialog"
	"github.com/opencode-ai/opencode/internal/tui/components/logs"
	"github.com/opencode-ai/opencode/internal/tui/layout"
	"github.com/opencode-ai/opencode/internal/tui/page"
	"github.com/opencode-ai/opencode/internal/tui/styles"
	"github.com/opencode-ai/opencode/internal/tui/theme"
	"github.com/opencode-ai/opencode/internal/tui/util"
)

type keyMap struct {
	Logs            key.Binding
	Quit            key.Binding
	Help            key.Binding
	SwitchSession   key.Binding
	Commands        key.Binding
	Filepicker      key.Binding
	Models          key.Binding
	SwitchTheme     key.Binding
	PruneSession    key.Binding
	SwitchAgent     key.Binding
	SwitchAgentBack key.Binding
}

type (
	startCompactSessionMsg struct{}
	toggleAutoApproveMsg   struct{}
	sessionDeletedMsg      struct{ id string }
)

const (
	quitKey = "q"
)

var keys = keyMap{
	Logs: key.NewBinding(
		key.WithKeys("ctrl+l"),
		key.WithHelp("ctrl+l", "logs"),
	),

	Quit: key.NewBinding(
		key.WithKeys("ctrl+c"),
		key.WithHelp("ctrl+c", "quit"),
	),
	Help: key.NewBinding(
		key.WithKeys("ctrl+_", "ctrl+h"),
		key.WithHelp("ctrl+h", "toggle help"),
	),

	SwitchSession: key.NewBinding(
		key.WithKeys("ctrl+s"),
		key.WithHelp("ctrl+s", "switch session"),
	),

	Commands: key.NewBinding(
		key.WithKeys("ctrl+k"),
		key.WithHelp("ctrl+k", "commands"),
	),
	Filepicker: key.NewBinding(
		key.WithKeys("ctrl+f"),
		key.WithHelp("ctrl+f", "select files to upload"),
	),
	Models: key.NewBinding(
		key.WithKeys("ctrl+o"),
		key.WithHelp("ctrl+o", "model selection"),
	),

	SwitchTheme: key.NewBinding(
		key.WithKeys("ctrl+t"),
		key.WithHelp("ctrl+t", "switch theme"),
	),

	PruneSession: key.NewBinding(
		key.WithKeys("ctrl+p"),
		key.WithHelp("ctrl+p", "delete session"),
	),
	SwitchAgent: key.NewBinding(
		key.WithKeys("tab"),
		key.WithHelp("tab", "switch agent"),
	),
	SwitchAgentBack: key.NewBinding(
		key.WithKeys("shift+tab"),
		key.WithHelp("shift+tab", "switch agent back"),
	),
}

var helpEsc = key.NewBinding(
	key.WithKeys("?"),
	key.WithHelp("?", "toggle help"),
)

var returnKey = key.NewBinding(
	key.WithKeys("esc"),
	key.WithHelp("esc", "close"),
)

var logsKeyReturnKey = key.NewBinding(
	key.WithKeys("esc", "backspace", quitKey),
	key.WithHelp("esc/q", "go back"),
)

type appModel struct {
	width, height   int
	currentPage     page.PageID
	previousPage    page.PageID
	pages           map[page.PageID]tea.Model
	loadedPages     map[page.PageID]bool
	status          core.StatusCmp
	app             *app.App
	selectedSession session.Session

	showPermissions bool
	permissions     dialog.PermissionDialogCmp

	showHelp bool
	help     dialog.HelpCmp

	showQuit bool
	quit     dialog.QuitDialog

	showSessionDialog bool
	sessionDialog     dialog.SessionDialog

	showDeleteSessionDialog bool
	deleteSessionDialog     dialog.SessionDialog

	showCommandDialog bool
	commandDialog     dialog.CommandDialog
	commands          []dialog.Command

	showModelDialog bool
	modelDialog     dialog.ModelDialog

	showInitDialog bool
	initDialog     dialog.InitDialogCmp

	showFilepicker bool
	filepicker     dialog.FilepickerCmp

	showThemeDialog bool
	themeDialog     dialog.ThemeDialog

	showMultiArgumentsDialog bool
	multiArgumentsDialog     dialog.MultiArgumentsDialogCmp

	isCompacting      bool
	compactingMessage string
}

func (a appModel) Init() tea.Cmd {
	var cmds []tea.Cmd
	cmd := a.pages[a.currentPage].Init()
	a.loadedPages[a.currentPage] = true
	cmds = append(cmds, cmd)
	cmd = a.status.Init()
	cmds = append(cmds, cmd)
	cmd = a.quit.Init()
	cmds = append(cmds, cmd)
	cmd = a.help.Init()
	cmds = append(cmds, cmd)
	cmd = a.sessionDialog.Init()
	cmds = append(cmds, cmd)
	cmd = a.commandDialog.Init()
	cmds = append(cmds, cmd)
	cmd = a.modelDialog.Init()
	cmds = append(cmds, cmd)
	cmd = a.initDialog.Init()
	cmds = append(cmds, cmd)
	cmd = a.filepicker.Init()
	cmds = append(cmds, cmd)
	cmd = a.themeDialog.Init()
	cmds = append(cmds, cmd)
	cmd = a.deleteSessionDialog.Init()
	cmds = append(cmds, cmd)

	// Check if we should show the init dialog
	cmds = append(cmds, func() tea.Msg {
		shouldShow, err := config.ShouldShowInitDialog()
		if err != nil {
			return util.InfoMsg{
				Type: util.InfoTypeError,
				Msg:  "Failed to check init status: " + err.Error(),
			}
		}
		return dialog.ShowInitDialogMsg{Show: shouldShow}
	})
	cmds = append(cmds, tea.RequestBackgroundColor)

	return tea.Batch(cmds...)
}

func (a appModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	var cmd tea.Cmd
	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		theme.SetIsDark(msg.IsDark())
		return a, nil
	case tea.WindowSizeMsg:
		msg.Height -= 1 // Make space for the status bar
		a.width, a.height = msg.Width, msg.Height

		s, _ := a.status.Update(msg)
		a.status = s.(core.StatusCmp)
		a.pages[a.currentPage], cmd = a.pages[a.currentPage].Update(msg)
		cmds = append(cmds, cmd)

		prm, permCmd := a.permissions.Update(msg)
		a.permissions = prm.(dialog.PermissionDialogCmp)
		cmds = append(cmds, permCmd)

		help, helpCmd := a.help.Update(msg)
		a.help = help.(dialog.HelpCmp)
		cmds = append(cmds, helpCmd)

		session, sessionCmd := a.sessionDialog.Update(msg)
		a.sessionDialog = session.(dialog.SessionDialog)
		cmds = append(cmds, sessionCmd)

		command, commandCmd := a.commandDialog.Update(msg)
		a.commandDialog = command.(dialog.CommandDialog)
		cmds = append(cmds, commandCmd)

		filepicker, filepickerCmd := a.filepicker.Update(msg)
		a.filepicker = filepicker.(dialog.FilepickerCmp)
		cmds = append(cmds, filepickerCmd)

		a.initDialog.SetSize(msg.Width, msg.Height)

		if a.showMultiArgumentsDialog {
			a.multiArgumentsDialog.SetSize(msg.Width, msg.Height)
			args, argsCmd := a.multiArgumentsDialog.Update(msg)
			a.multiArgumentsDialog = args.(dialog.MultiArgumentsDialogCmp)
			cmds = append(cmds, argsCmd, a.multiArgumentsDialog.Init())
		}

		return a, tea.Batch(cmds...)
	// Status
	case util.InfoMsg:
		s, cmd := a.status.Update(msg)
		a.status = s.(core.StatusCmp)
		cmds = append(cmds, cmd)
		return a, tea.Batch(cmds...)
	case pubsub.Event[logging.LogMessage]:
		if msg.Payload.Persist {
			switch msg.Payload.Level {
			case "error":
				s, cmd := a.status.Update(util.InfoMsg{
					Type: util.InfoTypeError,
					Msg:  msg.Payload.Message,
					TTL:  msg.Payload.PersistTime,
				})
				a.status = s.(core.StatusCmp)
				cmds = append(cmds, cmd)
			case "info":
				s, cmd := a.status.Update(util.InfoMsg{
					Type: util.InfoTypeInfo,
					Msg:  msg.Payload.Message,
					TTL:  msg.Payload.PersistTime,
				})
				a.status = s.(core.StatusCmp)
				cmds = append(cmds, cmd)

			case "warn":
				s, cmd := a.status.Update(util.InfoMsg{
					Type: util.InfoTypeWarn,
					Msg:  msg.Payload.Message,
					TTL:  msg.Payload.PersistTime,
				})

				a.status = s.(core.StatusCmp)
				cmds = append(cmds, cmd)
			default:
				s, cmd := a.status.Update(util.InfoMsg{
					Type: util.InfoTypeInfo,
					Msg:  msg.Payload.Message,
					TTL:  msg.Payload.PersistTime,
				})
				a.status = s.(core.StatusCmp)
				cmds = append(cmds, cmd)
			}
		}
	case util.ClearStatusMsg:
		s, _ := a.status.Update(msg)
		a.status = s.(core.StatusCmp)

	// Permission
	case pubsub.Event[permission.PermissionRequest]:
		a.showPermissions = true
		return a, a.permissions.SetPermissions(msg.Payload)
	case dialog.PermissionResponseMsg:
		var cmd tea.Cmd
		switch msg.Action {
		case dialog.PermissionAllow:
			a.app.Permissions.Grant(msg.Permission)
		case dialog.PermissionAllowForSession:
			a.app.Permissions.GrantPersistant(msg.Permission)
		case dialog.PermissionDeny:
			a.app.Permissions.Deny(msg.Permission)
		}
		a.showPermissions = false
		return a, cmd

	case page.PageChangeMsg:
		return a, a.moveToPage(msg.ID)

	case dialog.CloseQuitMsg:
		a.showQuit = false
		return a, nil

	case dialog.CloseSessionDialogMsg:
		if a.showDeleteSessionDialog {
			a.showDeleteSessionDialog = false
			return a, nil
		}
		a.showSessionDialog = false
		return a, nil

	case dialog.CloseCommandDialogMsg:
		a.showCommandDialog = false
		return a, nil

	case toggleAutoApproveMsg:
		if a.selectedSession.ID == "" {
			return a, util.ReportWarn("No active session")
		}
		if a.app.Permissions.IsAutoApproveSession(a.selectedSession.ID) {
			a.app.Permissions.RemoveAutoApproveSession(a.selectedSession.ID)
			s, _ := a.status.Update(core.AutoApproveChangedMsg{Active: false})
			a.status = s.(core.StatusCmp)
			return a, util.ReportInfo("Auto-approve disabled")
		}
		a.app.Permissions.AutoApproveSession(a.selectedSession.ID)
		s, _ := a.status.Update(core.AutoApproveChangedMsg{Active: true})
		a.status = s.(core.StatusCmp)
		return a, util.ReportInfo("Auto-approve enabled")

	case startCompactSessionMsg:
		// Start compacting the current session
		a.isCompacting = true
		a.compactingMessage = "Starting summarization..."

		if a.selectedSession.ID == "" {
			a.isCompacting = false
			return a, util.ReportWarn("No active session to summarize")
		}

		// Start the summarization process
		return a, func() tea.Msg {
			ctx := context.Background()
			a.app.ActiveAgent().Summarize(ctx, a.selectedSession.ID)
			return nil
		}

	case pubsub.Event[agent.AgentEvent]:
		payload := msg.Payload
		if payload.Error != nil {
			a.isCompacting = false
			return a, util.ReportError(payload.Error)
		}

		a.compactingMessage = payload.Progress

		if payload.Done && payload.Type == agent.AgentEventTypeSummarize {
			a.isCompacting = false
			return a, util.ReportInfo("Session summarization complete")
		} else if payload.Done && payload.Type == agent.AgentEventTypeResponse && a.selectedSession.ID != "" {
			model := a.app.ActiveAgent().Model()
			contextWindow := model.ContextWindow
			tokens := a.selectedSession.CompletionTokens + a.selectedSession.PromptTokens
			logging.Info("auto-compaction status", "contextLength", contextWindow, "tokens", tokens)
			if (tokens >= int64(float64(contextWindow)*0.95)) && config.Get().AutoCompact {
				logging.Info("auto-compaction triggered...")
				return a, util.CmdHandler(startCompactSessionMsg{})
			}
		}
		// Continue listening for events
		return a, nil

	case dialog.CloseThemeDialogMsg:
		a.showThemeDialog = false
		return a, nil

	case dialog.ThemeChangedMsg:
		styles.InvalidateMarkdownCache()
		a.pages[a.currentPage], cmd = a.pages[a.currentPage].Update(msg)
		s, _ := a.status.Update(msg)
		a.status = s.(core.StatusCmp)
		a.showThemeDialog = false
		return a, tea.Batch(cmd, util.ReportInfo("Theme changed to: "+msg.ThemeName))

	case dialog.CloseModelDialogMsg:
		a.showModelDialog = false
		return a, nil

	case dialog.ModelSelectedMsg:
		a.showModelDialog = false

		model, err := a.app.ActiveAgent().Update(a.app.ActiveAgentName(), msg.Model.ID)
		if err != nil {
			return a, util.ReportError(err)
		}

		return a, tea.Batch(
			util.CmdHandler(core.ActiveAgentChangedMsg{Name: a.app.ActiveAgentName()}),
			util.ReportInfo(fmt.Sprintf("Model changed to %s", model.Name)),
		)

	case dialog.ShowInitDialogMsg:
		a.showInitDialog = msg.Show
		return a, nil

	case dialog.CloseInitDialogMsg:
		a.showInitDialog = false
		if msg.Initialize {
			// Run the initialization command
			for _, cmd := range a.commands {
				if cmd.ID == "init" {
					// Mark the project as initialized
					if err := config.MarkProjectInitialized(); err != nil {
						return a, util.ReportError(err)
					}
					return a, cmd.Handler(cmd)
				}
			}
		} else {
			// Mark the project as initialized without running the command
			if err := config.MarkProjectInitialized(); err != nil {
				return a, util.ReportError(err)
			}
		}
		return a, nil

	case sessionDeletedMsg:
		cmds := []tea.Cmd{util.ReportInfo("Session deleted")}
		if a.selectedSession.ID == msg.id {
			a.selectedSession = session.Session{}
			a.sessionDialog.SetSelectedSession("")
			if a.currentPage == page.ChatPage {
				cmds = append(cmds, util.CmdHandler(chat.SessionClearedMsg{}))
			}
			s, _ := a.status.Update(core.AutoApproveChangedMsg{Active: false})
			a.status = s.(core.StatusCmp)
		}
		return a, tea.Batch(cmds...)

	case chat.SessionSelectedMsg:
		a.selectedSession = msg
		a.sessionDialog.SetSelectedSession(msg.ID)
		if a.app.AutoApprove && !a.app.Permissions.IsAutoApproveSession(msg.ID) {
			a.app.Permissions.AutoApproveSession(msg.ID)
			a.app.AutoApprove = false
		}
		autoApprove := a.app.Permissions.IsAutoApproveSession(msg.ID)
		s, _ := a.status.Update(core.AutoApproveChangedMsg{Active: autoApprove})
		a.status = s.(core.StatusCmp)

	case pubsub.Event[session.Session]:
		if msg.Type == pubsub.UpdatedEvent && msg.Payload.ID == a.selectedSession.ID {
			a.selectedSession = msg.Payload
		}
	case dialog.SessionSelectedMsg:
		// if we're in "delete" mode, delete instead of switch
		if a.showDeleteSessionDialog {
			a.showDeleteSessionDialog = false
			deletedID := msg.Session.ID
			return a, func() tea.Msg {
				ctx := context.Background()
				if err := a.app.Sessions.Delete(ctx, deletedID); err != nil {
					return util.InfoMsg{Type: util.InfoTypeError, Msg: "Delete failed: " + err.Error()}
				}
				return sessionDeletedMsg{id: deletedID}
			}
		}
		// otherwise fall through to normal "switch session"
		a.showSessionDialog = false
		if a.currentPage == page.ChatPage {
			return a, util.CmdHandler(chat.SessionSelectedMsg(msg.Session))
		}
		return a, nil

	case dialog.CommandSelectedMsg:
		a.showCommandDialog = false
		// Execute the command handler if available
		if msg.Command.Handler != nil {
			return a, msg.Command.Handler(msg.Command)
		}
		return a, util.ReportInfo("Command selected: " + msg.Command.Title)

	case dialog.ShowMultiArgumentsDialogMsg:
		// Show multi-arguments dialog
		a.multiArgumentsDialog = dialog.NewMultiArgumentsDialogCmp(msg.CommandID, msg.Content, msg.ArgNames, msg.ArgHints)
		a.showMultiArgumentsDialog = true
		return a, a.multiArgumentsDialog.Init()

	case dialog.CloseMultiArgumentsDialogMsg:
		// Close multi-arguments dialog
		a.showMultiArgumentsDialog = false

		// If submitted, replace all named arguments and run the command
		if msg.Submit {
			content := msg.Content

			// Replace each named argument with its value
			for name, value := range msg.Args {
				placeholder := "$" + name
				content = strings.ReplaceAll(content, placeholder, value)
			}

			// Execute the command with arguments
			return a, util.CmdHandler(dialog.CommandRunCustomMsg{
				Content: content,
				Args:    msg.Args,
			})
		}
		return a, nil

	case tea.KeyPressMsg:
		// If multi-arguments dialog is open, let it handle the key press first
		if a.showMultiArgumentsDialog {
			args, cmd := a.multiArgumentsDialog.Update(msg)
			a.multiArgumentsDialog = args.(dialog.MultiArgumentsDialogCmp)
			return a, cmd
		}

		switch {

		case key.Matches(msg, keys.Quit):
			// In shell mode, ctrl+c exits shell mode instead of showing quit dialog
			if a.pageIsShellMode() {
				break
			}
			a.showQuit = !a.showQuit
			if a.showHelp {
				a.showHelp = false
			}
			if a.showSessionDialog {
				a.showSessionDialog = false
			}
			if a.showCommandDialog {
				a.showCommandDialog = false
			}
			if a.showFilepicker {
				a.showFilepicker = false
				a.filepicker.ToggleFilepicker(a.showFilepicker)
			}
			if a.showModelDialog {
				a.showModelDialog = false
			}
			if a.showMultiArgumentsDialog {
				a.showMultiArgumentsDialog = false
			}
			return a, nil
		case key.Matches(msg, keys.SwitchSession):
			if a.currentPage == page.ChatPage && !a.showQuit && !a.showPermissions && !a.showCommandDialog {
				// Load sessions and show the dialog
				sessions, err := a.app.Sessions.List(context.Background())
				if err != nil {
					return a, util.ReportError(err)
				}
				if len(sessions) == 0 {
					return a, util.ReportWarn("No sessions available")
				}
				a.sessionDialog.SetSessions(sessions)
				a.showSessionDialog = true
				return a, nil
			}
			return a, nil
		case key.Matches(msg, keys.Commands):
			if a.currentPage == page.ChatPage && !a.showQuit && !a.showPermissions && !a.showSessionDialog && !a.showThemeDialog && !a.showFilepicker {
				// Show commands dialog
				if len(a.commands) == 0 {
					return a, util.ReportWarn("No commands available")
				}
				a.commandDialog.SetCommands(a.commands)
				a.showCommandDialog = true
				return a, nil
			}
			return a, nil
		case key.Matches(msg, keys.Models):
			if a.showModelDialog {
				a.showModelDialog = false
				return a, nil
			}
			if a.currentPage == page.ChatPage && !a.showQuit && !a.showPermissions && !a.showSessionDialog && !a.showCommandDialog {
				a.showModelDialog = true
				return a, nil
			}
			return a, nil
		case key.Matches(msg, keys.SwitchTheme):
			if !a.showQuit && !a.showPermissions && !a.showSessionDialog && !a.showCommandDialog {
				// Show theme switcher dialog
				a.showThemeDialog = true
				// Theme list is dynamically loaded by the dialog component
				return a, a.themeDialog.Init()
			}
			return a, nil
		case key.Matches(msg, keys.PruneSession):
			if a.currentPage == page.ChatPage && !a.showQuit && !a.showPermissions &&
				!a.showSessionDialog && !a.showCommandDialog {
				sessions, err := a.app.Sessions.List(context.Background())
				if err != nil {
					return a, util.ReportError(err)
				}
				if len(sessions) == 0 {
					return a, util.ReportWarn("No sessions available")
				}
				a.deleteSessionDialog.SetTitle("Prune Session")
				a.deleteSessionDialog.SetSessions(sessions)
				a.showDeleteSessionDialog = true
			}
			return a, nil
		case key.Matches(msg, keys.SwitchAgent):
			if a.currentPage == page.ChatPage && !a.showQuit && !a.showPermissions &&
				!a.showSessionDialog && !a.showDeleteSessionDialog && !a.showCommandDialog &&
				!a.showModelDialog && !a.showFilepicker && !a.showThemeDialog &&
				!a.showHelp && !a.showInitDialog && !a.showMultiArgumentsDialog &&
				!a.isCompacting && !a.app.ActiveAgent().IsBusy() &&
				!a.pageHasActiveOverlay() {
				agentName := a.app.SwitchAgent()
				return a, tea.Batch(
					util.CmdHandler(core.ActiveAgentChangedMsg{Name: agentName}),
					util.ReportInfo(fmt.Sprintf("Switched to %s", agentName)),
				)
			}
		case key.Matches(msg, keys.SwitchAgentBack):
			if a.currentPage == page.ChatPage && !a.showQuit && !a.showPermissions &&
				!a.showSessionDialog && !a.showDeleteSessionDialog && !a.showCommandDialog &&
				!a.showModelDialog && !a.showFilepicker && !a.showThemeDialog &&
				!a.showHelp && !a.showInitDialog && !a.showMultiArgumentsDialog &&
				!a.isCompacting && !a.app.ActiveAgent().IsBusy() &&
				!a.pageHasActiveOverlay() {
				agentName := a.app.SwitchAgentReverse()
				return a, tea.Batch(
					util.CmdHandler(core.ActiveAgentChangedMsg{Name: agentName}),
					util.ReportInfo(fmt.Sprintf("Switched to %s", agentName)),
				)
			}
		case key.Matches(msg, returnKey) || key.Matches(msg):
			if msg.String() == quitKey {
				if a.currentPage == page.LogsPage || a.currentPage == page.AgentsPage {
					return a, a.moveToPage(page.ChatPage)
				}
			} else if !a.filepicker.IsCWDFocused() {
				if a.showQuit {
					a.showQuit = !a.showQuit
					return a, nil
				}
				if a.showHelp {
					a.showHelp = !a.showHelp
					return a, nil
				}
				if a.showInitDialog {
					a.showInitDialog = false
					// Mark the project as initialized without running the command
					if err := config.MarkProjectInitialized(); err != nil {
						return a, util.ReportError(err)
					}
					return a, nil
				}
				if a.showFilepicker {
					a.showFilepicker = false
					a.filepicker.ToggleFilepicker(a.showFilepicker)
					return a, nil
				}
				if a.currentPage == page.LogsPage || a.currentPage == page.AgentsPage {
					return a, a.moveToPage(page.ChatPage)
				}
			}
		case key.Matches(msg, keys.Logs):
			return a, a.moveToPage(page.LogsPage)
		case key.Matches(msg, keys.Help):
			if a.showQuit {
				return a, nil
			}
			a.showHelp = !a.showHelp
			return a, nil
		case key.Matches(msg, helpEsc):
			if a.app.ActiveAgent().IsBusy() {
				if a.showQuit {
					return a, nil
				}
				a.showHelp = !a.showHelp
				return a, nil
			}
		case key.Matches(msg, keys.Filepicker):
			a.showFilepicker = !a.showFilepicker
			a.filepicker.ToggleFilepicker(a.showFilepicker)
			return a, nil
		}
	default:
	}

	if a.showFilepicker {
		f, filepickerCmd := a.filepicker.Update(msg)
		a.filepicker = f.(dialog.FilepickerCmp)
		cmds = append(cmds, filepickerCmd)
		// Only block key messages send all other messages down
		if _, ok := msg.(tea.KeyPressMsg); ok {
			return a, tea.Batch(cmds...)
		}
	}

	if a.showQuit {
		q, quitCmd := a.quit.Update(msg)
		a.quit = q.(dialog.QuitDialog)
		cmds = append(cmds, quitCmd)
		// Only block key messages send all other messages down
		if _, ok := msg.(tea.KeyPressMsg); ok {
			return a, tea.Batch(cmds...)
		}
	}
	if a.showPermissions {
		d, permissionsCmd := a.permissions.Update(msg)
		a.permissions = d.(dialog.PermissionDialogCmp)
		cmds = append(cmds, permissionsCmd)
		// Only block key messages send all other messages down
		if _, ok := msg.(tea.KeyPressMsg); ok {
			return a, tea.Batch(cmds...)
		}
	}

	if a.showSessionDialog {
		d, sessionCmd := a.sessionDialog.Update(msg)
		a.sessionDialog = d.(dialog.SessionDialog)
		cmds = append(cmds, sessionCmd)
		// Only block key messages send all other messages down
		if _, ok := msg.(tea.KeyPressMsg); ok {
			return a, tea.Batch(cmds...)
		}
	}

	if a.showDeleteSessionDialog {
		d, cmd := a.deleteSessionDialog.Update(msg)
		a.deleteSessionDialog = d.(dialog.SessionDialog)
		cmds = append(cmds, cmd)
		// block other tea.KeyPressMsgs
		if _, ok := msg.(tea.KeyPressMsg); ok {
			return a, tea.Batch(cmds...)
		}
	}

	if a.showCommandDialog {
		d, commandCmd := a.commandDialog.Update(msg)
		a.commandDialog = d.(dialog.CommandDialog)
		cmds = append(cmds, commandCmd)
		// Only block key messages send all other messages down
		if _, ok := msg.(tea.KeyPressMsg); ok {
			return a, tea.Batch(cmds...)
		}
	}

	if a.showModelDialog {
		d, modelCmd := a.modelDialog.Update(msg)
		a.modelDialog = d.(dialog.ModelDialog)
		cmds = append(cmds, modelCmd)
		// Only block key messages send all other messages down
		if _, ok := msg.(tea.KeyPressMsg); ok {
			return a, tea.Batch(cmds...)
		}
	}

	if a.showInitDialog {
		d, initCmd := a.initDialog.Update(msg)
		a.initDialog = d.(dialog.InitDialogCmp)
		cmds = append(cmds, initCmd)
		// Only block key messages send all other messages down
		if _, ok := msg.(tea.KeyPressMsg); ok {
			return a, tea.Batch(cmds...)
		}
	}

	if a.showThemeDialog {
		d, themeCmd := a.themeDialog.Update(msg)
		a.themeDialog = d.(dialog.ThemeDialog)
		cmds = append(cmds, themeCmd)
		// Only block key messages send all other messages down
		if _, ok := msg.(tea.KeyPressMsg); ok {
			return a, tea.Batch(cmds...)
		}
	}

	s, _ := a.status.Update(msg)
	a.status = s.(core.StatusCmp)
	a.pages[a.currentPage], cmd = a.pages[a.currentPage].Update(msg)
	cmds = append(cmds, cmd)
	return a, tea.Batch(cmds...)
}

func (a *appModel) pageHasActiveOverlay() bool {
	type overlayChecker interface {
		HasActiveOverlay() bool
	}
	if p, ok := a.pages[a.currentPage].(overlayChecker); ok {
		return p.HasActiveOverlay()
	}
	return false
}

func (a *appModel) pageIsShellMode() bool {
	type shellModeChecker interface {
		IsShellMode() bool
	}
	if p, ok := a.pages[a.currentPage].(shellModeChecker); ok {
		return p.IsShellMode()
	}
	return false
}

// RegisterCommand adds a command to the command dialog
func (a *appModel) RegisterCommand(cmd dialog.Command) {
	a.commands = append(a.commands, cmd)
}

func (a *appModel) findCommand(id string) (dialog.Command, bool) {
	for _, cmd := range a.commands {
		if cmd.ID == id {
			return cmd, true
		}
	}
	return dialog.Command{}, false
}

func (a *appModel) moveToPage(pageID page.PageID) tea.Cmd {
	if a.app.ActiveAgent().IsBusy() {
		// For now we don't move to any page if the agent is busy
		return util.ReportWarn("Agent is busy, please wait...")
	}

	var cmds []tea.Cmd
	if _, ok := a.loadedPages[pageID]; !ok {
		cmd := a.pages[pageID].Init()
		cmds = append(cmds, cmd)
		a.loadedPages[pageID] = true
	}
	a.previousPage = a.currentPage
	a.currentPage = pageID
	if sizable, ok := a.pages[a.currentPage].(layout.Sizeable); ok {
		cmd := sizable.SetSize(a.width, a.height)
		cmds = append(cmds, cmd)
	}
	if pageID == page.LogsPage {
		cmds = append(cmds, util.CmdHandler(logs.LogsPageActivatedMsg{}))
	}

	return tea.Batch(cmds...)
}

func (a appModel) View() tea.View {
	pageContent := lipgloss.NewStyle().
		MaxHeight(a.height).
		Render(a.pages[a.currentPage].View().Content)

	components := []string{
		pageContent,
	}

	components = append(components, a.status.View().Content)

	appView := lipgloss.JoinVertical(lipgloss.Top, components...)

	appViewHeight := lipgloss.Height(appView)
	appViewWidth := lipgloss.Width(appView)

	centerOverlay := func(overlay string) {
		row := appViewHeight/2 - lipgloss.Height(overlay)/2
		col := appViewWidth/2 - lipgloss.Width(overlay)/2
		appView = layout.PlaceOverlay(col, row, overlay, appView, true)
	}

	if a.showPermissions {
		centerOverlay(a.permissions.View().Content)
	}

	if a.showFilepicker {
		centerOverlay(a.filepicker.View().Content)
	}

	// Show compacting status overlay
	if a.isCompacting {
		t := theme.CurrentTheme()
		style := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(t.BorderFocused()).
			BorderBackground(t.Background()).
			Padding(1, 2).
			Background(t.Background()).
			Foreground(t.Text())

		centerOverlay(style.Render("Summarizing\n" + a.compactingMessage))
	}

	if a.showQuit {
		centerOverlay(a.quit.View().Content)
	}

	if a.showHelp {
		bindings := layout.KeyMapToSlice(keys)
		if p, ok := a.pages[a.currentPage].(layout.Bindings); ok {
			bindings = append(bindings, p.BindingKeys()...)
		}
		if a.showPermissions {
			bindings = append(bindings, a.permissions.BindingKeys()...)
		}
		if a.currentPage == page.LogsPage {
			bindings = append(bindings, logsKeyReturnKey)
		}
		if !a.app.ActiveAgent().IsBusy() {
			bindings = append(bindings, helpEsc)
		}
		a.help.SetBindings(bindings)

		centerOverlay(a.help.View().Content)
	}

	if a.showSessionDialog {
		centerOverlay(a.sessionDialog.View().Content)
	}

	if a.showDeleteSessionDialog {
		centerOverlay(a.deleteSessionDialog.View().Content)
	}

	if a.showModelDialog {
		centerOverlay(a.modelDialog.View().Content)
	}

	if a.showCommandDialog {
		centerOverlay(a.commandDialog.View().Content)
	}

	if a.showInitDialog {
		overlay := a.initDialog.View().Content
		appView = layout.PlaceOverlay(
			appViewWidth/2-lipgloss.Width(overlay)/2,
			appViewHeight/2-lipgloss.Height(overlay)/2,
			overlay,
			appView,
			true,
		)
	}

	if a.showThemeDialog {
		centerOverlay(a.themeDialog.View().Content)
	}

	if a.showMultiArgumentsDialog {
		centerOverlay(a.multiArgumentsDialog.View().Content)
	}

	v := tea.NewView(appView)
	v.AltScreen = true
	return v
}

func New(app *app.App) tea.Model {
	startPage := page.ChatPage

	// Build commands list before creating pages so chatPage can use it for slash completions
	commands := buildCommands()

	model := &appModel{
		currentPage:         startPage,
		loadedPages:         make(map[page.PageID]bool),
		status:              core.NewStatusCmp(app.LspService),
		help:                dialog.NewHelpCmp(),
		quit:                dialog.NewQuitCmp(),
		sessionDialog:       dialog.NewSessionDialogCmp(),
		deleteSessionDialog: dialog.NewSessionDialogCmp(),
		commandDialog:       dialog.NewCommandDialogCmp(),
		modelDialog:         dialog.NewModelDialogCmp(),
		permissions:         dialog.NewPermissionDialogCmp(),
		initDialog:          dialog.NewInitDialogCmp(),
		themeDialog:         dialog.NewThemeDialogCmp(),
		app:                 app,
		commands:            commands,
		pages: map[page.PageID]tea.Model{
			page.ChatPage:   page.NewChatPage(app, commands),
			page.LogsPage:   page.NewLogsPage(),
			page.AgentsPage: page.NewAgentsPage(app.Registry),
		},
		filepicker: dialog.NewFilepickerCmp(app),
	}

	return model
}

func buildCommands() []dialog.Command {
	initContent := readEmbeddedCommand("commands/init.md")
	reviewContent := readEmbeddedCommand("commands/review.md")
	commitContent := readEmbeddedCommand("commands/commit.md")

	commands := []dialog.Command{
		{
			ID:          "agents",
			Title:       "List Agents",
			Description: "List all available agents and their configuration",
			Handler: func(cmd dialog.Command) tea.Cmd {
				return util.CmdHandler(page.PageChangeMsg{ID: page.AgentsPage})
			},
		},
		{
			ID:          "init",
			Title:       "Initialize Project",
			Description: "Create/Update the AGENTS.md memory file",
			Content:     initContent,
			Handler: func(cmd dialog.Command) tea.Cmd {
				return tea.Batch(
					util.CmdHandler(chat.SendMsg{
						Text: initContent,
					}),
				)
			},
		},
		{
			ID:          "review",
			Title:       "Review code",
			Description: "Review a given work using provided commit hash or branch",
			Content:     reviewContent,
			Handler: func(cmd dialog.Command) tea.Cmd {
				return dialog.ParameterizedCommandHandler(reviewContent, &cmd)
			},
		},
		{
			ID:          "compact",
			Title:       "Compact Session",
			Description: "Summarize the current session and create a new one with the summary",
			Handler: func(cmd dialog.Command) tea.Cmd {
				return func() tea.Msg {
					return startCompactSessionMsg{}
				}
			},
		},
		{
			ID:          "commit",
			Title:       "Commit and Push",
			Description: "Commit changes to git using conventional commits and push",
			Content:     commitContent,
			Handler: func(cmd dialog.Command) tea.Cmd {
				return tea.Batch(
					util.CmdHandler(chat.SendMsg{
						Text: commitContent,
					}),
				)
			},
		},
		{
			ID:          "auto-approve",
			Title:       "Toggle Auto-Approve",
			Description: "Toggle auto-approve mode for the current session (skip permission dialogs)",
			Handler: func(cmd dialog.Command) tea.Cmd {
				return func() tea.Msg {
					return toggleAutoApproveMsg{}
				}
			},
		},
	}

	customCommands, err := dialog.LoadCustomCommands()
	if err != nil {
		logging.Warn("Failed to load custom commands", "error", err)
	} else {
		commands = append(commands, customCommands...)
	}

	return commands
}

func readEmbeddedCommand(path string) string {
	data, err := dialog.CommandPrompts.ReadFile(path)
	if err != nil {
		logging.Error("Failed to read embedded command", "path", path, "error", err)
		return ""
	}
	return string(data)
}
