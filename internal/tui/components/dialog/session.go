package dialog

import (
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/opencode-ai/opencode/internal/session"
	"github.com/opencode-ai/opencode/internal/tui/layout"
	"github.com/opencode-ai/opencode/internal/tui/styles"
	"github.com/opencode-ai/opencode/internal/tui/theme"
	"github.com/opencode-ai/opencode/internal/tui/util"
)

// SessionSelectedMsg is sent when a session is selected
type SessionSelectedMsg struct {
	Session session.Session
}

// CloseSessionDialogMsg is sent when the session dialog is closed
type CloseSessionDialogMsg struct{}

// SessionDialog interface for the session switching dialog
type SessionDialog interface {
	tea.Model
	layout.Bindings
	SetSessions(sessions []session.Session)
	SetSelectedSession(sessionID string)
	SetTitle(title string)
}

type sessionDialogCmp struct {
	sessions          []session.Session
	filtered          []session.Session // nil sentinel → use sessions
	selectedIdx       int
	width             int
	height            int
	selectedSessionID string
	title             string
	query             textinput.Model
	// Cached layout dimensions. Recomputed only when SetSessions or
	// WindowSizeMsg fires, so filter changes never resize the dialog.
	contentWidth int
	maxVisible   int
}

const (
	sessionDialogMinWidth     = 40
	sessionDialogMaxVisible   = 7
	sessionDialogChromeRows   = 10 // padding + border + title + search + help + separators
	sessionDialogItemRows     = 2  // title line + metadata line
	sessionDialogItemChrome   = 4  // 2-char indent + 1-char left/right padding
)

type sessionKeyMap struct {
	Up         key.Binding
	Down       key.Binding
	Enter      key.Binding
	Escape     key.Binding
	J          key.Binding
	K          key.Binding
	ClearQuery key.Binding
}

var sessionKeys = sessionKeyMap{
	Up: key.NewBinding(
		key.WithKeys("up"),
		key.WithHelp("↑", "previous session"),
	),
	Down: key.NewBinding(
		key.WithKeys("down"),
		key.WithHelp("↓", "next session"),
	),
	Enter: key.NewBinding(
		key.WithKeys("enter"),
		key.WithHelp("enter", "select session"),
	),
	Escape: key.NewBinding(
		key.WithKeys("esc"),
		key.WithHelp("esc", "close"),
	),
	J: key.NewBinding(
		key.WithKeys("j"),
		key.WithHelp("j", "next session"),
	),
	K: key.NewBinding(
		key.WithKeys("k"),
		key.WithHelp("k", "previous session"),
	),
	ClearQuery: key.NewBinding(
		key.WithKeys("ctrl+u"),
		key.WithHelp("^U", "clear search"),
	),
}

func (s *sessionDialogCmp) Init() tea.Cmd {
	return textinput.Blink
}

// visibleSessions returns filtered when non-nil, otherwise the full list.
func (s *sessionDialogCmp) visibleSessions() []session.Session {
	if s.filtered != nil {
		return s.filtered
	}
	return s.sessions
}

// recomputeLayout fixes the dialog's width and item-slot count based on the
// full session list and the current terminal size. It is called when the
// session list or window size changes — never on filter changes — so the
// dialog does not shrink or blink as the user types in the search bar.
func (s *sessionDialogCmp) recomputeLayout() {
	now := time.Now()

	width := sessionDialogMinWidth
	for _, sess := range s.sessions {
		if w := len(sess.Title) + sessionDialogItemChrome; w > width {
			width = w
		}
		if w := len(formatSessionMetadata(sess, now)) + sessionDialogItemChrome; w > width {
			width = w
		}
	}
	if s.width > 0 {
		width = min(width, s.width-15)
	}
	s.contentWidth = max(sessionDialogMinWidth, width)

	slots := sessionDialogMaxVisible
	if s.height > 0 {
		fromHeight := max(1, (s.height-sessionDialogChromeRows)/sessionDialogItemRows)
		slots = min(slots, fromHeight)
	}
	if len(s.sessions) > 0 {
		slots = min(slots, len(s.sessions))
	}
	s.maxVisible = max(1, slots)
}

// filter recomputes filtered based on query value. When the query is empty,
// filtered is reset to nil so visibleSessions falls back to the full list.
func (s *sessionDialogCmp) filter() {
	q := strings.TrimSpace(strings.ToLower(s.query.Value()))
	if q == "" {
		s.filtered = nil
		s.restoreSelection()
		return
	}

	result := make([]session.Session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		if strings.Contains(strings.ToLower(sess.Title), q) {
			result = append(result, sess)
		}
	}
	s.filtered = result
	s.restoreSelection()
}

