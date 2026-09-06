package dialog

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/tui/layout"
	"github.com/opencode-ai/opencode/internal/tui/styles"
	"github.com/opencode-ai/opencode/internal/tui/theme"
	"github.com/opencode-ai/opencode/internal/tui/util"
)

// ToggleQueueDialogMsg asks the TUI to show or hide the queued-messages viewer
// for a session. Emitted by the chat page (ctrl+g).
type ToggleQueueDialogMsg struct {
	SessionID string
}

// CloseQueueDialogMsg closes the queued-messages viewer.
type CloseQueueDialogMsg struct{}

// QueueDialog previews the messages waiting in a session's in-memory queue.
// It is a viewer: the only mutation it offers is discarding the whole queue
// (ctrl+x), mirroring the chat page's binding.
type QueueDialog interface {
	tea.Model
	layout.Bindings
	SetSession(sessionID string)
	SetSize(width, height int)
}

type queueDialogCmp struct {
	app           *app.App
	sessionID     string
	width, height int
}

var queueDialogKeys = struct {
	Close   key.Binding
	Discard key.Binding
}{
	Close: key.NewBinding(
		key.WithKeys("esc", "ctrl+g", "q"),
		key.WithHelp("esc", "close"),
	),
	Discard: key.NewBinding(
		key.WithKeys("ctrl+x"),
		key.WithHelp("ctrl+x", "discard queued messages"),
	),
}

const (
	// queueDialogMaxWidth caps the dialog so long pasted messages don't stretch
	// it across a wide terminal.
	queueDialogMaxWidth = 76
	// queueDialogMaxEntries caps the listed previews; the rest is summarised.
	queueDialogMaxEntries = 12
)

func (d *queueDialogCmp) SetSession(sessionID string) {
	d.sessionID = sessionID
}

func (d *queueDialogCmp) SetSize(width, height int) {
	d.width, d.height = width, height
}

func (d *queueDialogCmp) Init() tea.Cmd {
	return nil
}

func (d *queueDialogCmp) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, queueDialogKeys.Close):
			return d, util.CmdHandler(CloseQueueDialogMsg{})
		case key.Matches(msg, queueDialogKeys.Discard):
			// The dialog swallows key presses, so the chat page's ctrl+x never
			// fires while it is open — handle it here so the advertised key
			// works from inside the viewer.
			if d.sessionID != "" {
				d.app.DiscardQueue(d.sessionID)
			}
			return d, util.CmdHandler(CloseQueueDialogMsg{})
		}
	}
	return d, nil
}

func (d *queueDialogCmp) View() tea.View {
	t := theme.CurrentTheme()
	baseStyle := styles.BaseStyle()
	bg := t.Background()

	// Read the queue at render time rather than caching it: the drain worker
	// keeps delivering while the dialog is open, so a snapshot taken on open
	// would show messages that have already been sent.
	var queued []app.QueuedMessage
	if d.sessionID != "" {
		queued = d.app.QueuedMessages(d.sessionID)
	}

	innerWidth := queueDialogMaxWidth
	if d.width > 0 && d.width-10 < innerWidth {
		innerWidth = max(20, d.width-10)
	}

	header := baseStyle.
		Foreground(t.Primary()).
		Bold(true).
		Render(fmt.Sprintf("Queued messages (%d)", len(queued)))

	lines := []string{header, ""}

	if len(queued) == 0 {
		lines = append(lines, baseStyle.Foreground(t.TextMuted()).Italic(true).
			Render("Nothing queued — messages typed while the agent is busy land here."))
	} else {
		shown := queued
		if len(shown) > d.maxEntries() {
			shown = shown[:d.maxEntries()]
		}
		for i, m := range shown {
			lines = append(lines, queueEntryLine(i, m, innerWidth, baseStyle, t))
		}
		if rest := len(queued) - len(shown); rest > 0 {
			lines = append(lines, baseStyle.Foreground(t.TextMuted()).Italic(true).
				Render(fmt.Sprintf("… and %d more", rest)))
		}
	}

	lines = append(lines,
		"",
		baseStyle.Foreground(t.TextMuted()).Render("esc close · ctrl+x discard all"),
	)

	content := baseStyle.Width(innerWidth).Render(
		lipgloss.JoinVertical(lipgloss.Left, lines...),
	)

	rendered := baseStyle.Padding(1, 2).
		Border(lipgloss.RoundedBorder()).
		BorderBackground(bg).
		BorderForeground(t.TextMuted()).
		Width(innerWidth + 6).
		Render(content)

	return tea.NewView(styles.ForceReplaceBackgroundWithLipgloss(rendered, bg))
}

// maxEntries is how many previews fit: the hard cap, reduced further on short
// terminals so the dialog cannot outgrow the window it is centred in. The
// subtracted rows are the border (2), padding (2), header (2) and footer (2).
func (d *queueDialogCmp) maxEntries() int {
	n := queueDialogMaxEntries
	if d.height > 0 {
		if avail := d.height - 8; avail < n {
			n = max(1, avail)
		}
	}
	return n
}

// queueEntryLine renders one queued message as a single truncated preview line
// prefixed with its delivery position.
func queueEntryLine(idx int, m app.QueuedMessage, width int, baseStyle lipgloss.Style, t theme.Theme) string {
	prefix := fmt.Sprintf("%d. ", idx+1)
	suffix := ""
	if n := len(m.Attachments); n > 0 {
		suffix = fmt.Sprintf("  (+%d attachment%s)", n, plural(n))
	}

	// Collapse whitespace so a multi-line message stays one row, and drop any
	// escape sequences the text may carry so it cannot repaint the dialog.
	preview := strings.Join(strings.Fields(ansi.Strip(m.Text)), " ")
	if preview == "" {
		preview = "(empty)"
	}
	budget := max(8, width-lipgloss.Width(prefix)-lipgloss.Width(suffix))
	preview = ansi.Truncate(preview, budget, "…")

	return lipgloss.JoinHorizontal(
		lipgloss.Left,
		baseStyle.Foreground(t.TextMuted()).Render(prefix),
		baseStyle.Foreground(t.Text()).Render(preview),
		baseStyle.Foreground(t.TextMuted()).Render(suffix),
	)
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func (d *queueDialogCmp) BindingKeys() []key.Binding {
	return []key.Binding{queueDialogKeys.Close, queueDialogKeys.Discard}
}

func NewQueueDialogCmp(app *app.App) QueueDialog {
	return &queueDialogCmp{app: app}
}
