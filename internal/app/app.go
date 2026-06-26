package app

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"

	agentregistry "github.com/opencode-ai/opencode/internal/agent"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/cron"
	"github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/flow"
	"github.com/opencode-ai/opencode/internal/history"
	"github.com/opencode-ai/opencode/internal/hooks"
	"github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/lsp"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/question"
	"github.com/opencode-ai/opencode/internal/recap"
	"github.com/opencode-ai/opencode/internal/session"
	"github.com/opencode-ai/opencode/internal/todo"
	"github.com/opencode-ai/opencode/internal/tui/theme"
)

type App struct {
	Sessions      session.Service
	Messages      message.Service
	History       history.Service
	Recaps        recap.Service
	Permissions   permission.Service
	Registry      agentregistry.Registry
	MCPRegistry   agent.MCPRegistry
	Flows         flow.Service
	Crons         cron.Service
	CronScheduler *cron.Scheduler
	Todos         *todo.Store
	Questions     question.Service // nil in non-interactive mode
	AgentFactory  agent.AgentFactory
	LspService    lsp.LspService

	activeAgent agent.Service

	PrimaryAgents    map[config.AgentName]agent.Service
	PrimaryAgentKeys []config.AgentName
	ActiveAgentIdx   int

	InitialSession   *session.Session
	InitialSessionID string
	AutoApprove      bool

	// activeSessionID is the session currently visible in the TUI. Written by
	// the TUI Update loop (which uses a value receiver, so the appModel struct
	// is copied each tick), read by the cron scheduler from a separate goroutine.
	// Using atomic.Value avoids the stale-pointer problem that arises when a
	// closure captures the original *appModel pointer which bubbletea never
	// updates again after the first Update call.
	activeSessionID atomic.Value // stores string

	cliOutputSchema map[string]any
}

// SetActiveSessionID is called by the TUI whenever the selected session changes.
func (app *App) SetActiveSessionID(id string) {
	app.activeSessionID.Store(id)
}

