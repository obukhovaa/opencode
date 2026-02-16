package chat

import (
	"context"
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/diff"
	"github.com/opencode-ai/opencode/internal/history"
	"github.com/opencode-ai/opencode/internal/pubsub"
	"github.com/opencode-ai/opencode/internal/session"
	"github.com/opencode-ai/opencode/internal/tui/styles"
	"github.com/opencode-ai/opencode/internal/tui/theme"
)

type sidebarCmp struct {
	width, height int
	session       session.Session
	sessions      session.Service
	history       history.Service
	modFiles      map[string]struct {
		additions int
		removals  int
	}
	childSessionIDs map[string]bool
	filesCh         <-chan pubsub.Event[history.File]
}

func (m *sidebarCmp) waitForFileEvent() tea.Cmd {
	if m.filesCh == nil {
		return nil
	}
	return func() tea.Msg {
		msg, ok := <-m.filesCh
		if !ok {
			return nil
		}
		return msg
	}
}

func (m *sidebarCmp) Init() tea.Cmd {
	if m.history != nil {
		ctx := context.Background()
		m.filesCh = m.history.Subscribe(ctx)

		m.modFiles = make(map[string]struct {
			additions int
			removals  int
		})

		m.loadModifiedFiles(ctx)

		return m.waitForFileEvent()
	}
	return nil
}

func (m *sidebarCmp) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case SessionSelectedMsg:
		if msg.ID != m.session.ID {
			m.session = msg
			ctx := context.Background()
			m.loadModifiedFiles(ctx)
		}
	case pubsub.Event[session.Session]:
		if msg.Type == pubsub.UpdatedEvent {
			if m.session.ID == msg.Payload.ID {
				m.session = msg.Payload
			}
		}
		if msg.Type == pubsub.CreatedEvent {
			if msg.Payload.RootSessionID == m.session.RootSessionID || msg.Payload.ParentSessionID == m.session.ID {
				if m.childSessionIDs == nil {
					m.childSessionIDs = make(map[string]bool)
					m.childSessionIDs[m.session.ID] = true
				}
				m.childSessionIDs[msg.Payload.ID] = true
			}
		}
	case pubsub.Event[history.File]:
		if m.isInSessionTree(msg.Payload.SessionID) {
			ctx := context.Background()
			m.processFileChanges(ctx, msg.Payload)
		}
		return m, m.waitForFileEvent()
	}
	return m, nil
}

func (m *sidebarCmp) View() string {
	baseStyle := styles.BaseStyle()

	return baseStyle.
		Width(m.width).
		PaddingLeft(4).
		PaddingRight(2).
		Height(m.height - 1).
		Render(
			lipgloss.JoinVertical(
				lipgloss.Top,
				header(m.width),
				" ",
				m.projectSection(),
				" ",
				m.sessionSection(),
				" ",
				lspsConfigured(m.width),
				" ",
				m.modifiedFiles(),
			),
		)
}

func (m *sidebarCmp) projectSection() string {
	t := theme.CurrentTheme()
	baseStyle := styles.BaseStyle()
	cfg := config.Get()

	projectID := db.GetProjectID(cfg.WorkingDir)

	projectKey := baseStyle.
		Foreground(t.Primary()).
		Bold(true).
		Render("Project")

	projectValue := baseStyle.
		Foreground(t.Text()).
		Width(m.width - lipgloss.Width(projectKey)).
		Render(fmt.Sprintf(": %s", projectID))

	return lipgloss.JoinHorizontal(
		lipgloss.Left,
		projectKey,
		projectValue,
	)
}

