package chat

import (
	"context"
	"fmt"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/diff"
	"github.com/opencode-ai/opencode/internal/history"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/pubsub"
	"github.com/opencode-ai/opencode/internal/session"
	"github.com/opencode-ai/opencode/internal/tui/components/dialog"
	"github.com/opencode-ai/opencode/internal/tui/styles"
	"github.com/opencode-ai/opencode/internal/tui/theme"
)

type sidebarCmp struct {
	width, height int
	session       session.Session
	sessions      session.Service
	history       history.Service
	app           *app.App
	lspEnabled    bool
	modFiles      map[string]struct {
		additions int
		removals  int
	}
	childSessionIDs map[string]bool
	filesCh         <-chan pubsub.Event[history.File]
	initialVersions map[string]history.File
	subCancel       context.CancelFunc
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
	var cmds []tea.Cmd

	if m.history != nil {
		ctx, cancel := context.WithCancel(context.Background())
		m.subCancel = cancel
		m.filesCh = m.history.Subscribe(ctx)

		m.modFiles = make(map[string]struct {
			additions int
			removals  int
		})
		m.initialVersions = make(map[string]history.File)

		m.loadModifiedFiles(ctx)

		cmds = append(cmds, m.waitForFileEvent())
	}

	return tea.Batch(cmds...)
}

func (m *sidebarCmp) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case SessionSelectedMsg:
		if msg.ID != m.session.ID {
			m.session = msg
			m.initialVersions = make(map[string]history.File)
			if m.subCancel != nil {
				m.subCancel()
			}
			ctx, cancel := context.WithCancel(context.Background())
			m.subCancel = cancel
			m.filesCh = m.history.Subscribe(ctx)
			m.loadModifiedFiles(ctx)
			return m, m.waitForFileEvent()
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
			if msg.Payload.Version == history.InitialVersion {
				m.initialVersions[msg.Payload.Path] = msg.Payload
			}
			ctx := context.Background()
			m.processFileChanges(ctx, msg.Payload)
		}
		return m, m.waitForFileEvent()
	case AgentChangedMsg:
		reg := agentregistry.GetRegistry()
		m.lspEnabled = reg.IsToolEnabled(msg.Name, tools.LSPToolName)
		InvalidateLspCache()
		InvalidateMcpCache()
	case dialog.ThemeChangedMsg:
		InvalidateMcpCache()
		InvalidateLspCache()
	}
	return m, nil
}

const (
	sidebarPadLeft  = 4
	sidebarPadRight = 2
)

func (m *sidebarCmp) contentWidth() int {
	w := m.width - sidebarPadLeft - sidebarPadRight
	if w < 0 {
		w = 0
	}
	return w
}

func (m *sidebarCmp) View() tea.View {
	baseStyle := styles.BaseStyle()
	cw := m.contentWidth()

	var sections []string

	if m.lspEnabled {
		sections = append(sections, lspsConfigured(cw, m.app))
		sections = append(sections, " ")
	}

	if mcpSection := mcpServersConfigured(cw, m.app); mcpSection != "" {
		sections = append(sections, mcpSection)
		sections = append(sections, " ")
	}

	usedHeight := 0
	for _, s := range sections {
		usedHeight += lipgloss.Height(s)
	}
	availableHeight := m.height - 1 - usedHeight
	sections = append(sections, m.modifiedFiles(availableHeight))

	return tea.NewView(baseStyle.
		Width(m.width).
		PaddingLeft(sidebarPadLeft).
		PaddingRight(sidebarPadRight).
		Height(m.height - 1).
		MaxHeight(m.height).
		Render(
			lipgloss.JoinVertical(
				lipgloss.Top,
				sections...,
			),
		))
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

	cw := m.contentWidth()
	return baseStyle.
		Width(cw).
		Render(
			lipgloss.JoinHorizontal(
				lipgloss.Left,
				filePathStr,
				stats,
			),
		)
}

func (m *sidebarCmp) modifiedFiles(availableHeight int) string {
	t := theme.CurrentTheme()
	baseStyle := styles.BaseStyle()

	cw := m.contentWidth()
	modifiedFiles := baseStyle.
		Width(cw).
		Foreground(t.Primary()).
		Bold(true).
		Render("Modified Files")

	if len(m.modFiles) == 0 {
		message := "No modified files"
		remainingWidth := cw - lipgloss.Width(message)
		if remainingWidth > 0 {
			message += strings.Repeat(" ", remainingWidth)
		}
		return baseStyle.
			Width(cw).
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

	headerHeight := lipgloss.Height(modifiedFiles)
	remaining := availableHeight - headerHeight
	if remaining < 1 {
		remaining = 1
	}

	var fileViews []string
	usedLines := 0
	for i, path := range paths {
		stats := m.modFiles[path]
		rendered := m.modifiedFile(path, stats.additions, stats.removals)
		h := lipgloss.Height(rendered)

		left := len(paths) - i
		moreLineHeight := 1
		if left > 1 && usedLines+h+moreLineHeight > remaining {
			moreText := baseStyle.
				Foreground(t.TextMuted()).
				Width(cw).
				Render(fmt.Sprintf("%d more...", left))
			fileViews = append(fileViews, moreText)
			break
		}

		fileViews = append(fileViews, rendered)
		usedLines += h
	}

	return baseStyle.
		Width(cw).
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

func NewSidebarCmp(s session.Session, sessions session.Service, history history.Service, a *app.App) tea.Model {
	reg := agentregistry.GetRegistry()
	agentName := ""
	if a != nil {
		agentName = a.ActiveAgentName()
	}
	lspEnabled := agentName != "" && reg.IsToolEnabled(agentName, tools.LSPToolName)

	return &sidebarCmp{
		session:    s,
		sessions:   sessions,
		history:    history,
		app:        a,
		lspEnabled: lspEnabled,
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
	if v, ok := m.initialVersions[path]; ok {
		return v, nil
	}

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
		if v.Version == history.InitialVersion {
			m.initialVersions[v.Path] = v
		}
	}

	if v, ok := m.initialVersions[path]; ok {
		return v, nil
	}

	return history.File{}, fmt.Errorf("initial version not found")
}

func getDisplayPath(path string) string {
	workingDir := config.WorkingDirectory()
	displayPath := strings.TrimPrefix(path, workingDir)
	return strings.TrimPrefix(displayPath, "/")
}
