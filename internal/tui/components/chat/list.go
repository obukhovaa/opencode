package chat

import (
	"context"
	"fmt"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/pubsub"
	"github.com/opencode-ai/opencode/internal/session"
	"github.com/opencode-ai/opencode/internal/tui/components/dialog"
	"github.com/opencode-ai/opencode/internal/tui/styles"
	"github.com/opencode-ai/opencode/internal/tui/theme"
	"github.com/opencode-ai/opencode/internal/tui/util"
)

type cacheItem struct {
	width   int
	content []uiMessage
}
type messagesCmp struct {
	app              *app.App
	width, height    int
	viewport         viewport.Model
	session          session.Session
	messages         []message.Message
	uiMessages       []uiMessage
	currentMsgID     string
	cachedContent    map[string]cacheItem
	spinner          spinner.Model
	spinnerActive    bool
	rendering        bool
	attachments      viewport.Model
	cachedPending    pendingToolCounts
	cachedUnfinished bool
	taskMessages     map[string][]message.Message
	userScrolledUp   bool
	newMessageCount  int
}
type renderFinishedMsg struct {
	uiMessages      []uiMessage
	cacheUpdates    map[string]cacheItem
	viewportContent string
}

type MessageKeys struct {
	PageDown     key.Binding
	PageUp       key.Binding
	HalfPageUp   key.Binding
	HalfPageDown key.Binding
}

var messageKeys = MessageKeys{
	PageDown: key.NewBinding(
		key.WithKeys("pgdown"),
		key.WithHelp("f/pgdn", "page down"),
	),
	PageUp: key.NewBinding(
		key.WithKeys("pgup"),
		key.WithHelp("b/pgup", "page up"),
	),
	HalfPageUp: key.NewBinding(
		key.WithKeys("ctrl+u"),
		key.WithHelp("ctrl+u", "½ page up"),
	),
	HalfPageDown: key.NewBinding(
		key.WithKeys("ctrl+d", "ctrl+d"),
		key.WithHelp("ctrl+d", "½ page down"),
	),
}

func (m *messagesCmp) Init() tea.Cmd {
	return m.viewport.Init()
}

