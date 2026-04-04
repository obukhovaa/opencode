package chat

import (
	"fmt"
	"sort"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/lsp"
	"github.com/opencode-ai/opencode/internal/lsp/install"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/session"
	"github.com/opencode-ai/opencode/internal/tui/styles"
	"github.com/opencode-ai/opencode/internal/tui/theme"
	"github.com/opencode-ai/opencode/internal/version"
)

type SendMsg struct {
	Text        string
	Attachments []message.Attachment
}

type SessionSelectedMsg = session.Session

type SessionClearedMsg struct{}

type EditorFocusMsg bool

type ShellExecMsg struct {
	Command string
}

type ShellResultMsg struct {
	Command  string
	Stdout   string
	Stderr   string
	ExitCode int
	Err      error
}

type ShellModeChangedMsg struct {
	ShellMode bool
}

type ScrollStateMsg struct {
	Locked      bool
	NewMessages int
}

type AgentChangedMsg struct {
	Name config.AgentName
}

func header(width int) string {
	return lipgloss.JoinVertical(
		lipgloss.Top,
		logo(width),
		"",
		cwd(width),
	)
}

var cachedLspsConfigured struct {
	content string
	width   int
	themeID string
}

func InvalidateLspCache() {
	cachedLspsConfigured = struct {
		content string
		width   int
		themeID string
	}{}
}

func lspsConfigured(width int, a *app.App) string {
	cfg := config.Get()
	if len(cfg.LSP) == 0 {
		return ""
	}

	themeID := theme.CurrentThemeName()
	if cachedLspsConfigured.width == width && cachedLspsConfigured.themeID == themeID &&
		cachedLspsConfigured.content != "" {
		return cachedLspsConfigured.content
	}

	t := theme.CurrentTheme()
	baseStyle := styles.BaseStyle()

	title := baseStyle.
		Width(width).
		Foreground(t.Primary()).
		Bold(true).
		Render("LSP")

	// Get actually active LSP clients
	activeClients := make(map[string]lsp.ServerState)
	if a != nil {
		for name, client := range a.LspService.Clients() {
			activeClients[name] = client.GetServerState()
		}
	}

	// Get configured servers for display info
	servers := install.ResolveServers(cfg)

	var lspNames []string
	for name := range servers {
		lspNames = append(lspNames, name)
	}
	sort.Strings(lspNames)

	var lspViews []string
	for _, name := range lspNames {
		server := servers[name]

		indicator := "○"
		indicatorColor := t.TextMuted()
		if state, ok := activeClients[name]; ok {
			switch state {
			case lsp.StateReady:
				indicator = "●"
				indicatorColor = t.Success()
			case lsp.StateError:
				indicator = "●"
				indicatorColor = t.Error()
			}
		}

		indicatorStr := baseStyle.
			Foreground(indicatorColor).
			Render(indicator)

		lspName := baseStyle.
			Foreground(t.Text()).
			Render(" " + name)

		cmd := ""
		if len(server.Command) > 0 {
			cmd = server.Command[0]
		}
		cmd = ansi.Truncate(cmd, width-lipgloss.Width(indicatorStr)-lipgloss.Width(lspName)-4, "…")

		lspPath := baseStyle.
			Foreground(t.TextMuted()).
			Render(fmt.Sprintf(" (%s)", cmd))

		lspViews = append(lspViews,
			baseStyle.
				Width(width).
				Render(
					lipgloss.JoinHorizontal(
						lipgloss.Left,
						indicatorStr,
						lspName,
						lspPath,
					),
				),
		)
	}

	// Only cache when all servers have settled (no longer starting)
	allSettled := true
	for _, state := range activeClients {
		if state == lsp.StateStarting {
			allSettled = false
			break
		}
	}

	result := baseStyle.
		Width(width).
		Render(
			lipgloss.JoinVertical(
				lipgloss.Left,
				title,
				lipgloss.JoinVertical(
					lipgloss.Left,
					lspViews...,
				),
			),
		)
	if allSettled {
		cachedLspsConfigured.content = result
		cachedLspsConfigured.width = width
		cachedLspsConfigured.themeID = themeID
	}
	return result
}

