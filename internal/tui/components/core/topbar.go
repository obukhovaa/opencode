package core

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/pubsub"
	"github.com/opencode-ai/opencode/internal/session"
	"github.com/opencode-ai/opencode/internal/tui/components/chat"
	"github.com/opencode-ai/opencode/internal/tui/components/dialog"
	"github.com/opencode-ai/opencode/internal/tui/styles"
	"github.com/opencode-ai/opencode/internal/tui/theme"
)

type TopBarCmp interface {
	tea.Model
}

type topbarCmp struct {
	width   int
	session session.Session
}

func (m *topbarCmp) Init() tea.Cmd {
	return nil
}

func (m *topbarCmp) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
	case chat.SessionSelectedMsg:
		m.session = msg
	case chat.SessionClearedMsg:
		m.session = session.Session{}
	case ActiveAgentChangedMsg:
		// re-render on agent change
	case pubsub.Event[session.Session]:
		if msg.Type == pubsub.UpdatedEvent && m.session.ID == msg.Payload.ID {
			m.session = msg.Payload
		}
	case dialog.ThemeChangedMsg:
		// re-render on theme change
	}
	return m, nil
}

func (m *topbarCmp) View() tea.View {
	t := theme.CurrentTheme()
	cfg := config.Get()

	// Project name widget (right)
	projectID := db.GetProjectID(cfg.WorkingDir)
	projectWidget := styles.Padded().
		Background(t.TextMuted()).
		Foreground(t.BackgroundDarker()).
		Bold(true).
		Render(projectID)
	projectWidth := lipgloss.Width(projectWidget)

	// Provider widget (always visible)
	providerWidget := ""
	providerWidth := 0
	providerInfo := sessionProviderLabel(cfg)
	if providerInfo != "" {
		providerWidget = lipgloss.NewStyle().
			Background(t.BackgroundSecondary()).
			Foreground(t.TextMuted()).
			PaddingRight(1).
			PaddingLeft(1).
			Render(providerInfo)
		providerWidth = lipgloss.Width(providerWidget)
	}

	// Session widget (docked right, before provider/project)
	sessionWidget := ""
	sessionWidth := 0
	if m.session.ID != "" {
		sessionText := m.session.Title
		if sessionText == "" {
			sessionText = "New Session"
		}

		maxSessionWidth := m.width / 3
		sessionText = ansi.Truncate(sessionText, maxSessionWidth, "…")
		sessionWidget = lipgloss.NewStyle().
			Background(t.Background()).
			Foreground(t.TextMuted()).
			PaddingRight(1).
			Render(sessionText)
		sessionWidth = lipgloss.Width(sessionWidget)
	}

	// Left padding matches the messages container padding (1 col)
	leftPad := lipgloss.NewStyle().
		Background(t.Background()).
		Width(1).
		Render("")

	// Corner aligns with the message border
	corner := lipgloss.NewStyle().
		Background(t.Background()).
		Foreground(t.TextMuted()).
		Render("┏")

	// Line fills from the corner to the right-aligned widgets
	lineWidth := max(0, m.width-1-1-sessionWidth-providerWidth-projectWidth)
	lineText := ""
	if lineWidth > 2 {
		lineText = strings.Repeat("━", lineWidth-2)
	}
	lineText += "○ "
	line := lipgloss.NewStyle().
		Background(t.Background()).
		Foreground(t.TextMuted()).
		Width(lineWidth).
		Render(lineText)

	bar := leftPad + corner + line + sessionWidget + providerWidget + projectWidget

	return tea.NewView(bar)
}

func NewTopBarCmp() TopBarCmp {
	return &topbarCmp{}
}

func sessionProviderLabel(cfg *config.Config) string {
	if cfg.SessionProvider.Type != config.ProviderMySQL {
		return "⛁ (local)"
	}
	if cfg.SessionProvider.MySQL.Host != "" {
		return fmt.Sprintf("⛁ (%s)", cfg.SessionProvider.MySQL.Host)
	}
	if cfg.SessionProvider.MySQL.DSN != "" {
		dsn := cfg.SessionProvider.MySQL.DSN
		if idx := strings.Index(dsn, "@tcp("); idx != -1 {
			hostPart := dsn[idx+5:]
			if endIdx := strings.Index(hostPart, ")"); endIdx != -1 {
				host := hostPart[:endIdx]
				if colonIdx := strings.Index(host, ":"); colonIdx != -1 {
					host = host[:colonIdx]
				}
				return fmt.Sprintf("⛁ (%s)", host)
			}
		}
		return "⛁ (remote)"
	}
	return "⛁ (remote)"
}