func (m *messagesCmp) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	switch msg := msg.(type) {
	case dialog.ThemeChangedMsg:
		styles.InvalidateMarkdownCache()
		if m.rendering {
			m.rendering = false
		}
		m.rerender()
	case SessionSelectedMsg:
		if msg.ID != m.session.ID {
			cmd := m.SetSession(msg)
			cmds = append(cmds, cmd)
		}
	case SessionClearedMsg:
		m.session = session.Session{}
		m.messages = make([]message.Message, 0)
		m.taskMessages = make(map[string][]message.Message)
		m.cachedContent = make(map[string]cacheItem)
		m.currentMsgID = ""
		m.rendering = false
		m.userScrolledUp = false
		m.newMessageCount = 0
		cmds = append(cmds, m.emitScrollState())

	case tea.KeyPressMsg:
		if key.Matches(msg, messageKeys.PageUp) || key.Matches(msg, messageKeys.PageDown) ||
			key.Matches(msg, messageKeys.HalfPageUp) || key.Matches(msg, messageKeys.HalfPageDown) {
			u, cmd := m.viewport.Update(msg)
			m.viewport = u
			cmds = append(cmds, cmd)
			cmds = append(cmds, m.updateScrollState())
		}
	case tea.MouseWheelMsg:
		u, cmd := m.viewport.Update(msg)
		m.viewport = u
		cmds = append(cmds, cmd)
		cmds = append(cmds, m.updateScrollState())

	case renderFinishedMsg:
		if !m.rendering {
			// Async render was superseded by a sync render; discard stale results
			break
		}
		m.rendering = false
		m.uiMessages = msg.uiMessages
		for id, item := range msg.cacheUpdates {
			m.cachedContent[id] = item
		}
		if m.userScrolledUp {
			yOff := m.viewport.YOffset()
			m.viewport.SetContent(msg.viewportContent)
			m.viewport.SetYOffset(yOff)
		} else {
			m.viewport.SetContent(msg.viewportContent)
			m.viewport.GotoBottom()
		}
		// Messages may have arrived while the async render was in flight;
		// if so, kick off another render so they become visible immediately.
		if m.hasCacheMisses() {
			cmds = append(cmds, m.renderViewAsync())
		}
	case pubsub.Event[session.Session]:
		if msg.Type == pubsub.UpdatedEvent && msg.Payload.ID == m.session.ID {
			m.session = msg.Payload
			if m.session.SummaryMessageID == m.currentMsgID {
				delete(m.cachedContent, m.currentMsgID)
				if m.rendering {
					m.rendering = false
				}
				m.renderViewSync()
			}
		}
	case pubsub.Event[message.Message]:
		needsRerender := false
		if msg.Type == pubsub.CreatedEvent {
			if msg.Payload.SessionID == m.session.ID {

				messageExists := false
				for _, v := range m.messages {
					if v.ID == msg.Payload.ID {
						messageExists = true
						break
					}
				}

				if !messageExists {
					if len(m.messages) > 0 {
						lastMsgID := m.messages[len(m.messages)-1].ID
						delete(m.cachedContent, lastMsgID)
					}

					m.messages = append(m.messages, msg.Payload)
					delete(m.cachedContent, m.currentMsgID)
					m.currentMsgID = msg.Payload.ID
					needsRerender = true
				}
			}
			// There are tool calls from the child task
			for _, v := range m.messages {
				for _, c := range v.ToolCalls() {
					if c.ID == msg.Payload.SessionID {
						m.taskMessages[c.ID] = append(m.taskMessages[c.ID], msg.Payload)
						delete(m.cachedContent, v.ID)
						needsRerender = true
					}
				}
			}
		} else if msg.Type == pubsub.UpdatedEvent {
			if msg.Payload.SessionID == m.session.ID {
				for i, v := range m.messages {
					if v.ID == msg.Payload.ID {
						m.messages[i] = msg.Payload
						delete(m.cachedContent, msg.Payload.ID)
						needsRerender = true
						break
					}
				}
			}
			for _, v := range m.messages {
				for _, c := range v.ToolCalls() {
					if c.ID == msg.Payload.SessionID {
						for i, tm := range m.taskMessages[c.ID] {
							if tm.ID == msg.Payload.ID {
								m.taskMessages[c.ID][i] = msg.Payload
								delete(m.cachedContent, v.ID)
								needsRerender = true
								break
							}
						}
					}
				}
			}
		}
		if needsRerender {
			m.recomputeToolState()
			if m.userScrolledUp {
				if !m.rendering && m.hasCacheMisses() {
					cmds = append(cmds, m.renderViewAsync())
				} else if !m.rendering {
					yOff := m.viewport.YOffset()
					m.renderViewSync()
					m.viewport.SetYOffset(yOff)
				}
			} else {
				if !m.rendering && m.hasCacheMisses() {
					cmds = append(cmds, m.renderViewAsync())
				} else if !m.rendering {
					m.renderViewSync()
				}
				if len(m.messages) > 0 {
					if (msg.Type == pubsub.CreatedEvent) ||
						(msg.Type == pubsub.UpdatedEvent && msg.Payload.ID == m.messages[len(m.messages)-1].ID) {
						m.viewport.GotoBottom()
					}
				}
			}
			if msg.Type == pubsub.CreatedEvent && msg.Payload.SessionID == m.session.ID {
				if m.userScrolledUp {
					if msg.Payload.Role == message.User {
						m.userScrolledUp = false
						m.newMessageCount = 0
						m.viewport.GotoBottom()
						cmds = append(cmds, m.emitScrollState())
					} else {
						m.newMessageCount++
						cmds = append(cmds, m.emitScrollState())
					}
				}
			}
		}
	}

	working := m.IsAgentWorking()
	if working && !m.spinnerActive {
		m.spinnerActive = true
		cmds = append(cmds, m.spinner.Tick)
	} else if !working && m.spinnerActive {
		m.spinnerActive = false
	}

	if m.spinnerActive {
		spinner, cmd := m.spinner.Update(msg)
		m.spinner = spinner
		cmds = append(cmds, cmd)
	}
	return m, tea.Batch(cmds...)
}

func (m *messagesCmp) IsAgentWorking() bool {
	return m.app.ActiveAgent().IsSessionBusy(m.session.ID)
}

func (m *messagesCmp) hasCacheMisses() bool {
	for _, msg := range m.messages {
		if msg.Role == message.User || msg.Role == message.Assistant {
			if cache, ok := m.cachedContent[msg.ID]; !ok || cache.width != m.width {
				return true
			}
		}
	}
	return false
}

type renderResult struct {
	uiMessages   []uiMessage
	cacheUpdates map[string]cacheItem
}