func (m *sidebarCmp) sessionSection() string {
	t := theme.CurrentTheme()
	baseStyle := styles.BaseStyle()
	cfg := config.Get()

	providerInfo := "local"
	if cfg.SessionProvider.Type == config.ProviderMySQL {
		if cfg.SessionProvider.MySQL.Host != "" {
			providerInfo = fmt.Sprintf("remote (%s)", cfg.SessionProvider.MySQL.Host)
		} else if cfg.SessionProvider.MySQL.DSN != "" {
			dsn := cfg.SessionProvider.MySQL.DSN
			if idx := strings.Index(dsn, "@tcp("); idx != -1 {
				hostPart := dsn[idx+5:]
				if endIdx := strings.Index(hostPart, ")"); endIdx != -1 {
					host := hostPart[:endIdx]
					if colonIdx := strings.Index(host, ":"); colonIdx != -1 {
						host = host[:colonIdx]
					}
					providerInfo = fmt.Sprintf("remote (%s)", host)
				} else {
					providerInfo = "remote"
				}
			} else {
				providerInfo = "remote"
			}
		} else {
			providerInfo = "remote"
		}
	}

	sessionKey := baseStyle.
		Foreground(t.Primary()).
		Bold(true).
		Render("Session")

	provider := baseStyle.
		Foreground(t.TextMuted()).
		Render(fmt.Sprintf(" [%s]", providerInfo))

	sessionValue := baseStyle.
		Foreground(t.Text()).
		Render(fmt.Sprintf(": %s", m.session.Title))

	sessionView := baseStyle.
		Width(m.width - lipgloss.Width(sessionKey)).
		Render(
			lipgloss.JoinHorizontal(
				lipgloss.Left,
				sessionValue,
				provider,
			),
		)

	return lipgloss.JoinHorizontal(
		lipgloss.Left,
		sessionKey,
		sessionView,
	)
}

func (m *sidebarCmp) modifiedFile(filePath string, additions, removals int) string {
	t := theme.CurrentTheme()
	baseStyle := styles.BaseStyle()

	stats := ""
	if additions > 0 && removals > 0 {
		additionsStr := baseStyle.
			Foreground(t.Success()).
			PaddingLeft(1).
			Render(fmt.Sprintf("+%d", additions))

		removalsStr := baseStyle.
			Foreground(t.Error()).
			PaddingLeft(1).
			Render(fmt.Sprintf("-%d", removals))

		content := lipgloss.JoinHorizontal(lipgloss.Left, additionsStr, removalsStr)
		stats = baseStyle.Width(lipgloss.Width(content)).Render(content)
	} else if additions > 0 {
		additionsStr := fmt.Sprintf(" %s", baseStyle.
			PaddingLeft(1).
			Foreground(t.Success()).
			Render(fmt.Sprintf("+%d", additions)))
		stats = baseStyle.Width(lipgloss.Width(additionsStr)).Render(additionsStr)
	} else if removals > 0 {
		removalsStr := fmt.Sprintf(" %s", baseStyle.
			PaddingLeft(1).
			Foreground(t.Error()).
			Render(fmt.Sprintf("-%d", removals)))
		stats = baseStyle.Width(lipgloss.Width(removalsStr)).Render(removalsStr)
	}

	filePathStr := baseStyle.Render(filePath)

	return baseStyle.
		Width(m.width).
		Render(
			lipgloss.JoinHorizontal(
				lipgloss.Left,
				filePathStr,
				stats,
			),
		)
}