func logo(width int) string {
	t := theme.CurrentTheme()
	baseStyle := styles.BaseStyle()

	logo := baseStyle.
		Foreground(t.TextMuted()).
		Render(fmt.Sprintf("%s %s ", styles.OpenCodeIcon, "OpenCode"))

	versionText := baseStyle.
		Foreground(t.TextMuted()).
		Render(version.Version)

	return baseStyle.
		Bold(true).
		Width(width).
		Render(
			lipgloss.JoinHorizontal(
				lipgloss.Left,
				logo,
				versionText,
			),
		)
}

func cwd(width int) string {
	cwd := fmt.Sprintf("cwd: %s", config.WorkingDirectory())
	t := theme.CurrentTheme()

	return styles.BaseStyle().
		Foreground(t.TextMuted()).
		Width(width).
		Render(cwd)
}

var cachedMcpServers struct {
	content   string
	width     int
	themeID   string
	agentName string
	resolved  bool
}

func InvalidateMcpCache() {
	cachedMcpServers = struct {
		content   string
		width     int
		themeID   string
		agentName string
		resolved  bool
	}{}
}

func mcpServersConfigured(width int, a *app.App) string {
	mcpServers := config.ResolveMCPServers()
	if len(mcpServers) == 0 {
		return ""
	}

	themeID := theme.CurrentThemeName()
	agentName := ""
	toolsResolved := false
	if a != nil {
		agentName = a.ActiveAgentName()
		_, toolsResolved = a.ActiveAgent().ResolvedTools()
	}

	if cachedMcpServers.width == width && cachedMcpServers.themeID == themeID &&
		cachedMcpServers.agentName == agentName && cachedMcpServers.content != "" &&
		cachedMcpServers.resolved == toolsResolved {
		return cachedMcpServers.content
	}

	t := theme.CurrentTheme()
	baseStyle := styles.BaseStyle()

	title := baseStyle.
		Width(width).
		Foreground(t.Primary()).
		Bold(true).
		Render("MCP")

	// Build set of active MCP server names by checking agent tools
	activeServers := make(map[string]bool)
	if toolsResolved && a != nil {
		resolvedTools, _ := a.ActiveAgent().ResolvedTools()
		for _, tool := range resolvedTools {
			toolName := tool.Info().Name
			bestMatch := ""
			for name := range mcpServers {
				if strings.HasPrefix(toolName, name+"_") && len(name) > len(bestMatch) {
					bestMatch = name
				}
			}
			if bestMatch != "" {
				activeServers[bestMatch] = true
			}
		}
	}

	// Get sorted server names
	var serverNames []string
	for name := range mcpServers {
		serverNames = append(serverNames, name)
	}
	sort.Strings(serverNames)

	var serverViews []string
	for _, name := range serverNames {
		server := mcpServers[name]

		indicator := "○"
		indicatorColor := t.TextMuted()
		if toolsResolved && activeServers[name] {
			indicator = "●"
			indicatorColor = t.Success()
		}

		indicatorStr := baseStyle.
			Foreground(indicatorColor).
			Render(indicator)

		serverName := baseStyle.
			Foreground(t.Text()).
			Render(" " + name)

		detail := server.Command
		if detail == "" && server.URL != "" {
			detail = server.URL
		}
		detail = ansi.Truncate(detail, width-lipgloss.Width(indicatorStr)-lipgloss.Width(serverName)-4, "…")

		serverDetail := baseStyle.
			Foreground(t.TextMuted()).
			Render(fmt.Sprintf(" (%s)", detail))

		serverViews = append(serverViews,
			baseStyle.
				Width(width).
				Render(
					lipgloss.JoinHorizontal(
						lipgloss.Left,
						indicatorStr,
						serverName,
						serverDetail,
					),
				),
		)
	}

	result := baseStyle.
		Width(width).
		Render(
			lipgloss.JoinVertical(
				lipgloss.Left,
				title,
				lipgloss.JoinVertical(
					lipgloss.Left,
					serverViews...,
				),
			),
		)

	if toolsResolved {
		cachedMcpServers.content = result
		cachedMcpServers.width = width
		cachedMcpServers.themeID = themeID
		cachedMcpServers.agentName = agentName
		cachedMcpServers.resolved = toolsResolved
	}
	return result
}