func (m *messagesCmp) computeRender() renderResult {
	var uiMsgs []uiMessage
	cacheUpdates := make(map[string]cacheItem)
	pos := 0

	for inx, msg := range m.messages {
		switch msg.Role {
		case message.User:
			if cache, ok := m.cachedContent[msg.ID]; ok && cache.width == m.width {
				uiMsgs = append(uiMsgs, cache.content...)
				continue
			}
			userMsg := renderUserMessage(
				msg,
				msg.ID == m.currentMsgID,
				m.width,
				pos,
			)
			uiMsgs = append(uiMsgs, userMsg)
			cacheUpdates[msg.ID] = cacheItem{
				width:   m.width,
				content: []uiMessage{userMsg},
			}
			pos += userMsg.height + 1
		case message.Assistant:
			if cache, ok := m.cachedContent[msg.ID]; ok && cache.width == m.width {
				uiMsgs = append(uiMsgs, cache.content...)
				continue
			}
			isSummary := m.session.SummaryMessageID == msg.ID

			assistantMessages := renderAssistantMessage(
				msg,
				inx,
				m.messages,
				m.taskMessages,
				m.currentMsgID,
				isSummary,
				m.width,
				pos,
			)
			for _, am := range assistantMessages {
				uiMsgs = append(uiMsgs, am)
				pos += am.height + 1
			}
			cacheUpdates[msg.ID] = cacheItem{
				width:   m.width,
				content: assistantMessages,
			}
		}
	}

	return renderResult{uiMessages: uiMsgs, cacheUpdates: cacheUpdates}
}

func (m *messagesCmp) applyRender(result renderResult) {
	m.uiMessages = result.uiMessages
	for id, item := range result.cacheUpdates {
		m.cachedContent[id] = item
	}

	baseStyle := styles.BaseStyle()
	messages := make([]string, 0, len(m.uiMessages)*2)
	for _, v := range m.uiMessages {
		messages = append(messages, lipgloss.JoinVertical(lipgloss.Left, v.content),
			baseStyle.
				Width(m.width).
				Render(
					"",
				),
		)
	}

	m.viewport.SetContent(
		baseStyle.
			Width(m.width).
			Render(
				lipgloss.JoinVertical(
					lipgloss.Top,
					messages...,
				),
			),
	)
}

func (m *messagesCmp) renderViewSync() {
	if m.width == 0 {
		return
	}
	result := m.computeRender()
	m.applyRender(result)
}

func (m *messagesCmp) renderViewAsync() tea.Cmd {
	if m.width == 0 {
		return nil
	}
	m.rendering = true

	msgsCopy := make([]message.Message, len(m.messages))
	copy(msgsCopy, m.messages)

	taskMsgsCopy := make(map[string][]message.Message, len(m.taskMessages))
	for k, v := range m.taskMessages {
		taskMsgsCopy[k] = append([]message.Message(nil), v...)
	}

	cacheCopy := make(map[string]cacheItem, len(m.cachedContent))
	for k, v := range m.cachedContent {
		cacheCopy[k] = v
	}

	width := m.width
	currentMsgID := m.currentMsgID
	summaryMsgID := m.session.SummaryMessageID

	return func() tea.Msg {
		var uiMsgs []uiMessage
		cacheUpdates := make(map[string]cacheItem)
		pos := 0

		for inx, msg := range msgsCopy {
			switch msg.Role {
			case message.User:
				if cache, ok := cacheCopy[msg.ID]; ok && cache.width == width {
					uiMsgs = append(uiMsgs, cache.content...)
					continue
				}
				userMsg := renderUserMessage(
					msg,
					msg.ID == currentMsgID,
					width,
					pos,
				)
				uiMsgs = append(uiMsgs, userMsg)
				cacheUpdates[msg.ID] = cacheItem{
					width:   width,
					content: []uiMessage{userMsg},
				}
				pos += userMsg.height + 1
			case message.Assistant:
				if cache, ok := cacheCopy[msg.ID]; ok && cache.width == width {
					uiMsgs = append(uiMsgs, cache.content...)
					continue
				}
				isSummary := summaryMsgID == msg.ID

				assistantMessages := renderAssistantMessage(
					msg,
					inx,
					msgsCopy,
					taskMsgsCopy,
					currentMsgID,
					isSummary,
					width,
					pos,
				)
				for _, am := range assistantMessages {
					uiMsgs = append(uiMsgs, am)
					pos += am.height + 1
				}
				cacheUpdates[msg.ID] = cacheItem{
					width:   width,
					content: assistantMessages,
				}
			}
		}

		baseStyle := styles.BaseStyle()
		messages := make([]string, 0, len(uiMsgs)*2)
		for _, v := range uiMsgs {
			messages = append(messages, lipgloss.JoinVertical(lipgloss.Left, v.content),
				baseStyle.
					Width(width).
					Render(
						"",
					),
			)
		}

		viewportContent := baseStyle.
			Width(width).
			Render(
				lipgloss.JoinVertical(
					lipgloss.Top,
					messages...,
				),
			)

		return renderFinishedMsg{
			uiMessages:      uiMsgs,
			cacheUpdates:    cacheUpdates,
			viewportContent: viewportContent,
		}
	}
}

