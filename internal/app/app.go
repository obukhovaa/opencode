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

	CoderAgent agent.Service

	PrimaryAgents    map[config.AgentName]agent.Service
	PrimaryAgentKeys []config.AgentName
	ActiveAgentIdx   int

	LSPClients map[string]*lsp.Client

	clientsMutex sync.RWMutex

	watcherCancelFuncs []context.CancelFunc
	cancelFuncsMutex   sync.Mutex
	watcherWG          sync.WaitGroup
}

func (a *App) ActiveAgent() agent.Service {
	if len(a.PrimaryAgentKeys) == 0 {
		return a.CoderAgent
	}
	name := a.PrimaryAgentKeys[a.ActiveAgentIdx]
	return a.PrimaryAgents[name]
}

func (a *App) ActiveAgentName() config.AgentName {
	if len(a.PrimaryAgentKeys) == 0 {
		return config.AgentCoder
	}
	return a.PrimaryAgentKeys[a.ActiveAgentIdx]
}

func (a *App) SwitchAgent() config.AgentName {
	if len(a.PrimaryAgentKeys) <= 1 {
		return a.ActiveAgentName()
	}
	a.ActiveAgentIdx = (a.ActiveAgentIdx + 1) % len(a.PrimaryAgentKeys)
	name := a.PrimaryAgentKeys[a.ActiveAgentIdx]
	a.CoderAgent = a.PrimaryAgents[name]
	return name
}

func New(ctx context.Context, conn *sql.DB) (*App, error) {
	q := db.NewQuerier(conn)
	sessions := session.NewService(q)
	messages := message.NewService(q)

	// Type assert to *db.Queries or *db.MySQLQuerier (both have embedded *db.Queries)
	var queries *db.Queries
	switch v := q.(type) {
	case *db.Queries:
		queries = v
	case *db.MySQLQuerier:
		queries = v.Queries
	default:
		return nil, fmt.Errorf("unexpected querier type: %T", q)
	}

	files := history.NewService(queries, conn)

	app := &App{
		Sessions:      sessions,
		Messages:      messages,
		History:       files,
		Permissions:   permission.NewPermissionService(),
		LSPClients:    make(map[string]*lsp.Client),
		PrimaryAgents: make(map[config.AgentName]agent.Service),
	}

	// Initialize theme based on configuration
	app.initTheme()

	// Initialize LSP clients in the background
	go app.initLSPClients(ctx)

	var err error
	app.CoderAgent, err = agent.NewAgent(
		config.AgentCoder,
		app.Sessions,
		app.Messages,
		agent.CoderAgentTools(
			app.Permissions,
			app.Sessions,
			app.Messages,
			app.History,
			app.LSPClients,
		),
	)
	if err != nil {
		logging.Error("Failed to create coder agent", err)
		return nil, err
	}
	app.PrimaryAgents[config.AgentCoder] = app.CoderAgent
	app.PrimaryAgentKeys = append(app.PrimaryAgentKeys, config.AgentCoder)

	// Try to create hivemind agent
	cfg := config.Get()
	if _, ok := cfg.Agents[config.AgentHivemind]; ok {
		hivemindAgent, hivemindErr := agent.NewAgent(
			config.AgentHivemind,
			app.Sessions,
			app.Messages,
			agent.HivemindAgentTools(app.Sessions, app.Messages, app.LSPClients, app.Permissions),
		)
		if hivemindErr != nil {
			logging.Warn("Failed to create hivemind agent, skipping", "error", hivemindErr)
		} else {
			app.PrimaryAgents[config.AgentHivemind] = hivemindAgent
			app.PrimaryAgentKeys = append(app.PrimaryAgentKeys, config.AgentHivemind)
		}
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

	sess, err := app.Sessions.Create(ctx, title)
	if err != nil {
		return fmt.Errorf("failed to create session for non-interactive mode: %w", err)
	}
	logging.Info("Created session for non-interactive run", "session_id", sess.ID)

	// Automatically approve all permission requests for this non-interactive session
	app.Permissions.AutoApproveSession(sess.ID)

	done, err := app.CoderAgent.Run(ctx, sess.ID, prompt)
	if err != nil {
		return fmt.Errorf("failed to start agent processing stream: %w", err)
	}

	result := <-done
	if result.Error != nil {
		if errors.Is(result.Error, context.Canceled) || errors.Is(result.Error, agent.ErrRequestCancelled) {
			logging.Info("Agent processing cancelled", "session_id", sess.ID)
			return nil
		}
		return fmt.Errorf("agent processing failed: %w", result.Error)
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
	app.cancelFuncsMutex.Lock()
	for _, cancel := range app.watcherCancelFuncs {
		cancel()
	}
	app.cancelFuncsMutex.Unlock()
	app.watcherWG.Wait()

	// Perform additional cleanup for LSP clients
	app.clientsMutex.RLock()
	clients := make(map[string]*lsp.Client, len(app.LSPClients))
	maps.Copy(clients, app.LSPClients)
	app.clientsMutex.RUnlock()

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
	app.cancelFuncsMutex.Lock()
	for _, cancel := range app.watcherCancelFuncs {
		cancel()
	}
	app.cancelFuncsMutex.Unlock()

	// Don't wait for watchers in force shutdown - kill LSP clients directly
	app.clientsMutex.RLock()
	clients := make(map[string]*lsp.Client, len(app.LSPClients))
	maps.Copy(clients, app.LSPClients)
	app.clientsMutex.RUnlock()

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
