package page

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/task"
	"github.com/opencode-ai/opencode/internal/tui/components/chat"
	"github.com/opencode-ai/opencode/internal/tui/styles"
	"github.com/opencode-ai/opencode/internal/tui/theme"
	"github.com/opencode-ai/opencode/internal/tui/util"
)

var TasksPage PageID = "tasks"
var SubagentsPage PageID = "subagents"

type activity struct {
	id, sessionID, title, state, detail string
	subagent                            bool
}
type activitiesLoaded struct {
	sessionID  string
	generation uint64
	items      []activity
	err        error
}
type activityTick struct{ generation uint64 }

// All storage and filesystem reads happen in commands, never View or key handling.
func loadActivities(a *app.App, sessionID, detailID string, width int, generation uint64) tea.Cmd {
	return func() tea.Msg {
		result := activitiesLoaded{sessionID: sessionID, generation: generation}
		if sessionID == "" {
			return result
		}
		ctx := context.Background()
		inputs := map[string]string{}
		if detailID != "" {
			parents, err := a.Messages.List(ctx, sessionID)
			if err != nil {
				result.err = err
				return result
			}
			for _, m := range parents {
				for _, c := range m.ToolCalls() {
					inputs[c.ID] = c.Input
				}
			}
		}
		children, err := a.Sessions.ListChildren(ctx, sessionID)
		if err != nil {
			result.err = err
			return result
		}
		scope := []string{sessionID}
		for _, child := range children {
			scope = append(scope, child.ID)
		}
		registered := []*task.Task{}
		taskIDs := map[string]bool{}
		registry := task.GlobalRegistry()
		for _, id := range scope {
			if registry == nil {
				break
			}
			for _, t := range registry.ListBySessionTree(id) {
				if !taskIDs[t.ID] {
					taskIDs[t.ID] = true
					registered = append(registered, t)
				}
			}
		}
		seen := map[string]bool{}
		for _, t := range registered {
			item := activity{id: t.ID, sessionID: t.AgentSessionID, title: t.Description, state: t.State().String(), subagent: t.Kind == task.KindTask || t.Kind == task.KindCron}
			item.detail = fmt.Sprintf("%s\n%s\n%s\n", t.ID, t.Kind, inputs[t.OriginatingToolCallID])
			if item.id == detailID && t.OutputPath != "" {
				item.detail += "\nOutput: " + t.OutputPath + "\n"
				item.detail += readActivityTail(t.OutputPath)
			}
			if item.sessionID != "" {
				seen[item.sessionID] = true
				if item.id == detailID {
					item.detail += activityTranscript(a, item.sessionID, width)
				}
			}
			result.items = append(result.items, item)
		}
		for _, child := range children {
			if child.ParentSessionID == "" || seen[child.ID] {
				continue
			}
			state := "completed"
			if agent.SessionBusy(child.ID) {
				state = "running"
			}
			item := activity{id: child.ID, sessionID: child.ID, title: child.Title, state: state, subagent: true, detail: inputs[child.ID]}
			if item.id == detailID {
				item.detail += activityTranscript(a, child.ID, width)
			}
			result.items = append(result.items, item)
		}
		sort.SliceStable(result.items, func(i, j int) bool {
			a, b := result.items[i], result.items[j]
			if (a.state == "running") != (b.state == "running") {
				return a.state == "running"
			}
			return a.id < b.id
		})
		return result
	}
}
func readActivityTail(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return err.Error()
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err.Error()
	}
	if st.Size() > 32768 {
		_, _ = f.Seek(-32768, io.SeekEnd)
	}
	b, err := io.ReadAll(io.LimitReader(f, 32768))
	if err != nil {
		return err.Error()
	}
	return ansi.Strip(string(b))
}
func activityTranscript(a *app.App, id string, width int) string {
	msgs, err := a.Messages.ListLatest(context.Background(), id, 100)
	if err != nil {
		return err.Error()
	}
	return chat.RenderTranscript(msgs, max(20, width))
}

type activitiesPage struct {
	app           *app.App
	sessionID     string
	subagents     bool
	items         []activity
	selected      int
	detail        bool
	width, height int
	viewport      viewport.Model
	loading       bool
	generation    uint64
}