func (m *messagesCmp) View() tea.View {
	baseStyle := styles.BaseStyle()

	if len(m.messages) == 0 {
		content := baseStyle.
			Width(m.width).
			Height(m.height - 2).
			Render(
				m.initialScreen(),
			)

		return tea.NewView(baseStyle.
			Width(m.width).
			Render(
				lipgloss.JoinVertical(
					lipgloss.Top,
					content,
					"",
					m.help(),
				),
			))
	}

	return tea.NewView(baseStyle.
		Width(m.width).
		Render(
			lipgloss.JoinVertical(
				lipgloss.Top,
				m.viewport.View(),
				m.working(),
				m.help(),
			),
		))
}

type pendingToolCounts struct {
	tools     int
	subagents int
}

func (p pendingToolCounts) total() int {
	return p.tools + p.subagents
}

func countToolsWithoutResponse(messages []message.Message) pendingToolCounts {
	toolCalls := make([]message.ToolCall, 0)
	toolResults := make([]message.ToolResult, 0)
	for _, m := range messages {
		toolCalls = append(toolCalls, m.ToolCalls()...)
		toolResults = append(toolResults, m.ToolResults()...)
	}

	var counts pendingToolCounts
	for _, v := range toolCalls {
		found := false
		for _, r := range toolResults {
			if v.ID == r.ToolCallID {
				found = true
				break
			}
		}
		if !found && v.Finished {
			if v.Name == agent.TaskToolName {
				counts.subagents++
			} else {
				counts.tools++
			}
		}
	}
	return counts
}

func pendingTaskDescription(p pendingToolCounts) string {
	switch {
	case p.tools == 0:
		return fmt.Sprintf("Running %d subagents", p.subagents)
	case p.subagents == 0:
		return fmt.Sprintf("Running %d tools", p.tools)
	default:
		return fmt.Sprintf("Running %d tools and %d subagents", p.tools, p.subagents)
	}
}

func hasUnfinishedToolCalls(messages []message.Message) bool {
	toolCalls := make([]message.ToolCall, 0)
	for _, m := range messages {
		toolCalls = append(toolCalls, m.ToolCalls()...)
	}
	for _, v := range toolCalls {
		if !v.Finished {
			return true
		}
	}
	return false
}

func (m *messagesCmp) recomputeToolState() {
	m.cachedPending = countToolsWithoutResponse(m.messages)
	m.cachedUnfinished = hasUnfinishedToolCalls(m.messages)
}

func (m *messagesCmp) working() string {
	text := ""
	if m.IsAgentWorking() && len(m.messages) > 0 {
		t := theme.CurrentTheme()
		baseStyle := styles.BaseStyle()

		task := "Thinking"
		lastMessage := m.messages[len(m.messages)-1]
		if m.cachedPending.total() > 1 {
			task = pendingTaskDescription(m.cachedPending)
		} else if m.cachedPending.total() == 1 {
			if m.cachedPending.subagents == 1 {
				task = "Running subagent"
			} else {
				task = "Waiting for tool"
			}
		} else if m.cachedUnfinished {
			task = "Building tool call"
		} else if !lastMessage.IsFinished() {
			task = "Generating"
		}
		if task != "" {
			text += baseStyle.
				Width(m.width).
				Foreground(t.Primary()).
				Bold(true).
				Render(fmt.Sprintf("%s %s ", m.spinner.View(), task))
		}
	}
	return text
}