// ActiveSessionID returns the session currently visible in the TUI.
func (app *App) ActiveSessionID() string {
	v := app.activeSessionID.Load()
	if v == nil {
		return ""
	}
	return v.(string)
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

func (app *App) SwitchAgentReverse() config.AgentName {
	if len(app.PrimaryAgentKeys) <= 1 {
		return app.ActiveAgentName()
	}
	app.ActiveAgentIdx = (app.ActiveAgentIdx - 1 + len(app.PrimaryAgentKeys)) % len(app.PrimaryAgentKeys)
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

func New(ctx context.Context, conn *sql.DB, cliSchema map[string]any, projectID string) (*App, error) {
	q := db.NewQuerier(conn)
	sessions := session.NewService(q, projectID)
	messages := message.NewService(q, conn)
	files := history.NewService(q, conn)
	recaps := recap.NewService(q)
	reg := agentregistry.GetRegistry()
	perm := permission.NewPermissionService()
	lspSvc := NewLspService()
	mcpRegistry := agent.NewMCPRegistry(perm, reg)
	factory := agent.NewAgentFactory(sessions, messages, perm, files, lspSvc, reg, mcpRegistry)
	todoStore := todo.NewStore()
	factory.SetTodoStore(todoStore)
	flows := flow.NewService(sessions, messages, q, perm, factory)

	// Hook registry: reads the `hooks` block from .opencode.json on
	// every event fire (via config.Get()). The getter indirection
	// keeps internal/hooks decoupled from internal/config — same
	// pattern as bridge late-injection.
	if cwd, cwdErr := os.Getwd(); cwdErr == nil {
		hookReg := hooks.NewRegistry(func() map[string][]hooks.MatcherGroup {
			c := config.Get()
			if c == nil {
				return nil
			}
			return c.Hooks
		}, cwd)
		factory.SetHookRegistry(hookReg)
	} else {
		logging.Warn("Hook registry not installed: could not resolve project root", "error", cwdErr)
	}

	// Initialize cron service (unless disabled)
	var cronSvc cron.Service
	if os.Getenv("OPENCODE_DISABLE_CRON") == "" {
		cronSvc = cron.NewService(q)
		cronAdapter := cron.NewToolServiceAdapter(cronSvc)
		schedHelper := cron.NewScheduleHelper()
		factory.SetCronServices(cronAdapter, schedHelper)
	}

	// Initialize question service if enabled.
	// Three triggers (any one of them enables Questions):
	//   1. OPENCODE_ENABLE_QUESTION_TOOL env var (legacy TUI path)
	//   2. cfg.Router.QuestionMode is non-empty in .opencode.json (chat
	//      bridge needs Questions live to route platform-native UI back
	//      to agents — per the bridge-http-api + chat-bridge specs the
	//      questionMode field is the canonical gate for the in-process
	//      bridge, replacing the legacy env var)
	// The service must be injected into the factory before
	// InitPrimaryAgents so agents see the tool during tool resolution.
	var questionSvc question.Service
	appCfg := config.Get()
	enabledByEnv := os.Getenv("OPENCODE_ENABLE_QUESTION_TOOL") == "1" || os.Getenv("OPENCODE_ENABLE_QUESTION_TOOL") == "true"
	enabledByRouter := appCfg != nil && appCfg.Router != nil && appCfg.Router.QuestionMode != "" && appCfg.Router.QuestionMode != "disabled"
	if enabledByEnv || enabledByRouter {
		questionSvc = question.NewService()
		factory.SetQuestionService(questionSvc)
	}

	app := &App{
		Sessions:      sessions,
		Messages:      messages,
		History:       files,
		Recaps:        recaps,
		Permissions:   perm,
		Registry:      reg,
		LspService:    lspSvc,
		PrimaryAgents: make(map[config.AgentName]agent.Service),
		MCPRegistry:   mcpRegistry,
		AgentFactory:  factory,
		Flows:         flows,
		Crons:         cronSvc,
		Todos:         todoStore,
		Questions:     questionSvc,
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
	}
	// Default to coder agent if it exists, otherwise fall back to the first agent
	if _, ok := app.PrimaryAgents[config.AgentCoder]; ok {
		for i, key := range app.PrimaryAgentKeys {
			if key == config.AgentCoder {
				app.ActiveAgentIdx = i
				app.activeAgent = app.PrimaryAgents[key]
				break
			}
		}
	} else if len(primaryAgents) > 0 {
		app.activeAgent = primaryAgents[0]
	}

	// Create and start the cron scheduler after primary agents are ready
	if cronSvc != nil {
		taskTool := agent.NewAgentTool(sessions, perm, reg, factory)
		taskRunner := cron.NewTaskToolRunner(taskTool)
		busyChecker := cron.NewAppBusyChecker(
			func(sessionID string) bool {
				activeAgent := app.ActiveAgent()
				if activeAgent == nil {
					return false
				}
				return activeAgent.IsSessionBusy(sessionID)
			},
			func(sessionID string) bool {
				activeAgent := app.ActiveAgent()
				if activeAgent == nil {
					return true
				}
				return activeAgent.TryLockSession(sessionID)
			},
			func(sessionID string) {
				activeAgent := app.ActiveAgent()
				if activeAgent == nil {
					return
				}
				activeAgent.UnlockSession(sessionID)
			},
		)
		// The TUI wires the active-session provider via
		// scheduler.SetActiveSessionProvider once it has been constructed.
		scheduler := cron.NewScheduler(cronSvc, messages, sessions, perm, busyChecker, nil, taskRunner)
		app.CronScheduler = scheduler

		// Pin scheduling to a single opencode process per database. Without
		// this, two processes (e.g. `opencode serve` + `opencode` TUI) tick
		// every second and race on ClaimForFiring; whichever wins runs the
		// job, which means a TUI process can claim a cron whose session is
		// bridge-bound and then defer it forever (no bridge resolver in this
		// process) or pop a permission dialog the chat user cannot see.
		// First process to start wins; followers retry every 5s and take
		// over on leader exit. Construction failure is non-fatal so a
		// misconfigured deployment still falls back to the legacy
		// every-process-ticks behaviour rather than disabling cron outright.
		cfg := config.Get()
		if cfg != nil {
			lock, err := cron.NewLeaderLock(cfg.SessionProvider.Type, cfg.Data.Directory, projectID, conn)
			if err != nil {
				logging.Warn("Cron leader lock disabled (continuing without single-process pinning)", "error", err)
			} else {
				scheduler.SetLeaderLock(lock)
			}
		}

		scheduler.Start(ctx)
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
	if app.CronScheduler != nil {
		app.CronScheduler.Stop()
	}
	if app.Messages != nil {
		app.Messages.Shutdown()
	}
	tools.CleanupTempDir()
	app.LspService.Shutdown(context.Background())
}

// ForceShutdown performs an aggressive shutdown for non-interactive mode
func (app *App) ForceShutdown() {
	logging.Info("Starting force shutdown")
	if app.CronScheduler != nil {
		app.CronScheduler.Stop()
	}
	if app.Messages != nil {
		app.Messages.Shutdown()
	}
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
