package dialog

import (
	"context"
	"fmt"
	"strings"
	"time"

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

// QueueDialog previews ordinary chat messages and durable flow steering.
// Ctrl+X discards ordinary chat messages, mirroring the chat page binding.
// Flow steering is read-only here; its lifecycle belongs to the flow runner.
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
	generation    uint64
	flowMessages  []app.FlowSteeringMessage
	flowErr       error
	flowLoading   bool
}

type queueFlowLoaded struct {
	sessionID  string
	generation uint64
	messages   []app.FlowSteeringMessage
	err        error
}
type queueFlowTick struct{ generation uint64 }

func (d *queueDialogCmp) loadFlowMessages() tea.Cmd {
	id, generation, application := d.sessionID, d.generation, d.app
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		messages, err := application.PendingFlowMessages(ctx, id)
		return queueFlowLoaded{id, generation, messages, err}
	}
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
		key.WithHelp("ctrl+x", "discard queued chat messages"),
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
	d.generation++
	d.flowMessages = nil
	d.flowErr = nil
	d.flowLoading = true
}

func (d *queueDialogCmp) SetSize(width, height int) {
	d.width, d.height = width, height
}

func (d *queueDialogCmp) Init() tea.Cmd {
	if d.sessionID == "" {
		return nil
	}
	return d.loadFlowMessages()
}

func (d *queueDialogCmp) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case queueFlowLoaded:
		if msg.sessionID != d.sessionID || msg.generation != d.generation {
			return d, nil
		}
		d.flowLoading = false
		d.flowErr = msg.err
		if msg.err == nil {
			d.flowMessages = msg.messages
		}
		generation := d.generation
		return d, tea.Tick(time.Second, func(time.Time) tea.Msg { return queueFlowTick{generation} })
	case queueFlowTick:
		if msg.generation != d.generation {
			return d, nil
		}
		return d, d.loadFlowMessages()
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

	for _, msg := range d.flowMessages {
		queued = append(queued, app.QueuedMessage{Text: "[flow] " + msg.Text})
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

	footer := "esc close · ctrl+x discard all"
	if len(d.flowMessages) > 0 {
		lines = append(lines, "", baseStyle.Foreground(t.TextMuted()).Render("Flow messages wait for the next step/iteration boundary."))
		footer = "esc close · ctrl+x discard chat only"
	}
	if d.flowLoading {
		lines = append(lines, baseStyle.Foreground(t.TextMuted()).Render("Loading flow queue…"))
	}
	if d.flowErr != nil {
		lines = append(lines, baseStyle.Foreground(t.Error()).Render("Flow queue unavailable; retrying…"))
	}
	lines = append(lines, "", baseStyle.Foreground(t.TextMuted()).Render(footer))

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
// subtracted rows allow for borders, padding, header, footer and flow status.
func (d *queueDialogCmp) maxEntries() int {
	n := queueDialogMaxEntries
	if d.height > 0 {
		if avail := d.height - 12; avail < n {
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
