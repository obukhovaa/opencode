package page

import (
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/flow"
	"github.com/opencode-ai/opencode/internal/tui/components/chat"
	"github.com/opencode-ai/opencode/internal/tui/styles"
)

const FlowsPage PageID = "flows"

type StartDynamicFlowMsg struct{ Prompt string }

type flowController interface {
	DynamicFlowSnapshot(context.Context, string) (app.FlowSnapshot, error)
	ControlFlow(context.Context, string, string, string) error
}

type flowsPage struct {
	controller              flowController
	root                    string
	width, height, selected int
	generation              uint64
	snapshot                app.FlowSnapshot
	err                     error
	choosing                bool
	viewport                viewport.Model
}
type flowSnapshotMsg struct {
	root       string
	generation uint64
	snapshot   app.FlowSnapshot
	err        error
}
type flowRefreshMsg struct {
	root       string
	generation uint64
}
type flowControlMsg struct {
	root       string
	generation uint64
	err        error
}

func NewFlowsPage(controller flowController) tea.Model {
	return &flowsPage{controller: controller, viewport: viewport.New()}
}
func (p *flowsPage) Init() tea.Cmd       { return nil }
func (p *flowsPage) GetSize() (int, int) { return p.width, p.height }
func (p *flowsPage) SetSize(w, h int) tea.Cmd {
	p.width, p.height = w, h
	g := pageDimensions(w, h)
	_, _, dw, dh, _ := pageSplit(g.width, g.bodyHeight)
	p.viewport.SoftWrap = true
	p.viewport.SetWidth(max(1, dw-4))
	p.viewport.SetHeight(max(1, dh-2))
	p.renderContent()
	return nil
}
func (p *flowsPage) reload() tea.Cmd {
	root, generation, controller := p.root, p.generation, p.controller
	return func() tea.Msg {
		if root == "" || controller == nil {
			return flowSnapshotMsg{root: root, generation: generation}
		}
		snapshot, err := controller.DynamicFlowSnapshot(context.Background(), root)
		return flowSnapshotMsg{root: root, generation: generation, snapshot: snapshot, err: err}
	}
}
func (p *flowsPage) control(action string) tea.Cmd {
	if p.root == "" || p.controller == nil {
		return nil
	}
	root, generation, controller := p.root, p.generation, p.controller
	step := ""
	if p.selected < len(p.snapshot.Steps) {
		step = p.snapshot.Steps[p.selected].ID
	}
	return func() tea.Msg {
		return flowControlMsg{root: root, generation: generation, err: controller.ControlFlow(context.Background(), root, step, action)}
	}
}
func (p *flowsPage) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		return p, p.SetSize(msg.Width, msg.Height)
	case chat.SessionSelectedMsg:
		p.root = msg.ID
		p.generation++
		p.snapshot = app.FlowSnapshot{}
		p.selected = 0
		p.choosing = false
		p.err = nil
		p.renderContent()
		return p, p.reload()
	case chat.SessionClearedMsg:
		p.root = ""
		p.generation++
		p.snapshot = app.FlowSnapshot{}
		p.renderContent()
		return p, nil
	case flowSnapshotMsg:
		if msg.root != p.root || msg.generation != p.generation {
			return p, nil
		}
		p.snapshot, p.err = msg.snapshot, msg.err
		p.selected = min(p.selected, max(0, len(p.snapshot.Steps)-1))
		p.renderContent()
		root, generation := p.root, p.generation
		return p, tea.Tick(time.Second, func(time.Time) tea.Msg { return flowRefreshMsg{root, generation} })
	case flowRefreshMsg:
		if msg.root == p.root && msg.generation == p.generation {
			return p, p.reload()
		}
		return p, nil
	case flowControlMsg:
		if msg.root != p.root || msg.generation != p.generation {
			return p, nil
		}
		p.err = msg.err
		p.renderContent()
		if msg.err == nil {
			p.generation++
			return p, p.reload()
		}
		return p, nil
	case tea.KeyPressMsg:
		if p.choosing {
			p.choosing = false
			switch msg.String() {
			case "r":
				return p, p.control("resume")
			case "n":
				return p, p.control("skip")
			case "t":
				return p, p.control("terminate")
			}
			p.renderContent()
			return p, nil
		}
		switch msg.String() {
		case "esc":
			return p, func() tea.Msg { return PageChangeMsg{ID: ChatPage} }
		case "up", "k":
			p.selected = max(0, p.selected-1)
			p.viewport.GotoTop()
			p.renderContent()
			return p, nil
		case "down", "j":
			p.selected = min(max(0, len(p.snapshot.Steps)-1), p.selected+1)
			p.viewport.GotoTop()
			p.renderContent()
			return p, nil
		case "c":
			p.choosing = true
			p.renderContent()
			return p, p.control("cancel")
		case "r":
			return p, p.control("resume")
		case "enter":
			p.choosing = true
			p.renderContent()
			return p, nil
		}
	}
	var cmd tea.Cmd
	p.viewport, cmd = p.viewport.Update(msg)
	return p, cmd
}
func (p *flowsPage) renderContent() {
	if p.selected >= len(p.snapshot.Steps) {
		p.viewport.SetContent("")
		return
	}
	step := p.snapshot.Steps[p.selected]
	output := step.Output
	if strings.TrimSpace(output) == "" {
		output = "Output will appear when this step runs."
	}
	output = flow.OutputMarkdown(pagePlain(output), step.IsStructOutput, 0, 0)
	if rendered, err := styles.GetMarkdownRenderer(max(1, p.viewport.Width()-2)).Render(output); err == nil {
		output = strings.TrimSpace(rendered)
	}
	content := styles.BaseStyle().Bold(true).Render("Prompt") + "\n" + pagePlain(step.Prompt) + "\n\n" + styles.BaseStyle().Bold(true).Render("Current output") + "\n" + output
	offset := p.viewport.YOffset()
	p.viewport.SetContent(content)
	p.viewport.SetYOffset(offset)
}
func (p *flowsPage) View() tea.View {
	g := pageDimensions(p.width, p.height)
	subtitle := "Follow each step, inspect its work and control execution."
	footer := "↑↓ step · Enter actions · c cancel · r resume · PgUp/PgDn output · Esc back"
	if p.choosing {
		footer = "r resume · n skip step · t terminate · any other key dismiss"
		subtitle = "Choose how to continue the selected step."
	}
	if p.err != nil {
		subtitle = "Error: " + p.err.Error()
	}
	summary := ""
	var body string
	if p.snapshot.FlowID == "" {
		title, help := "No flow in this session", "Use /flow <goal> to plan and run a workflow."
		if p.snapshot.Running {
			title, help = "Preparing your workflow", "The agent is building and validating the plan."
			summary = pageStatus(p.snapshot.Status)
		}
		body = pagePanel("Overview", pageEmpty(title, help), g.width, g.bodyHeight, false)
	} else {
		summary = pageStatus(p.snapshot.Status)
		subtitle = p.snapshot.FlowID
		if p.err != nil {
			subtitle = "Error: " + p.err.Error()
		}
		if p.choosing {
			subtitle = "Choose how to continue the selected step."
		}
		lw, lh, dw, dh, wide := pageSplit(g.width, g.bodyHeight)
		count := max(1, (lh-2)/2)
		start := max(0, p.selected-count+1)
		rows := []string{}
		for i := start; i < min(len(p.snapshot.Steps), start+count); i++ {
			step := p.snapshot.Steps[i]
			rows = append(rows, pageRow(fmt.Sprintf("%02d  %s", i+1, step.ID), step.Status, i == p.selected, lw-4))
		}
		detailTitle := "Step details"
		if p.selected < len(p.snapshot.Steps) {
			step := p.snapshot.Steps[p.selected]
			detailTitle = fmt.Sprintf("%s · iteration %d · %d agents", step.Agent, step.Iteration, step.RunningAgents)
		}
		body = pageJoin(pagePanel("Steps · "+pageCount(p.selected, len(p.snapshot.Steps)), strings.Join(rows, "\n"), lw, lh, true), pagePanel(detailTitle, p.viewport.View(), dw, dh, false), wide)
	}
	return tea.NewView(pageFrame(p.width, p.height, "Flow progress", subtitle, summary, body, footer))
}
