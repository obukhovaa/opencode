package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/format"
	"github.com/opencode-ai/opencode/internal/history"
	"github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/lsp"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/session"
	"github.com/opencode-ai/opencode/internal/tui/theme"
)

type App struct {
	Sessions    session.Service
	Messages    message.Service
	History     history.Service
	Permissions permission.Service
	Registry    agentregistry.Registry
	MCPRegistry agent.MCPRegistry

	activeAgent agent.Service

	PrimaryAgents    map[config.AgentName]agent.Service
	PrimaryAgentKeys []config.AgentName
	ActiveAgentIdx   int

	InitialSession   *session.Session
	InitialSessionID string

	LSPClients            map[string]*lsp.Client
	LSPClientsCh          chan *lsp.Client
	lspClientsMutex       sync.RWMutex
	lspWatcherCancelFuncs []context.CancelFunc
	lspCancelFuncsMutex   sync.Mutex
	lspWatcherWG          sync.WaitGroup
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

func New(ctx context.Context, conn *sql.DB) (*App, error) {
	q := db.NewQuerier(conn)
	sessions := session.NewService(q)
	messages := message.NewService(q)
	files := history.NewService(q, conn)
	reg := agentregistry.GetRegistry()
	perm := permission.NewPermissionService()

	app := &App{
		Sessions:      sessions,
		Messages:      messages,
		History:       files,
		Permissions:   perm,
		Registry:      reg,
		LSPClients:    make(map[string]*lsp.Client),
		LSPClientsCh:  make(chan *lsp.Client, 50),
		PrimaryAgents: make(map[config.AgentName]agent.Service),
		MCPRegistry:   agent.NewMCPRegistry(perm, reg),
	}

	// Initialize theme based on configuration
	app.initTheme()

	// Initialize LSP clients in the background
	go app.initLSPClients(ctx)

	// Create all primary agents from registry
	primaryAgents := reg.ListByMode(config.AgentModeAgent)
	if len(primaryAgents) == 0 {
		return nil, errors.New("no primary agents found in registry")
	}

	for _, agentInfo := range primaryAgents {
		agentInfoCopy := agentInfo
		primaryAgent, err := agent.NewAgent(
			ctx,
			&agentInfoCopy,
			app.Sessions,
			app.Messages,
			app.Permissions,
			app.History,
			app.LSPClients,
			app.Registry,
			app.MCPRegistry,
		)
		if err != nil {
			logging.Error("Failed to create agent", "agent", agentInfo.ID, "error", err)
			continue
		}
		app.PrimaryAgents[agentInfo.ID] = primaryAgent
		app.PrimaryAgentKeys = append(app.PrimaryAgentKeys, agentInfo.ID)
		if app.activeAgent == nil {
			app.activeAgent = primaryAgent
		}
	}

	if app.activeAgent == nil {
		return nil, errors.New("failed to create any primary agents")
	}

	return app, nil
}

// initTheme sets the application theme based on the configuration
func (app *App) initTheme() {
	cfg := config.Get()
	if cfg == nil || cfg.TUI.Theme == "" {
		return // Use default theme
	}

	// Try to set the theme from config
	err := theme.SetTheme(cfg.TUI.Theme)
	if err != nil {
		logging.Warn("Failed to set theme from config, using default theme", "theme", cfg.TUI.Theme, "error", err)
	} else {
		logging.Debug("Set theme from config", "theme", cfg.TUI.Theme)
	}
}

// RunNonInteractive handles the execution flow when a prompt is provided via CLI flag.
func (app *App) RunNonInteractive(ctx context.Context, prompt string, outputFormat string, quiet bool) error {
	logging.Info("Running in non-interactive mode")

	// Start spinner if not in quiet mode
	var spinner *format.Spinner
	if !quiet {
		spinner = format.NewSpinner("Thinking...")
		spinner.Start()
		defer spinner.Stop()
	}

	const maxPromptLengthForTitle = 100
	titlePrefix := "Non-interactive: "
	var titleSuffix string

	if len(prompt) > maxPromptLengthForTitle {
		titleSuffix = prompt[:maxPromptLengthForTitle] + "..."
	} else {
		titleSuffix = prompt
	}
	title := titlePrefix + titleSuffix

	var sess session.Session
	if app.InitialSession != nil {
		sess = *app.InitialSession
		logging.Info("Resuming existing session for non-interactive run", "session_id", sess.ID)
	} else if app.InitialSessionID != "" {
		var createErr error
		sess, createErr = app.Sessions.CreateWithID(ctx, app.InitialSessionID, title)
		if createErr != nil {
			return fmt.Errorf("failed to create session for non-interactive mode: %w", createErr)
		}
		logging.Info("Created session with provided ID for non-interactive run", "session_id", sess.ID)
	} else {
		var createErr error
		sess, createErr = app.Sessions.Create(ctx, title)
		if createErr != nil {
			return fmt.Errorf("failed to create session for non-interactive mode: %w", createErr)
		}
		logging.Info("Created session for non-interactive run", "session_id", sess.ID)
	}

	// Automatically approve all permission requests for this non-interactive session
	app.Permissions.AutoApproveSession(sess.ID)

	done, err := app.ActiveAgent().Run(ctx, sess.ID, prompt)
	if err != nil {
		return fmt.Errorf("failed to start agent processing stream for session %s: %w", sess.ID, err)
	}

	result := <-done
	if result.Error != nil {
		if errors.Is(result.Error, context.Canceled) || errors.Is(result.Error, agent.ErrRequestCancelled) {
			logging.Warn("Agent processing cancelled", "session_id", sess.ID)
			return nil
		}
		return fmt.Errorf("agent processing failed for session %s: %w", sess.ID, result.Error)
	}

	// Stop spinner before printing output
	if !quiet && spinner != nil {
		spinner.Stop()
	}

	// Get the text content from the response
	content := "No content available"
	if result.Message.Content().String() != "" {
		content = result.Message.Content().String()
	}

	fmt.Println(format.FormatOutput(content, outputFormat))

	logging.Info("Non-interactive run completed", "session_id", sess.ID)

	return nil
}

// Shutdown performs a clean shutdown of the application
func (app *App) Shutdown() {
	// Cancel all watcher goroutines
	app.lspCancelFuncsMutex.Lock()
	for _, cancel := range app.lspWatcherCancelFuncs {
		cancel()
	}
	app.lspCancelFuncsMutex.Unlock()
	app.lspWatcherWG.Wait()

	// Perform additional cleanup for LSP clients
	app.lspClientsMutex.RLock()
	clients := make(map[string]*lsp.Client, len(app.LSPClients))
	maps.Copy(clients, app.LSPClients)
	app.lspClientsMutex.RUnlock()

	for name, client := range clients {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := client.Shutdown(shutdownCtx); err != nil {
			logging.Error("Failed to shutdown LSP client", "name", name, "error", err)
		}
		cancel()
	}
}

// ForceShutdown performs an aggressive shutdown for non-interactive mode
func (app *App) ForceShutdown() {
	logging.Info("Starting force shutdown")

	// Cancel all watcher goroutines immediately
	app.lspCancelFuncsMutex.Lock()
	for _, cancel := range app.lspWatcherCancelFuncs {
		cancel()
	}
	app.lspCancelFuncsMutex.Unlock()

	// Don't wait for watchers in force shutdown - kill LSP clients directly
	app.lspClientsMutex.RLock()
	clients := make(map[string]*lsp.Client, len(app.LSPClients))
	maps.Copy(clients, app.LSPClients)
	app.lspClientsMutex.RUnlock()

	for name, client := range clients {
		// Use a very short timeout for force shutdown
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		if err := client.Shutdown(shutdownCtx); err != nil {
			logging.Debug("Failed to gracefully shutdown LSP client, forcing close", "name", name, "error", err)
			// Force close if graceful shutdown fails
			client.Close()
		}
		cancel()
	}

	// Force kill any remaining child processes
	app.forceKillAllChildProcesses()

	logging.Info("Force shutdown completed")
}

// forceKillAllChildProcesses kills all child processes of the current process
func (app *App) forceKillAllChildProcesses() {
	currentPID := os.Getpid()

	// Find all child processes using pgrep
	cmd := exec.Command("pgrep", "-P", strconv.Itoa(currentPID))
	output, err := cmd.Output()
	if err != nil {
		// No child processes found or pgrep failed
		return
	}

	// Parse PIDs and kill them
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