func (m *sidebarCmp) modifiedFiles() string {
	t := theme.CurrentTheme()
	baseStyle := styles.BaseStyle()

	modifiedFiles := baseStyle.
		Width(m.width).
		Foreground(t.Primary()).
		Bold(true).
		Render("Modified Files:")

	if len(m.modFiles) == 0 {
		message := "No modified files"
		remainingWidth := m.width - lipgloss.Width(message)
		if remainingWidth > 0 {
			message += strings.Repeat(" ", remainingWidth)
		}
		return baseStyle.
			Width(m.width).
			Render(
				lipgloss.JoinVertical(
					lipgloss.Top,
					modifiedFiles,
					baseStyle.Foreground(t.TextMuted()).Render(message),
				),
			)
	}

	var paths []string
	for path := range m.modFiles {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	var fileViews []string
	for _, path := range paths {
		stats := m.modFiles[path]
		fileViews = append(fileViews, m.modifiedFile(path, stats.additions, stats.removals))
	}

	return baseStyle.
		Width(m.width).
		Render(
			lipgloss.JoinVertical(
				lipgloss.Top,
				modifiedFiles,
				lipgloss.JoinVertical(
					lipgloss.Left,
					fileViews...,
				),
			),
		)
}

func (m *sidebarCmp) SetSize(width, height int) tea.Cmd {
	m.width = width
	m.height = height
	return nil
}

func (m *sidebarCmp) GetSize() (int, int) {
	return m.width, m.height
}

func NewSidebarCmp(s session.Session, sessions session.Service, history history.Service) tea.Model {
	return &sidebarCmp{
		session:  s,
		sessions: sessions,
		history:  history,
	}
}

func (m *sidebarCmp) loadModifiedFiles(ctx context.Context) {
	if m.history == nil || m.session.ID == "" {
		return
	}

	rootSessionID := m.session.RootSessionID
	if rootSessionID == "" {
		rootSessionID = m.session.ID
	}

	m.buildChildSessionCache(ctx, rootSessionID)

	latestFiles, err := m.history.ListLatestSessionTreeFiles(ctx, rootSessionID)
	if err != nil || len(latestFiles) == 0 {
		latestFiles, err = m.history.ListLatestSessionFiles(ctx, m.session.ID)
		if err != nil {
			return
		}
	}

	allFiles, err := m.history.ListBySessionTree(ctx, rootSessionID)
	if err != nil || len(allFiles) == 0 {
		allFiles, err = m.history.ListBySession(ctx, m.session.ID)
		if err != nil {
			return
		}
	}

	m.modFiles = make(map[string]struct {
		additions int
		removals  int
	})

	for _, file := range latestFiles {
		if file.Version == history.InitialVersion {
			continue
		}

		var initialVersion history.File
		for _, v := range allFiles {
			if v.Path == file.Path && v.Version == history.InitialVersion {
				initialVersion = v
				break
			}
		}

		if initialVersion.ID == "" {
			continue
		}
		if initialVersion.Content == file.Content {
			continue
		}

		_, additions, removals := diff.GenerateDiff(initialVersion.Content, file.Content, file.Path)

		if additions > 0 || removals > 0 {
			displayPath := getDisplayPath(file.Path)
			m.modFiles[displayPath] = struct {
				additions int
				removals  int
			}{
				additions: additions,
				removals:  removals,
			}
		}
	}
}

func (m *sidebarCmp) buildChildSessionCache(ctx context.Context, rootSessionID string) {
	m.childSessionIDs = make(map[string]bool)
	m.childSessionIDs[m.session.ID] = true

	if m.sessions == nil {
		return
	}

	children, err := m.sessions.ListChildren(ctx, rootSessionID)
	if err != nil {
		return
	}
	for _, child := range children {
		m.childSessionIDs[child.ID] = true
	}
}

func (m *sidebarCmp) isInSessionTree(sessionID string) bool {
	if m.childSessionIDs == nil {
		return sessionID == m.session.ID
	}
	return m.childSessionIDs[sessionID]
}

func (m *sidebarCmp) processFileChanges(ctx context.Context, file history.File) {
	if file.Version == history.InitialVersion {
		return
	}

	initialVersion, err := m.findInitialVersion(ctx, file.Path)
	if err != nil || initialVersion.ID == "" {
		return
	}

	if initialVersion.Content == file.Content {
		displayPath := getDisplayPath(file.Path)
		delete(m.modFiles, displayPath)
		return
	}

	_, additions, removals := diff.GenerateDiff(initialVersion.Content, file.Content, file.Path)

	if additions > 0 || removals > 0 {
		displayPath := getDisplayPath(file.Path)
		m.modFiles[displayPath] = struct {
			additions int
			removals  int
		}{
			additions: additions,
			removals:  removals,
		}
	} else {
		displayPath := getDisplayPath(file.Path)
		delete(m.modFiles, displayPath)
	}
}

func (m *sidebarCmp) findInitialVersion(ctx context.Context, path string) (history.File, error) {
	rootSessionID := m.session.RootSessionID
	if rootSessionID == "" {
		rootSessionID = m.session.ID
	}

	fileVersions, err := m.history.ListBySessionTree(ctx, rootSessionID)
	if err != nil || len(fileVersions) == 0 {
		fileVersions, err = m.history.ListBySession(ctx, m.session.ID)
		if err != nil {
			return history.File{}, err
		}
	}

	for _, v := range fileVersions {
		if v.Path == path && v.Version == history.InitialVersion {
			return v, nil
		}
	}

	return history.File{}, fmt.Errorf("initial version not found")
}

func getDisplayPath(path string) string {
	workingDir := config.WorkingDirectory()
	displayPath := strings.TrimPrefix(path, workingDir)
	return strings.TrimPrefix(displayPath, "/")
}
