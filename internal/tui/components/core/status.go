package core

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/lsp"
	"github.com/opencode-ai/opencode/internal/lsp/protocol"
	"github.com/opencode-ai/opencode/internal/pubsub"
	"github.com/opencode-ai/opencode/internal/session"
	"github.com/opencode-ai/opencode/internal/tui/components/chat"
	"github.com/opencode-ai/opencode/internal/tui/styles"
	"github.com/opencode-ai/opencode/internal/tui/theme"
	"github.com/opencode-ai/opencode/internal/tui/util"
)

type StatusCmp interface {
	tea.Model
}

type ActiveAgentChangedMsg struct {
	Name config.AgentName
}

type statusCmp struct {
	info            util.InfoMsg
	width           int
	messageTTL      time.Duration
	lspService      lsp.LspService
	session         session.Session
	activeAgentName config.AgentName
}

// clearMessageCmd is a command that clears status messages after a timeout
func (m statusCmp) clearMessageCmd(ttl time.Duration) tea.Cmd {
	return tea.Tick(ttl, func(time.Time) tea.Msg {
		return util.ClearStatusMsg{}
	})
}

func (m statusCmp) Init() tea.Cmd {
	return nil
}

func (m statusCmp) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		return m, nil
	case chat.SessionSelectedMsg:
		m.session = msg
	case chat.SessionClearedMsg:
		m.session = session.Session{}
	case pubsub.Event[session.Session]:
		if msg.Type == pubsub.UpdatedEvent {
			if m.session.ID == msg.Payload.ID {
				m.session = msg.Payload
			}
		}
	case util.InfoMsg:
		m.info = msg
		ttl := msg.TTL
		if ttl == 0 {
			ttl = m.messageTTL
		}
		return m, m.clearMessageCmd(ttl)
	case util.ClearStatusMsg:
		m.info = util.InfoMsg{}
	case ActiveAgentChangedMsg:
		m.activeAgentName = msg.Name
	}
	return m, nil
}

var helpWidget = ""
var agentHintWidget = ""

// getHelpWidget returns the help widget with current theme colors
func getHelpWidget() string {
	t := theme.CurrentTheme()
	helpText := "ctrl+h help"

	return styles.Padded().
		Background(t.TextMuted()).
		Foreground(t.BackgroundDarker()).
		Bold(true).
		Render(helpText)
}

func getAgentHintWidget() string {
	reg := agentregistry.GetRegistry()
	primaryAgents := reg.ListByMode(config.AgentModeAgent)
	if len(primaryAgents) <= 1 {
		return ""
	}

	t := theme.CurrentTheme()
	return styles.Padded().
		Background(t.TextMuted()).
		Foreground(t.BackgroundDarker()).
		Bold(true).
		Render("tab agents")
}

func formatTokensAndCost(tokens, contextWindow int64, cost float64) string {
	// Format tokens in human-readable format (e.g., 110K, 1.2M)
	var formattedTokens string
	switch {
	case tokens >= 1_000_000:
		formattedTokens = fmt.Sprintf("%.1fM", float64(tokens)/1_000_000)
	case tokens >= 1_000:
		formattedTokens = fmt.Sprintf("%.1fK", float64(tokens)/1_000)
	default:
		formattedTokens = fmt.Sprintf("%d", tokens)
	}

	// Remove .0 suffix if present
	if strings.HasSuffix(formattedTokens, ".0K") {
		formattedTokens = strings.Replace(formattedTokens, ".0K", "K", 1)
	}
	if strings.HasSuffix(formattedTokens, ".0M") {
		formattedTokens = strings.Replace(formattedTokens, ".0M", "M", 1)
	}

	// Format cost with $ symbol and 2 decimal places
	formattedCost := fmt.Sprintf("$%.2f", cost)

	if contextWindow <= 0 {
		return fmt.Sprintf("Context: %s, Cost: %s", formattedTokens, formattedCost)
	}
	percentage := (float64(tokens) / float64(contextWindow)) * 100
	if percentage > 80 {
		// add the warning icon and percentage
		formattedTokens = fmt.Sprintf("%s(%d%%)", styles.WarningIcon, int(percentage))
	}

	return fmt.Sprintf("Context: %s, Cost: %s", formattedTokens, formattedCost)
}

func (m statusCmp) resolveModel() models.Model {
	agentName := m.activeAgentName
	if agentCfg, ok := config.Get().Agents[agentName]; ok {
		if model, ok := models.SupportedModels[agentCfg.Model]; ok {
			return model
		}
	}
	reg := agentregistry.GetRegistry()
	if info, ok := reg.Get(agentName); ok && info.Model != "" {
		if model, ok := models.SupportedModels[models.ModelID(info.Model)]; ok {
			return model
		}
	}
	return models.Model{}
}

