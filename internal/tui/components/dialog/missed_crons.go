package dialog

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/opencode-ai/opencode/internal/cron"
	"github.com/opencode-ai/opencode/internal/tui/layout"
	"github.com/opencode-ai/opencode/internal/tui/styles"
	"github.com/opencode-ai/opencode/internal/tui/theme"
	"github.com/opencode-ai/opencode/internal/tui/util"
)

// ResolveMissedCronMsg is emitted when the user picks an action for a missed
// one-shot cron. The TUI calls cron.Service.ResolveMissedOneShot in response.
type ResolveMissedCronMsg struct {
	JobID  string
	Action cron.MissedAction
}

// CloseMissedCronDialogMsg closes the dialog without resolving (e.g. ESC).
// The unresolved jobs stay in the queue; the dialog re-opens on next startup.
type CloseMissedCronDialogMsg struct{}

// MissedCronDialog renders a per-job confirmation prompt for one-shot cron
// jobs that missed their fire window while the process was down. The user
// picks Run Now / Discard / Keep For Later for each job; the dialog advances
// through the queue.
type MissedCronDialog interface {
	tea.Model
	layout.Bindings
	SetJobs(jobs []cron.CronJob)
	HasJobs() bool
}

type missedCronDialogCmp struct {
	jobs     []cron.CronJob
	idx      int
	selected int // 0=Run Now, 1=Discard, 2=Keep For Later
}

const (
	missedActionRunNow = iota
	missedActionDiscard
	missedActionKeep
)

var missedActionLabels = []string{"Run Now", "Discard", "Keep For Later"}

func (d *missedCronDialogCmp) SetJobs(jobs []cron.CronJob) {
	d.jobs = append(d.jobs, jobs...)
}

func (d *missedCronDialogCmp) HasJobs() bool {
	return d.idx < len(d.jobs)
}

func (d *missedCronDialogCmp) Init() tea.Cmd {
	return nil
}

func (d *missedCronDialogCmp) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, key.NewBinding(key.WithKeys("left", "h"))):
			if d.selected > 0 {
				d.selected--
			}
		case key.Matches(msg, key.NewBinding(key.WithKeys("right", "l", "tab"))):
			if d.selected < len(missedActionLabels)-1 {
				d.selected++
			} else {
				d.selected = 0
			}
		case key.Matches(msg, key.NewBinding(key.WithKeys("enter", " "))):
			return d, d.resolveCurrent()
		case key.Matches(msg, key.NewBinding(key.WithKeys("esc"))):
			// Reset queue but keep undelivered jobs for next session activation.
			d.jobs = d.jobs[d.idx:]
			d.idx = 0
			d.selected = 0
			return d, util.CmdHandler(CloseMissedCronDialogMsg{})
		}
	}
	return d, nil
}

func (d *missedCronDialogCmp) resolveCurrent() tea.Cmd {
	if d.idx >= len(d.jobs) {
		return util.CmdHandler(CloseMissedCronDialogMsg{})
	}
	job := d.jobs[d.idx]
	var action cron.MissedAction
	switch d.selected {
	case missedActionRunNow:
		action = cron.MissedActionRunNow
	case missedActionDiscard:
		action = cron.MissedActionDiscard
	case missedActionKeep:
		action = cron.MissedActionKeep
	}
	d.idx++
	d.selected = 0

	cmds := []tea.Cmd{util.CmdHandler(ResolveMissedCronMsg{JobID: job.ID, Action: action})}
	if d.idx >= len(d.jobs) {
		// Drain — close after the last job.
		d.jobs = nil
		d.idx = 0
		cmds = append(cmds, util.CmdHandler(CloseMissedCronDialogMsg{}))
	}
	return tea.Batch(cmds...)
}

func (d *missedCronDialogCmp) View() tea.View {
	t := theme.CurrentTheme()
	baseStyle := styles.BaseStyle()

	if d.idx >= len(d.jobs) {
		return tea.NewView(baseStyle.Render(""))
	}
	job := d.jobs[d.idx]

	header := fmt.Sprintf("⏲ Missed scheduled task (%d of %d)", d.idx+1, len(d.jobs))
	missedAt := "unknown"
	if job.NextRunAt > 0 {
		missedAt = time.Unix(job.NextRunAt, 0).Format("2006-01-02 15:04")
	}
	scheduleLine := fmt.Sprintf("Original fire: %s   Schedule: %s", missedAt, job.Schedule)
	titleLine := fmt.Sprintf("Title: %s", job.TaskTitle)

	// Wrap the prompt in a fenced code block to prevent self-inflicted prompt
	// injection — multi-line imperative prompts must not look like a directive
	// to the user reading the dialog.
	promptBlock := "```\n" + job.Prompt + "\n```"

	yesStyle := baseStyle
	noStyle := baseStyle
	keepStyle := baseStyle
	spacerStyle := baseStyle.Background(t.Background())

	highlight := func(active bool, s lipgloss.Style) lipgloss.Style {
		if active {
			return s.Background(t.Primary()).Foreground(t.Background())
		}
		return s.Background(t.Background()).Foreground(t.Primary())
	}

	yesStyle = highlight(d.selected == missedActionRunNow, yesStyle)
	noStyle = highlight(d.selected == missedActionDiscard, noStyle)
	keepStyle = highlight(d.selected == missedActionKeep, keepStyle)

	yesBtn := yesStyle.Padding(0, 1).Render(missedActionLabels[missedActionRunNow])
	noBtn := noStyle.Padding(0, 1).Render(missedActionLabels[missedActionDiscard])
	keepBtn := keepStyle.Padding(0, 1).Render(missedActionLabels[missedActionKeep])

	buttons := lipgloss.JoinHorizontal(
		lipgloss.Left,
		yesBtn,
		spacerStyle.Render("  "),
		noBtn,
		spacerStyle.Render("  "),
		keepBtn,
	)

	bg := t.Background()
	content := baseStyle.Render(
		lipgloss.JoinVertical(
			lipgloss.Left,
			header,
			"",
			titleLine,
			scheduleLine,
			"",
			promptBlock,
			"",
			buttons,
		),
	)

	width := lipgloss.Width(content)
	if w := lipgloss.Width(scheduleLine); w > width {
		width = w
	}
	if w := lipgloss.Width(strings.Repeat("a", 60)); w > width {
		width = w
	}

	rendered := baseStyle.Padding(1, 2).
		Border(lipgloss.RoundedBorder()).
		BorderBackground(bg).
		BorderForeground(t.TextMuted()).
		Width(width + 6).
		Render(content)

	return tea.NewView(styles.ForceReplaceBackgroundWithLipgloss(rendered, bg))
}

func (d *missedCronDialogCmp) BindingKeys() []key.Binding {
	return []key.Binding{
		key.NewBinding(key.WithKeys("left", "h"), key.WithHelp("←/h", "previous")),
		key.NewBinding(key.WithKeys("right", "l", "tab"), key.WithHelp("→/l/tab", "next")),
		key.NewBinding(key.WithKeys("enter", " "), key.WithHelp("enter/space", "confirm")),
		key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "defer")),
	}
}

func NewMissedCronDialog() MissedCronDialog {
	return &missedCronDialogCmp{}
}