// restoreSelection points selectedIdx at the previously-remembered session ID
// within the current visible list, falling back to index 0 when it's not
// currently visible. The remembered ID is intentionally NOT overwritten on
// fallback, so that narrowing the filter past the original selection and
// then clearing the filter restores that selection.
func (s *sessionDialogCmp) restoreSelection() {
	vis := s.visibleSessions()
	if len(vis) == 0 {
		s.selectedIdx = 0
		return
	}
	if s.selectedSessionID != "" {
		for i, sess := range vis {
			if sess.ID == s.selectedSessionID {
				s.selectedIdx = i
				return
			}
		}
	}
	s.selectedIdx = 0
}

func (s *sessionDialogCmp) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		// Component-level keys always win, even while the textinput is focused.
		switch {
		case key.Matches(msg, sessionKeys.Escape):
			return s, util.CmdHandler(CloseSessionDialogMsg{})
		case key.Matches(msg, sessionKeys.Enter):
			vis := s.visibleSessions()
			if len(vis) > 0 && s.selectedIdx < len(vis) {
				return s, util.CmdHandler(SessionSelectedMsg{
					Session: vis[s.selectedIdx],
				})
			}
			return s, nil
		case key.Matches(msg, sessionKeys.Up):
			s.moveSelection(-1)
			return s, nil
		case key.Matches(msg, sessionKeys.Down):
			s.moveSelection(1)
			return s, nil
		case key.Matches(msg, sessionKeys.ClearQuery):
			if s.query.Value() != "" {
				s.query.SetValue("")
				s.filter()
			}
			return s, nil
		}

		// j/k navigate only while the query is empty; once the user starts
		// typing, j and k must go to the textinput as literal characters.
		if s.query.Value() == "" {
			switch {
			case key.Matches(msg, sessionKeys.K):
				s.moveSelection(-1)
				return s, nil
			case key.Matches(msg, sessionKeys.J):
				s.moveSelection(1)
				return s, nil
			}
		}

		// Everything else goes to the textinput; if the value changed, re-filter.
		prev := s.query.Value()
		var cmd tea.Cmd
		s.query, cmd = s.query.Update(msg)
		if s.query.Value() != prev {
			s.filter()
		}
		return s, cmd
	case tea.WindowSizeMsg:
		s.width = msg.Width
		s.height = msg.Height
		s.recomputeLayout()
	}
	return s, nil
}

func (s *sessionDialogCmp) moveSelection(delta int) {
	vis := s.visibleSessions()
	if len(vis) == 0 {
		return
	}
	next := min(max(s.selectedIdx+delta, 0), len(vis)-1)
	s.selectedIdx = next
	s.selectedSessionID = vis[next].ID
}

