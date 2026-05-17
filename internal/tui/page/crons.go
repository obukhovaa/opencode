package page

import (
	"context"
	"fmt"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/opencode-ai/opencode/internal/cron"
	"github.com/opencode-ai/opencode/internal/pubsub"
	"github.com/opencode-ai/opencode/internal/tui/components/chat"
	"github.com/opencode-ai/opencode/internal/tui/styles"
	"github.com/opencode-ai/opencode/internal/tui/theme"
)

var CronsPage PageID = "crons"

type cronsPage struct {
	width, height int
	cronService   cron.Service
	sessionID     string
	jobs          []cron.CronJob
	selected      int
	ctx           context.Context
	cancel        context.CancelFunc
	updatesCh     <-chan pubsub.Event[cron.CronJob]
}

func (p *cronsPage) Init() tea.Cmd {
	p.ctx, p.cancel = context.WithCancel(context.Background())
	if p.cronService != nil {
		p.updatesCh = p.cronService.Subscribe(p.ctx)
	}
	return tea.Batch(p.reloadCmd(), p.waitForUpdate())
}

type cronJobsLoadedMsg struct {
	jobs []cron.CronJob
}

type cronJobDeletedMsg struct {
	id string
}

func (p *cronsPage) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		p.width = msg.Width
		p.height = msg.Height
		return p, nil
	case chat.SessionSelectedMsg:
		if msg.ID != p.sessionID {
			p.sessionID = msg.ID
			p.selected = 0
			return p, p.reloadCmd()
		}
		return p, nil
	case chat.SessionClearedMsg:
		p.sessionID = ""
		p.jobs = nil
		p.selected = 0
		return p, nil
	case cronJobsLoadedMsg:
		p.jobs = msg.jobs
		if p.selected >= len(p.jobs) {
			p.selected = max(0, len(p.jobs)-1)
		}
		return p, nil
	case cronJobDeletedMsg:
		return p, p.reloadCmd()
	case pubsub.Event[cron.CronJob]:
		// Re-arm the listener on the same long-lived subscription channel so
		// each event triggers exactly one new tea.Cmd. Subscribing here would
		// leak orphan channels in the broker because p.ctx is never cancelled
		// until the page itself goes away.
		var cmds []tea.Cmd
		if msg.Payload.SessionID == p.sessionID {
			cmds = append(cmds, p.reloadCmd())
		}
		cmds = append(cmds, p.waitForUpdate())
		return p, tea.Batch(cmds...)
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, key.NewBinding(key.WithKeys("up", "k"))):
			if p.selected > 0 {
				p.selected--
			}
		case key.Matches(msg, key.NewBinding(key.WithKeys("down", "j"))):
			if p.selected < len(p.jobs)-1 {
				p.selected++
			}
		case key.Matches(msg, key.NewBinding(key.WithKeys("d", "delete"))):
			if p.selected < len(p.jobs) {
				job := p.jobs[p.selected]
				return p, func() tea.Msg {
					if p.cronService != nil {
						_ = p.cronService.Delete(p.ctx, job.ID)
					}
					return cronJobDeletedMsg{id: job.ID}
				}
			}
		}
	}
	return p, nil
}

func (p *cronsPage) reloadCmd() tea.Cmd {
	sessionID := p.sessionID
	return func() tea.Msg {
		if p.cronService == nil || sessionID == "" {
			return cronJobsLoadedMsg{}
		}
		jobs, err := p.cronService.List(p.ctx, sessionID)
		if err != nil {
			return cronJobsLoadedMsg{}
		}
		return cronJobsLoadedMsg{jobs: jobs}
	}
}

func (p *cronsPage) waitForUpdate() tea.Cmd {
	if p.updatesCh == nil {
		return nil
	}
	return func() tea.Msg {
		event, ok := <-p.updatesCh
		if !ok {
			return nil
		}
		return event
	}
}