func (m statusCmp) View() string {
	t := theme.CurrentTheme()
	model := m.resolveModel()
	modelWidget := m.renderModelWidget(model)

	status := getHelpWidget()
	agentHint := getAgentHintWidget()
	status += agentHint

	tokenInfoWidth := 0
	if m.session.ID != "" {
		totalTokens := m.session.PromptTokens + m.session.CompletionTokens
		tokens := formatTokensAndCost(totalTokens, model.ContextWindow, m.session.Cost)
		tokensStyle := styles.Padded().
			Background(t.Text()).
			Foreground(t.BackgroundSecondary())
		if model.ContextWindow > 0 {
			percentage := (float64(totalTokens) / float64(model.ContextWindow)) * 100
			if percentage > 80 {
				tokensStyle = tokensStyle.Background(t.Warning())
			}
		}
		tokenInfoWidth = lipgloss.Width(tokens) + 2
		status += tokensStyle.Render(tokens)
	}

	diagnostics := styles.Padded().
		Background(t.BackgroundDarker()).
		Render(m.projectDiagnostics())

	availableWidht := max(0, m.width-lipgloss.Width(helpWidget)-lipgloss.Width(agentHintWidget)-lipgloss.Width(modelWidget)-lipgloss.Width(diagnostics)-tokenInfoWidth)

	if m.info.Msg != "" {
		infoStyle := styles.Padded().
			Foreground(t.Background()).
			Width(availableWidht)

		switch m.info.Type {
		case util.InfoTypeInfo:
			infoStyle = infoStyle.Background(t.Info())
		case util.InfoTypeWarn:
			infoStyle = infoStyle.Background(t.Warning())
		case util.InfoTypeError:
			infoStyle = infoStyle.Background(t.Error())
		}

		infoWidth := availableWidht - 10
		msg := m.info.Msg
		if len(msg) > infoWidth && infoWidth > 0 {
			msg = msg[:infoWidth] + "..."
		}
		status += infoStyle.Render(msg)
	} else {
		status += styles.Padded().
			Foreground(t.Text()).
			Background(t.BackgroundSecondary()).
			Width(availableWidht).
			Render("")
	}

	status += diagnostics
	status += modelWidget
	return status
}

func (m *statusCmp) projectDiagnostics() string {
	t := theme.CurrentTheme()

	// Check if any LSP server is still initializing
	initializing := false
	for _, client := range m.lspService.Clients() {
		if client.GetServerState() == lsp.StateStarting {
			initializing = true
			break
		}
	}

	// If any server is initializing, show that status
	if initializing {
		return lipgloss.NewStyle().
			Background(t.BackgroundDarker()).
			Foreground(t.Warning()).
			Render(fmt.Sprintf("%s Initializing LSP...", styles.SpinnerIcon))
	}

	errorDiagnostics := []protocol.Diagnostic{}
	warnDiagnostics := []protocol.Diagnostic{}
	hintDiagnostics := []protocol.Diagnostic{}
	infoDiagnostics := []protocol.Diagnostic{}
	for _, client := range m.lspService.Clients() {
		for _, d := range client.GetDiagnostics() {
			for _, diag := range d {
				switch diag.Severity {
				case protocol.SeverityError:
					errorDiagnostics = append(errorDiagnostics, diag)
				case protocol.SeverityWarning:
					warnDiagnostics = append(warnDiagnostics, diag)
				case protocol.SeverityHint:
					hintDiagnostics = append(hintDiagnostics, diag)
				case protocol.SeverityInformation:
					infoDiagnostics = append(infoDiagnostics, diag)
				}
			}
		}
	}

	if len(errorDiagnostics) == 0 && len(warnDiagnostics) == 0 && len(hintDiagnostics) == 0 && len(infoDiagnostics) == 0 {
		return "No diagnostics"
	}

	diagnostics := []string{}

	if len(errorDiagnostics) > 0 {
		errStr := lipgloss.NewStyle().
			Background(t.BackgroundDarker()).
			Foreground(t.Error()).
			Render(fmt.Sprintf("%s %d", styles.ErrorIcon, len(errorDiagnostics)))
		diagnostics = append(diagnostics, errStr)
	}
	if len(warnDiagnostics) > 0 {
		warnStr := lipgloss.NewStyle().
			Background(t.BackgroundDarker()).
			Foreground(t.Warning()).
			Render(fmt.Sprintf("%s %d", styles.WarningIcon, len(warnDiagnostics)))
		diagnostics = append(diagnostics, warnStr)
	}
	if len(hintDiagnostics) > 0 {
		hintStr := lipgloss.NewStyle().
			Background(t.BackgroundDarker()).
			Foreground(t.Text()).
			Render(fmt.Sprintf("%s %d", styles.HintIcon, len(hintDiagnostics)))
		diagnostics = append(diagnostics, hintStr)
	}
	if len(infoDiagnostics) > 0 {
		infoStr := lipgloss.NewStyle().
			Background(t.BackgroundDarker()).
			Foreground(t.Info()).
			Render(fmt.Sprintf("%s %d", styles.InfoIcon, len(infoDiagnostics)))
		diagnostics = append(diagnostics, infoStr)
	}
	return strings.Join(
		diagnostics,
		lipgloss.NewStyle().
			Background(t.BackgroundDarker()).
			Foreground(t.Text()).
			Render(" "),
	)
}

func (m statusCmp) renderModelWidget(model models.Model) string {
	t := theme.CurrentTheme()

	reg := agentregistry.GetRegistry()
	primaryAgents := reg.ListByMode(config.AgentModeAgent)

	agentLabel := ""
	if len(primaryAgents) > 1 {
		agentName := m.activeAgentName
		name := ""
		if agentCfg, ok := config.Get().Agents[agentName]; ok && agentCfg.Name != "" {
			name = agentCfg.Name
		}
		if name == "" {
			if info, found := reg.Get(agentName); found {
				name = info.Name
			}
		}
		if name == "" {
			name = string(agentName)
		}
		agentLabel = " ▶ " + name
	}

	return styles.Padded().
		Background(t.Secondary()).
		Foreground(t.Background()).
		Render(model.Name + agentLabel)
}

func NewStatusCmp(lspService lsp.LspService) StatusCmp {
	helpWidget = getHelpWidget()
	agentHintWidget = getAgentHintWidget()

	return &statusCmp{
		messageTTL:      10 * time.Second,
		lspService:      lspService,
		activeAgentName: config.AgentCoder,
	}
}