func (s *sessionDialogCmp) View() tea.View {
	t := theme.CurrentTheme()
	baseStyle := styles.BaseStyle()

	if len(s.sessions) == 0 {
		return tea.NewView(baseStyle.Padding(1, 2).
			Border(lipgloss.RoundedBorder()).
			BorderBackground(t.Background()).
			BorderForeground(t.TextMuted()).
			Width(sessionDialogMinWidth).
			Render("No sessions available"))
	}

	// Width and slot count are cached — they do NOT depend on the filtered list
	// so the dialog does not resize as the user types in the search bar.
	w := s.contentWidth
	slots := max(s.maxVisible, 1)

	now := time.Now()
	vis := s.visibleSessions()

	// Compute scroll window over the filtered list, bounded by `slots`.
	startIdx := 0
	if len(vis) > slots {
		halfVisible := slots / 2
		if s.selectedIdx >= halfVisible && s.selectedIdx < len(vis)-halfVisible {
			startIdx = s.selectedIdx - halfVisible
		} else if s.selectedIdx >= len(vis)-halfVisible {
			startIdx = len(vis) - slots
		}
	}
	endIdx := min(startIdx+slots, len(vis))

	// Always emit exactly `slots` rows so the dialog height is constant.
	blankRow := baseStyle.Width(w).Render("")
	blankSlot := lipgloss.JoinVertical(lipgloss.Left, blankRow, blankRow)

	sessionItems := make([]string, 0, slots)
	for i := startIdx; i < endIdx; i++ {
		sess := vis[i]

		titleStyle := baseStyle.Width(w).Padding(0, 1)
		metaStyle := baseStyle.Width(w).Padding(0, 1).Foreground(t.TextMuted())

		if i == s.selectedIdx {
			titleStyle = titleStyle.
				Background(t.Primary()).
				Foreground(t.Background()).
				Bold(true)
			metaStyle = metaStyle.
				Background(t.Primary()).
				Foreground(t.Background())
		}

		titleLine := titleStyle.Render(sess.Title)
		metaLine := metaStyle.Render("  " + formatSessionMetadata(sess, now))
		sessionItems = append(sessionItems, lipgloss.JoinVertical(lipgloss.Left, titleLine, metaLine))
	}
	for len(sessionItems) < slots {
		sessionItems = append(sessionItems, blankSlot)
	}

	dialogTitle := s.title
	if dialogTitle == "" {
		dialogTitle = "Switch Session"
	}

	title := baseStyle.
		Foreground(t.Primary()).
		Bold(true).
		Width(w).
		Padding(0, 1).
		Render(dialogTitle)

	// Search input, width synced to the dialog.
	s.query.SetWidth(w - 2)
	searchBar := baseStyle.Width(w).Padding(0, 1).Render(s.query.View())

	var body string
	if len(vis) == 0 {
		// Empty-state line on the first slot, blank padding below keeps the
		// dialog height identical to the populated state.
		emptyLine := baseStyle.
			Width(w).
			Padding(0, 1).
			Foreground(t.TextMuted()).
			Render("no sessions match")
		emptySlot := lipgloss.JoinVertical(lipgloss.Left, emptyLine, blankRow)
		padded := make([]string, 0, slots)
		padded = append(padded, emptySlot)
		for len(padded) < slots {
			padded = append(padded, blankSlot)
		}
		body = baseStyle.Width(w).Render(lipgloss.JoinVertical(lipgloss.Left, padded...))
	} else {
		body = baseStyle.Width(w).Render(lipgloss.JoinVertical(lipgloss.Left, sessionItems...))
	}

	help := baseStyle.
		Width(w).
		Padding(0, 1).
		Foreground(t.TextMuted()).
		Render("↑↓ navigate  ⏎ select  ^U clear  esc close")

	content := lipgloss.JoinVertical(
		lipgloss.Left,
		title,
		blankRow,
		searchBar,
		blankRow,
		body,
		blankRow,
		help,
	)

	return tea.NewView(baseStyle.Padding(1, 2).
		Border(lipgloss.RoundedBorder()).
		BorderBackground(t.Background()).
		BorderForeground(t.TextMuted()).
		Width(lipgloss.Width(content) + 6).
		Render(content))
}

func (s *sessionDialogCmp) BindingKeys() []key.Binding {
	return layout.KeyMapToSlice(sessionKeys)
}

func (s *sessionDialogCmp) SetSessions(sessions []session.Session) {
	s.sessions = sessions
	s.filtered = nil
	s.query.SetValue("")
	s.recomputeLayout()

	// If we have a selected session ID, find its index
	if s.selectedSessionID != "" {
		for i, sess := range sessions {
			if sess.ID == s.selectedSessionID {
				s.selectedIdx = i
				return
			}
		}
	}

	// Default to first session if selected not found
	s.selectedIdx = 0
	if len(sessions) > 0 {
		s.selectedSessionID = sessions[0].ID
	}
}

func (s *sessionDialogCmp) SetSelectedSession(sessionID string) {
	s.selectedSessionID = sessionID

	// Resolve the index against the full session list. Any active filter
	// narrows what's visible but must not change which session is
	// "remembered" as the caller's selection.
	if len(s.sessions) == 0 {
		return
	}
	for i, sess := range s.sessions {
		if sess.ID == sessionID {
			s.selectedIdx = i
			return
		}
	}
}

func (s *sessionDialogCmp) SetTitle(title string) {
	s.title = title
}

// NewSessionDialogCmp creates a new session switching dialog
func NewSessionDialogCmp() SessionDialog {
	t := theme.CurrentTheme()
	ti := textinput.New()
	ti.Placeholder = "search sessions"
	ti.Prompt = "> "
	ti.SetWidth(38)
	st := ti.Styles()
	st.Focused.Placeholder = st.Focused.Placeholder.Background(t.Background())
	st.Blurred.Placeholder = st.Blurred.Placeholder.Background(t.Background())
	st.Focused.Prompt = st.Focused.Prompt.Background(t.Background()).Foreground(t.Primary())
	st.Blurred.Prompt = st.Blurred.Prompt.Background(t.Background())
	st.Focused.Text = st.Focused.Text.Background(t.Background())
	st.Blurred.Text = st.Blurred.Text.Background(t.Background())
	ti.SetStyles(st)
	ti.Focus()

	return &sessionDialogCmp{
		sessions:          []session.Session{},
		selectedIdx:       0,
		selectedSessionID: "",
		query:             ti,
	}
}