func NewActivitiesPage(a *app.App, subagents bool) tea.Model {
	return &activitiesPage{app: a, subagents: subagents, viewport: viewport.New()}
}
func (p *activitiesPage) Init() tea.Cmd { return nil }
func (p *activitiesPage) refresh() tea.Cmd {
	if p.loading {
		return nil
	}
	p.loading = true
	detailID := ""
	if p.detail && p.selected < len(p.items) {
		detailID = p.items[p.selected].id
	}
	p.generation++
	return loadActivities(p.app, p.sessionID, detailID, p.width, p.generation)
}
func (p *activitiesPage) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {
	case chat.SessionSelectedMsg:
		p.sessionID = m.ID
		p.loading = false
		p.items = nil
		p.selected = 0
		return p, p.refresh()
	case chat.SessionClearedMsg:
		p.sessionID = ""
		p.generation++
		p.loading = false
		p.items = nil
		return p, nil
	case activityTick:
		if m.generation != 0 && m.generation != p.generation {
			return p, nil
		}
		return p, p.refresh()
	case activitiesLoaded:
		if m.sessionID != p.sessionID || m.generation != p.generation {
			return p, nil
		}
		p.loading = false
		if m.err != nil {
			return p, util.ReportError(m.err)
		}
		selectedID := ""
		if p.selected < len(p.items) {
			selectedID = p.items[p.selected].id
		}
		p.items = nil
		for _, item := range m.items {
			if item.subagent == p.subagents {
				p.items = append(p.items, item)
			}
		}
		p.selected = min(p.selected, max(0, len(p.items)-1))
		for i, item := range p.items {
			if item.id == selectedID {
				p.selected = i
			}
		}
		p.updateDetail()
		return p, tea.Tick(time.Second, func(time.Time) tea.Msg { return activityTick{generation: m.generation} })
	case tea.KeyPressMsg:
		switch m.String() {
		case "esc":
			if p.detail {
				p.detail = false
				return p, nil
			}
			return p, util.CmdHandler(PageChangeMsg{ID: ChatPage})
		case "enter":
			p.detail = true
			p.updateDetail()
			return p, p.refresh()
		case "x":
			if len(p.items) == 0 {
				return p, nil
			}
			item := p.items[p.selected]
			a := p.app
			return p, func() tea.Msg {
				if _, ok := task.GlobalRegistry().Get(item.id); ok {
					if err := task.GlobalRegistry().Kill(item.id); err != nil {
						return util.ReportError(err)()
					}
				} else {
					a.ActiveAgent().Cancel(item.sessionID)
				}
				return activityTick{}
			}
		case "up", "k":
			if !p.detail {
				p.selected = max(0, p.selected-1)
				return p, nil
			}
		case "down", "j":
			if !p.detail {
				p.selected = min(max(0, len(p.items)-1), p.selected+1)
				return p, nil
			}
		}
	}
	if p.detail {
		var cmd tea.Cmd
		p.viewport, cmd = p.viewport.Update(msg)
		return p, cmd
	}
	return p, nil
}
func (p *activitiesPage) updateDetail() {
	if len(p.items) > 0 {
		offset := p.viewport.YOffset()
		p.viewport.SetContent(p.items[p.selected].detail)
		p.viewport.SetYOffset(offset)
	}
}
func (p *activitiesPage) View() tea.View {
	title := "Background tasks"
	if p.subagents {
		title = "Subagents"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s — Enter details · x cancel · Esc back\n\n", title)
	if p.detail {
		b.WriteString(p.viewport.View())
	} else {
		if len(p.items) == 0 {
			b.WriteString("No activities in this session.")
		}
		start := max(0, p.selected-max(1, p.height-5)+1)
		for i := start; i < len(p.items) && i < start+max(1, p.height-4); i++ {
			item := p.items[i]
			mark := " "
			if i == p.selected {
				mark = ">"
			}
			fmt.Fprintf(&b, "%s %s  %s\n", mark, item.state, ansi.Truncate(ansi.Strip(item.title), max(1, p.width-16), "…"))
		}
	}
	rendered := styles.BaseStyle().Width(p.width).Height(p.height).MaxHeight(p.height).Render(b.String())
	return tea.NewView(styles.ForceReplaceBackgroundWithLipgloss(rendered, theme.CurrentTheme().Background()))
}
func (p *activitiesPage) SetSize(w, h int) tea.Cmd {
	p.width = w
	p.height = h
	p.viewport.SetWidth(w)
	p.viewport.SetHeight(max(1, h-3))
	return nil
}
func (p *activitiesPage) GetSize() (int, int)        { return p.width, p.height }
func (p *activitiesPage) BindingKeys() []key.Binding { return nil }
