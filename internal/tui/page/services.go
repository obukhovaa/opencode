package page

import (
	"context"
	"sort"
	"strings"
	"sync/atomic"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/lsp"
	"github.com/opencode-ai/opencode/internal/lsp/install"
	"github.com/opencode-ai/opencode/internal/tui/components/chat"
	"github.com/opencode-ai/opencode/internal/tui/styles"
	"github.com/opencode-ai/opencode/internal/tui/theme"
)

const (
	MCPPage           PageID = "mcp"
	LSPPage           PageID = "lsp"
	ModifiedFilesPage PageID = "modified-files"
)

type serviceRow struct{ name, status, detail string }
type servicesLoaded struct {
	sessionID  string
	generation uint64
	agentID    config.AgentName
	kind       PageID
	rows       []serviceRow
	err        error
}
type serviceActionDone struct {
	kind PageID
	err  error
}
type servicesPage struct {
	app                                   *app.App
	kind                                  PageID
	width, height, selected, detailOffset int
	sessionID                             string
	rows                                  []serviceRow
	busy                                  atomic.Bool
	actionResult                          atomic.Pointer[serviceActionDone]
	generation                            uint64
	agentID                               config.AgentName
	status                                string
}

func NewServicesPage(a *app.App, kind PageID) tea.Model { return &servicesPage{app: a, kind: kind} }
func (p *servicesPage) Init() tea.Cmd                   { return p.reload() }
func (p *servicesPage) reload() tea.Cmd {
	kind, sid := p.kind, p.sessionID
	p.generation++
	generation := p.generation
	id := p.app.ActiveAgentName()
	if result := p.actionResult.Load(); result != nil {
		p.status = "Updated"
		if result.err != nil {
			p.status = result.err.Error()
		}
	}
	a := p.app
	return func() tea.Msg {
		var rows []serviceRow
		if sid != "" {
			id, _ = a.ResolveSessionAgent(context.Background(), sid)
		}
		switch kind {
		case MCPPage:
			loaded := a.MCPRegistry.LoadedServers()
			info, _ := a.Registry.Get(string(id))
			for name := range config.ResolveMCPServers() {
				state := "not connected"
				if loaded[name] {
					state = "connected"
				}
				names := a.MCPRegistry.ServerTools(name)
				sort.Strings(names)
				enabled := a.Registry.IsToolEnabled(string(id), name+"_*")
				if !enabled {
					state = "disabled for " + string(id)
				}
				if info.DeferredTools[strings.ToLower(name)+"_*"] {
					state += " · deferred"
				}
				rows = append(rows, serviceRow{name, state, strings.Join(names, "\n")})
			}
		case LSPPage:
			names := map[string]bool{}
			for _, def := range install.BuiltinServers {
				names[def.ID] = true
			}
			for name := range config.Get().LSP {
				names[name] = true
			}
			clients := a.LspService.Clients()
			for name := range names {
				state := "inactive · Enter to install/activate"
				if clients[name] != nil {
					state = "active"
					if clients[name].GetServerState() != lsp.StateReady {
						state = "error"
					}
				}
				rows = append(rows, serviceRow{name, state, "Activation uses the built-in installer when available. Servers without an installer must be installed on PATH."})
			}
		case ModifiedFilesPage:
			if sid != "" {
				files, err := a.History.ListLatestSessionTreeFiles(context.Background(), sid)
				if err != nil {
					return servicesLoaded{kind: kind, sessionID: sid, generation: generation, agentID: id, err: err}
				}
				for _, f := range files {
					rows = append(rows, serviceRow{f.Path, f.Version, f.Content})
				}
			}
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].name < rows[j].name })
		return servicesLoaded{kind: kind, sessionID: sid, generation: generation, agentID: id, rows: rows}
	}
}
func (p *servicesPage) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		p.width, p.height = msg.Width, msg.Height
	case chat.SessionSelectedMsg:
		if p.sessionID != msg.ID {
			p.rows = nil
			p.selected = 0
		}
		p.sessionID = msg.ID
		return p, p.reload()
	case chat.SessionClearedMsg:
		p.rows = nil
		p.selected = 0
		p.sessionID = ""
		return p, p.reload()
	case PageChangeMsg:
		if msg.ID == p.kind {
			return p, p.reload()
		}
	case servicesLoaded:
		if msg.kind != p.kind || msg.sessionID != p.sessionID || msg.generation != p.generation {
			return p, nil
		}
		p.rows = msg.rows
		p.agentID = msg.agentID
		p.selected = min(p.selected, max(0, len(p.rows)-1))
		if msg.err != nil {
			p.status = msg.err.Error()
		}
	case serviceActionDone:
		if msg.kind != p.kind {
			return p, nil
		}
		p.busy.Store(false)
		p.status = "Updated"
		if msg.err != nil {
			p.status = msg.err.Error()
		}
		return p, p.reload()
	case tea.KeyMsg:
		k := msg.String()
		switch k {
		case "esc":
			return p, func() tea.Msg { return PageChangeMsg{ID: ChatPage} }
		case "up", "k":
			p.selected = max(0, p.selected-1)
			p.detailOffset = 0
		case "down", "j":
			p.selected = min(max(0, len(p.rows)-1), p.selected+1)
			p.detailOffset = 0
		case "pgdown":
			p.detailOffset += max(1, p.height/3)
		case "pgup":
			p.detailOffset = max(0, p.detailOffset-max(1, p.height/3))
		case "r":
			if p.kind != MCPPage {
				return p, p.reload()
			}
			fallthrough
		case "enter", "e", "d", "f":
			if p.busy.Load() || len(p.rows) == 0 {
				return p, nil
			}
			row := p.rows[p.selected]
			kind := p.kind
			id := p.agentID
			action := ""
			if kind == MCPPage {
				switch k {
				case "r", "enter":
					action = "reconnect"
				case "e":
					action = "enable"
				case "d":
					action = "disable"
				case "f":
					action = "defer"
				}
			} else if kind != LSPPage || (k != "enter" && k != "d") {
				return p, nil
			}
			p.busy.Store(true)
			p.actionResult.Store(nil)
			p.status = "Working…"
			return p, func() tea.Msg {
				var err error
				if kind == MCPPage {
					err = p.app.SetMCPPolicy(id, row.name, action)
				} else {
					if k == "d" {
						err = p.app.DeactivateLSP(row.name)
					} else {
						err = p.app.ActivateLSP(row.name)
					}
				}
				result := &serviceActionDone{kind, err}
				p.actionResult.Store(result)
				p.busy.Store(false)
				return *result
			}
		}
	}
	return p, nil
}
func (p *servicesPage) View() tea.View {
	t := theme.CurrentTheme()
	bg := t.Background()
	lines := []string{strings.ToUpper(string(p.kind)), ""}
	count := max(1, p.height/2-3)
	start := max(0, p.selected-count+1)
	for i := start; i < min(len(p.rows), start+count); i++ {
		prefix := "  "
		if i == p.selected {
			prefix = "> "
		}
		lines = append(lines, ansi.Truncate(ansi.Strip(prefix+p.rows[i].name+"  "+p.rows[i].status), max(1, p.width-4), "…"))
	}
	if len(p.rows) == 0 {
		lines = append(lines, "No entries.")
	}
	if p.selected < len(p.rows) {
		lines = append(lines, "")
		details := strings.Split(p.rows[p.selected].detail, "\n")
		offset := min(p.detailOffset, max(0, len(details)-1))
		for _, line := range details[offset:min(len(details), offset+max(1, p.height-len(lines)-4))] {
			lines = append(lines, ansi.Truncate(ansi.Strip(line), max(1, p.width-4), "…"))
		}
	}
	hint := "↑/↓ j/k select · PgUp/PgDn details · Esc chat"
	if p.kind == MCPPage {
		hint += " · r reconnect · e enable · d disable · f defer"
	}
	if p.kind == LSPPage {
		hint += " · Enter install/activate · d deactivate"
	}
	lines = append(lines, "", ansi.Truncate(ansi.Strip(p.status), max(1, p.width), "…"), ansi.Truncate(hint, max(1, p.width), "…"))
	rendered := lipgloss.NewStyle().Width(max(1, p.width)).Height(max(1, p.height)).MaxHeight(max(1, p.height)).MaxWidth(max(1, p.width)).Foreground(t.Text()).Background(bg).Render(strings.Join(lines, "\n"))
	return tea.NewView(styles.ForceReplaceBackgroundWithLipgloss(rendered, bg))
}
func (p *servicesPage) SetSize(w, h int) tea.Cmd   { p.width, p.height = w, h; return nil }
func (p *servicesPage) GetSize() (int, int)        { return p.width, p.height }
func (p *servicesPage) BindingKeys() []key.Binding { return nil }
