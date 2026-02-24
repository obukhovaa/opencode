package app

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/flow"
	"github.com/opencode-ai/opencode/internal/history"
	"github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/lsp"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/session"
	"github.com/opencode-ai/opencode/internal/tui/theme"
)

type App struct {
	Sessions     session.Service
	Messages     message.Service
	History      history.Service
	Permissions  permission.Service
	Registry     agentregistry.Registry
	MCPRegistry  agent.MCPRegistry
	Flows        flow.Service
	AgentFactory agent.AgentFactory
	LspService   lsp.LspService

	activeAgent agent.Service

	PrimaryAgents    map[config.AgentName]agent.Service
	PrimaryAgentKeys []config.AgentName
	ActiveAgentIdx   int

	InitialSession   *session.Session
	InitialSessionID string

	cliOutputSchema map[string]any
}

func (app *App) ActiveAgent() agent.Service {
	if len(app.PrimaryAgentKeys) == 0 {
		return app.activeAgent
	}
	name := app.PrimaryAgentKeys[app.ActiveAgentIdx]
	return app.PrimaryAgents[name]
}

func (app *App) ActiveAgentName() config.AgentName {
	if len(app.PrimaryAgentKeys) == 0 {
		return config.AgentCoder
	}
	return app.PrimaryAgentKeys[app.ActiveAgentIdx]
}

func (app *App) SwitchAgent() config.AgentName {
	if len(app.PrimaryAgentKeys) <= 1 {
		return app.ActiveAgentName()
	}
	app.ActiveAgentIdx = (app.ActiveAgentIdx + 1) % len(app.PrimaryAgentKeys)
	name := app.PrimaryAgentKeys[app.ActiveAgentIdx]
	app.activeAgent = app.PrimaryAgents[name]
	return name
}

func (app *App) SetActiveAgent(agentID config.AgentName) error {
	for i, key := range app.PrimaryAgentKeys {
		if key == agentID {
			app.ActiveAgentIdx = i
			app.activeAgent = app.PrimaryAgents[key]
			return nil
		}
	}
	return fmt.Errorf("agent %q not found among primary agents", agentID)
}

func New(ctx context.Context, conn *sql.DB, cliSchema map[string]any) (*App, error) {
	q := db.NewQuerier(conn)
	sessions := session.NewService(q)
	messages := message.NewService(q)
	files := history.NewService(q, conn)
	reg := agentregistry.GetRegistry()
	perm := permission.NewPermissionService()
	lspSvc := NewLspService()
	mcpRegistry := agent.NewMCPRegistry(perm, reg)
	factory := agent.NewAgentFactory(sessions, messages, perm, files, lspSvc, reg, mcpRegistry)
	flows := flow.NewService(sessions, q, perm, factory)

	app := &App{
		Sessions:      sessions,
		Messages:      messages,
		History:       files,
		Permissions:   perm,
		Registry:      reg,
		LspService:    lspSvc,
		PrimaryAgents: make(map[config.AgentName]agent.Service),
		MCPRegistry:   mcpRegistry,
		AgentFactory:  factory,
		Flows:         flows,
	}

	app.initTheme()
	// start lsp in background
	go lspSvc.Init(ctx)

	primaryAgents, err := factory.InitPrimaryAgents(ctx, cliSchema)
	if err != nil {
		return nil, err
	}
	for _, primaryAgent := range primaryAgents {
		app.PrimaryAgents[primaryAgent.AgentID()] = primaryAgent
		app.PrimaryAgentKeys = append(app.PrimaryAgentKeys, primaryAgent.AgentID())
		if app.activeAgent == nil {
			app.activeAgent = primaryAgent
		}
	}
	return app, nil
}

// initTheme sets the application theme based on the configuration
func (app *App) initTheme() {
	cfg := config.Get()
	if cfg == nil || cfg.TUI.Theme == "" {
		return
	}

	err := theme.SetTheme(cfg.TUI.Theme)
	if err != nil {
		logging.Warn("Failed to set theme from config, using default theme", "theme", cfg.TUI.Theme, "error", err)
	} else {
		logging.Debug("Set theme from config", "theme", cfg.TUI.Theme)
	}
}

// Shutdown performs a clean shutdown of the application
func (app *App) Shutdown() {
	tools.CleanupTempDir()
	app.LspService.Shutdown(context.Background())
}

// ForceShutdown performs an aggressive shutdown for non-interactive mode
func (app *App) ForceShutdown() {
	logging.Info("Starting force shutdown")
	tools.CleanupTempDir()
	app.LspService.ForceShutdown()
	app.forceKillAllChildProcesses()
	logging.Info("Force shutdown completed")
}

// forceKillAllChildProcesses kills all child processes of the current process
func (app *App) forceKillAllChildProcesses() {
	currentPID := os.Getpid()

	cmd := exec.Command("pgrep", "-P", strconv.Itoa(currentPID))
	output, err := cmd.Output()
	if err != nil {
		return
	}

	pidStrings := strings.FieldsSeq(string(output))
	for pidStr := range pidStrings {
		if pid, err := strconv.Atoi(pidStr); err == nil {
			process, err := os.FindProcess(pid)
			if err == nil {
				logging.Debug("Force killing child process", "pid", pid)
				process.Kill()
			}
		}
	}
}