func (p *cronsPage) View() tea.View {
	t := theme.CurrentTheme()
	style := styles.BaseStyle().Width(p.width).Height(p.height)

	if p.cronService == nil {
		return tea.NewView(style.Render(
			lipgloss.NewStyle().
				Padding(1, 2).
				Foreground(t.TextMuted()).
				Render("Cron scheduling is disabled (OPENCODE_DISABLE_CRON is set)"),
		))
	}

	if p.sessionID == "" {
		return tea.NewView(style.Render(
			lipgloss.NewStyle().
				Padding(1, 2).
				Foreground(t.TextMuted()).
				Render("Select a session to view its scheduled cron jobs."),
		))
	}

	if len(p.jobs) == 0 {
		return tea.NewView(style.Render(
			lipgloss.NewStyle().
				Padding(1, 2).
				Foreground(t.TextMuted()).
				Render("No cron jobs in this session.\nUse /loop or the croncreate tool to schedule tasks."),
		))
	}

	// Render table header
	header := lipgloss.NewStyle().Bold(true).Foreground(t.Primary()).
		Render(fmt.Sprintf("  %-12s %-14s %-8s %-8s %6s  %-8s", "ID", "Schedule", "Source", "Status", "Runs", "Next Run"))

	// Render rows
	rows := ""
	for i, job := range p.jobs {
		prefix := "  "
		rowStyle := lipgloss.NewStyle().Foreground(t.Text())
		if i == p.selected {
			prefix = "> "
			rowStyle = rowStyle.Bold(true).Foreground(t.Primary())
		}
		if job.Status == cron.StatusDone {
			rowStyle = rowStyle.Foreground(t.TextMuted())
		}

		nextRun := "—"
		if job.NextRunAt > 0 {
			nextRun = time.Unix(job.NextRunAt, 0).Format("15:04")
		}

		id := truncateRunes(job.ID, 12)
		schedule := truncateRunes(cron.CronToHuman(job.Schedule), 14)

		row := fmt.Sprintf("%s%-12s %-14s %-8s %-8s %6d  %-8s",
			prefix, id, schedule, job.Source, job.Status, job.RunCount, nextRun)
		rows += rowStyle.Render(row) + "\n"
	}

	// Detail panel for selected job
	detail := ""
	if p.selected < len(p.jobs) {
		job := p.jobs[p.selected]
		human := cron.CronToHuman(job.Schedule)
		detailStyle := lipgloss.NewStyle().Padding(0, 2).Foreground(t.Text())

		detail = detailStyle.Render(fmt.Sprintf(
			"ID: %s\nSchedule: %s (%s)\nPrompt: %s\nSubagent: %s\nCreated: %s\nLast run: %s\nRun count: %d",
			job.ID, human, job.Schedule,
			job.Prompt, job.SubagentType,
			time.Unix(job.CreatedAt, 0).Format("2006-01-02 15:04"),
			formatLastRun(job.LastRunAt),
			job.RunCount,
		))

		if job.LastResult != "" {
			result := job.LastResult
			if len(result) > 500 {
				result = result[:500] + "\n[truncated]"
			}
			detail += "\n\n" + lipgloss.NewStyle().Foreground(t.TextMuted()).Render("Latest output:\n"+result)
		}
		if job.Error != "" {
			detail += "\n\n" + lipgloss.NewStyle().Foreground(t.Error()).Render("Error: "+job.Error)
		}
	}

	footer := lipgloss.NewStyle().Foreground(t.TextMuted()).Padding(0, 2).
		Render("[d] Delete  [j/k] Navigate  [esc] Back")

	content := lipgloss.JoinVertical(lipgloss.Top,
		lipgloss.NewStyle().Padding(1, 0).Render("⏲ Cron Jobs"),
		header,
		rows,
		"",
		detail,
		"",
		footer,
	)

	return tea.NewView(style.Render(content))
}

func (p *cronsPage) BindingKeys() []key.Binding {
	return nil
}

func (p *cronsPage) GetSize() (int, int) {
	return p.width, p.height
}

func (p *cronsPage) SetSize(width int, height int) tea.Cmd {
	p.width = width
	p.height = height
	return nil
}

func NewCronsPage(cronSvc cron.Service) tea.Model {
	return &cronsPage{
		cronService: cronSvc,
	}
}

func formatLastRun(ts int64) string {
	if ts == 0 {
		return "never"
	}
	return time.Unix(ts, 0).Format("2006-01-02 15:04")
}

// truncateRunes returns s truncated to at most max runes without splitting a
// multi-byte sequence.
func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max])
}
