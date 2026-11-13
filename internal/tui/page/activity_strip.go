package page

import (
	"context"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/task"
	"github.com/opencode-ai/opencode/internal/tui/components/chat"
	"github.com/opencode-ai/opencode/internal/tui/layout"
	"github.com/opencode-ai/opencode/internal/tui/styles"
	"github.com/opencode-ai/opencode/internal/tui/util"
)

type ActivateActivitiesMsg struct{}
type stripTick struct{ generation uint64 }
type stripCounts struct {
	sessionID     string
	generation    uint64
	tasks, agents int
	flowQueued    int
	flowErr       error
}

func activityCounts(a *app.App, id string, generation uint64) tea.Cmd {
	return func() tea.Msg {
		result := stripCounts{sessionID: id, generation: generation}
		if id == "" {
			return result
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		pending, err := a.PendingFlowMessages(ctx, id)
		result.flowQueued, result.flowErr = len(pending), err
		children, err := a.Sessions.ListChildren(context.Background(), id)
		scope := []string{id}
		if err == nil {
			for _, child := range children {
				scope = append(scope, child.ID)
			}
		}
		seen := map[string]bool{}
		taskIDs := map[string]bool{}
		registry := task.GlobalRegistry()
		if registry != nil {
			for _, sessionID := range scope {
				for _, t := range registry.PendingForSessionTree(sessionID, nil) {
					if taskIDs[t.ID] {
						continue
					}
					taskIDs[t.ID] = true
					if t.Kind == task.KindTask || t.Kind == task.KindCron {
						result.agents++
						seen[t.AgentSessionID] = true
					} else {
						result.tasks++
					}
				}
			}
		}
		if err == nil {
			for _, child := range children {
				if child.ParentSessionID != "" && !seen[child.ID] && agent.SessionBusy(child.ID) {
					result.agents++
				}
			}
		}

		return result
	}
}
func (p *chatPage) activityStrip(view string) string {
	if p.activityTasks+p.activityAgents == 0 && p.activityFlowQueued == 0 && !p.app.FlowBusy(p.session.ID) {
		return view
	}
	labels := []string{fmt.Sprintf("Tasks %d", p.activityTasks), fmt.Sprintf("Subagents %d", p.activityAgents)}
	if p.activityFocused && p.activitySelected < 2 {
		labels[p.activitySelected] = "[" + labels[p.activitySelected] + "]"
	}
	text := labels[0] + "  ·  " + labels[1]
	if p.app.FlowBusy(p.session.ID) {
		if p.activityFocused && p.activitySelected == 2 {
			text += "  ·  [Flow running]"
		} else {
			text += "  ·  Flow running"
		}
	}
	if n := p.app.QueueLen(p.session.ID); n > 0 {
		text += fmt.Sprintf("  ·  %d queued", n)
	}
	if p.activityFlowQueued > 0 {
		text += fmt.Sprintf("  ·  %d flow queued (ctrl+g)", p.activityFlowQueued)
	}
	if p.activityFocused {
		text = "> " + text + "  ←/→ select · Enter open · ↓ back"
	} else {
		text += "  ↑ inspect"
	}
	w, h := p.layout.GetSize()
	_, eh := p.editor.GetSize()
	return layout.PlaceOverlay(0, max(0, h-eh-1), styles.BaseStyle().Width(w).MaxHeight(1).Render(text), view, false)
}
func (p *chatPage) activityKey(k string) (tea.Cmd, bool) {
	if !p.activityFocused {
		return nil, false
	}
	switch k {
	case "esc", "down", "j":
		p.activityFocused = false
	case "left", "h":
		p.activitySelected = max(0, p.activitySelected-1)
	case "right", "l":
		limit := 1
		if p.app.FlowBusy(p.session.ID) {
			limit = 2
		}
		p.activitySelected = min(limit, p.activitySelected+1)
	case "enter":
		id := TasksPage
		if p.activitySelected == 1 {
			id = SubagentsPage
		}
		if p.activitySelected == 2 {
			id = FlowsPage
		}
		p.activityFocused = false
		return util.CmdHandler(PageChangeMsg{ID: id}), true
	}
	return nil, true
}
func (p *chatPage) activityUpdate(msg tea.Msg) (tea.Cmd, bool) {
	switch m := msg.(type) {
	case chat.FocusActivitiesMsg:
		if p.activityTasks+p.activityAgents > 0 || p.app.FlowBusy(p.session.ID) {
			p.activityFocused = true
		}
		return nil, true
	case ActivateActivitiesMsg:
		return p.refreshActivityCounts(), true
	case stripTick:
		if m.generation != p.stripGeneration {
			return nil, true
		}
		return p.refreshActivityCounts(), true
	case stripCounts:
		if m.generation != p.stripGeneration {
			return nil, true
		}
		if m.sessionID == p.session.ID {
			p.activityTasks = m.tasks
			p.activityAgents = m.agents
			if m.flowErr == nil {
				p.activityFlowQueued = m.flowQueued
			}
			if m.tasks+m.agents == 0 && !p.app.FlowBusy(p.session.ID) {
				p.activityFocused = false
			}
		}
		return tea.Tick(time.Second, func(time.Time) tea.Msg { return stripTick{generation: m.generation} }), true
	}
	return nil, false
}

func (p *chatPage) refreshActivityCounts() tea.Cmd {
	p.stripGeneration++
	return activityCounts(p.app, p.session.ID, p.stripGeneration)
}