func (m *messagesCmp) help() string {
	t := theme.CurrentTheme()
	baseStyle := styles.BaseStyle()

	text := ""

	if m.app.ActiveAgent().IsBusy() {
		text += lipgloss.JoinHorizontal(
			lipgloss.Left,
			baseStyle.Foreground(t.TextMuted()).Bold(true).Render("press "),
			baseStyle.Foreground(t.Text()).Bold(true).Render("esc"),
			baseStyle.Foreground(t.TextMuted()).Bold(true).Render(" to exit cancel"),
		)
	} else {
		text += lipgloss.JoinHorizontal(
			lipgloss.Left,
			baseStyle.Foreground(t.TextMuted()).Bold(true).Render("press "),
			baseStyle.Foreground(t.Text()).Bold(true).Render("enter"),
			baseStyle.Foreground(t.TextMuted()).Bold(true).Render(" to send,"),
			baseStyle.Foreground(t.TextMuted()).Bold(true).Render(" type"),
			baseStyle.Foreground(t.Text()).Bold(true).Render(" \\"),
			baseStyle.Foreground(t.TextMuted()).Bold(true).Render(" to add a new line,"),
			baseStyle.Foreground(t.TextMuted()).Bold(true).Render(" type"),
			baseStyle.Foreground(t.Text()).Bold(true).Render(" /"),
			baseStyle.Foreground(t.TextMuted()).Bold(true).Render(" to command,"),
			baseStyle.Foreground(t.TextMuted()).Bold(true).Render(" type"),
			baseStyle.Foreground(t.Text()).Bold(true).Render(" !"),
			baseStyle.Foreground(t.TextMuted()).Bold(true).Render(" for shell"),
		)
	}
	return baseStyle.
		Width(m.width).
		Render(text)
}

func (m *messagesCmp) initialScreen() string {
	baseStyle := styles.BaseStyle()

	return baseStyle.Width(m.width).Render(
		lipgloss.JoinVertical(
			lipgloss.Top,
			header(m.width),
			"",
			lspsConfigured(m.width),
		),
	)
}

func (m *messagesCmp) updateScrollState() tea.Cmd {
	wasScrolledUp := m.userScrolledUp
	m.userScrolledUp = !m.viewport.AtBottom()
	if wasScrolledUp != m.userScrolledUp {
		if !m.userScrolledUp {
			m.newMessageCount = 0
		}
		return m.emitScrollState()
	}
	return nil
}

func (m *messagesCmp) emitScrollState() tea.Cmd {
	return util.CmdHandler(ScrollStateMsg{
		Locked:      m.userScrolledUp,
		NewMessages: m.newMessageCount,
	})
}

func (m *messagesCmp) rerender() {
	for _, msg := range m.messages {
		delete(m.cachedContent, msg.ID)
	}
	m.renderViewSync()
}

func (m *messagesCmp) SetSize(width, height int) tea.Cmd {
	if m.width == width && m.height == height {
		return nil
	}
	m.width = width
	m.height = height
	m.viewport.SetWidth(width)
	m.viewport.SetHeight(height - 2)
	m.attachments.SetWidth(width + 40)
	m.attachments.SetHeight(3)
	m.rerender()
	return nil
}

func (m *messagesCmp) GetSize() (int, int) {
	return m.width, m.height
}

func (m *messagesCmp) SetSession(session session.Session) tea.Cmd {
	if m.session.ID == session.ID {
		return nil
	}
	m.session = session
	messages, err := m.app.Messages.List(context.Background(), session.ID)
	if err != nil {
		return util.ReportError(err)
	}
	m.messages = messages
	m.cachedContent = make(map[string]cacheItem)
	m.taskMessages = make(map[string][]message.Message)
	for _, msg := range m.messages {
		for _, tc := range msg.ToolCalls() {
			if tc.Name == agent.TaskToolName {
				if taskMsgs, err := m.app.Messages.List(context.Background(), tc.ID); err == nil {
					m.taskMessages[tc.ID] = taskMsgs
				}
			}
		}
	}
	if len(m.messages) > 0 {
		m.currentMsgID = m.messages[len(m.messages)-1].ID
	}
	m.recomputeToolState()
	m.renderViewSync()
	m.viewport.GotoBottom()
	m.userScrolledUp = false
	m.newMessageCount = 0
	return m.emitScrollState()
}

func (m *messagesCmp) BindingKeys() []key.Binding {
	return []key.Binding{
		m.viewport.KeyMap.PageDown,
		m.viewport.KeyMap.PageUp,
		m.viewport.KeyMap.HalfPageUp,
		m.viewport.KeyMap.HalfPageDown,
	}
}

func NewMessagesCmp(app *app.App) tea.Model {
	s := spinner.New()
	s.Spinner = spinner.Points
	vp := viewport.New()
	attachmets := viewport.New()
	vp.KeyMap.PageUp = messageKeys.PageUp
	vp.KeyMap.PageDown = messageKeys.PageDown
	vp.KeyMap.HalfPageUp = messageKeys.HalfPageUp
	vp.KeyMap.HalfPageDown = messageKeys.HalfPageDown
	return &messagesCmp{
		app:           app,
		cachedContent: make(map[string]cacheItem),
		taskMessages:  make(map[string][]message.Message),
		viewport:      vp,
		spinner:       s,
		attachments:   attachmets,
	}
}
