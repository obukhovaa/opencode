package page

import (
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/tui/components/chat"
	"github.com/opencode-ai/opencode/internal/tui/styles"
	"github.com/opencode-ai/opencode/internal/tui/theme"
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
	p.viewport.SetWidth(max(1, w-4))
	p.viewport.SetHeight(max(1, h-5))
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
		case "up", "k":
			p.selected = max(0, p.selected-1)
			p.renderContent()
			return p, nil
		case "down", "j":
			p.selected = min(max(0, len(p.snapshot.Steps)-1), p.selected+1)
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
	var b strings.Builder
	if p.err != nil {
		fmt.Fprintf(&b, "Error: %v\n\n", p.err)
	}
	if p.snapshot.FlowID == "" && p.snapshot.Running {
		fmt.Fprintf(&b, "Flow %s. The agent is preparing and validating the workflow.\n", p.snapshot.Status)
	} else if p.snapshot.FlowID == "" {
		b.WriteString("No flow in this session. Start one with /flow <goal>.")
	} else {
		fmt.Fprintf(&b, "%s  [%s]\n\n", p.snapshot.FlowID, p.snapshot.Status)
		for i, step := range p.snapshot.Steps {
			marker := "  "
			if i == p.selected {
				marker = "> "
			}
			fmt.Fprintf(&b, "%s%s  %s  agent: %s  iteration: %d  running agents: %d\n", marker, step.ID, step.Status, step.Agent, step.Iteration, step.RunningAgents)
		}
		if p.selected < len(p.snapshot.Steps) {
			step := p.snapshot.Steps[p.selected]
			fmt.Fprintf(&b, "\nPrompt\n%s\n\nCurrent output\n%s", step.Prompt, step.Output)
		}
	}
	p.viewport.SetContent(b.String())
}
func (p *flowsPage) View() tea.View {
	footer := "↑/↓ select step · c cancel step · Enter actions · r resume · PgUp/PgDown scroll · Esc back"
	if p.choosing {
		footer = "Choose next action: r resume · n skip step · t terminate · any other key dismiss"
	}
	content := "Flow progress\n\n" + p.viewport.View() + "\n" + footer
	return tea.NewView(styles.ForceReplaceBackgroundWithLipgloss(styles.BaseStyle().Width(p.width).Height(p.height).Render(content), theme.CurrentTheme().Background()))
}
