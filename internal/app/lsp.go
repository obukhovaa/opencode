package app

import (
	"context"
	"time"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/lsp"
	"github.com/opencode-ai/opencode/internal/lsp/install"
	"github.com/opencode-ai/opencode/internal/lsp/watcher"
)

func (app *App) initLSPClients(ctx context.Context) {
	cfg := config.Get()

	// Resolve which servers to start: merge built-in registry with user config
	servers := install.ResolveServers(cfg)

	for name, server := range servers {
		if server.Disabled {
			logging.Debug("LSP server disabled, skipping", "name", name)
			continue
		}

		go app.startLSPServer(ctx, name, server)
	}

	logging.Info("LSP clients initialization started in background")
}

// startLSPServer resolves the binary (auto-installing if needed), then creates and starts the LSP client
func (app *App) startLSPServer(ctx context.Context, name string, server install.ResolvedServer) {
	cfg := config.Get()

	// Resolve the command — check PATH, bin dir, or auto-install
	command, args, err := install.ResolveCommand(ctx, server, cfg.DisableLSPDownload)
	if err != nil {
		logging.Debug("LSP server not available, skipping", "name", name, "reason", err)
		return
	}

	app.createAndStartLSPClient(ctx, name, server, command, args...)
}

// createAndStartLSPClient creates a new LSP client, initializes it, and starts its workspace watcher
func (app *App) createAndStartLSPClient(ctx context.Context, name string, server install.ResolvedServer, command string, args ...string) {
	logging.Info("Creating LSP client", "name", name, "command", command, "args", args)

	// Build environment for the server
	lspClient, err := lsp.NewClient(ctx, command, server.Env, args...)
	if err != nil {
		logging.Error("Failed to create LSP client for", name, err)
		return
	}

	// Store extensions on the client for routing
	lspClient.SetExtensions(server.Extensions)

	initCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Pass server-specific initialization options
	var initOpts map[string]any
	if m, ok := server.Initialization.(map[string]any); ok {
		initOpts = m
	}

	_, err = lspClient.InitializeLSPClient(initCtx, config.WorkingDirectory(), initOpts)
	if err != nil {
		logging.Error("Initialize failed", "name", name, "error", err)
		lspClient.Close()
		return
	}

	if err := lspClient.WaitForServerReady(initCtx); err != nil {
		logging.Error("Server failed to become ready", "name", name, "error", err)
		lspClient.SetServerState(lsp.StateError)
	} else {
		logging.Info("LSP server is ready", "name", name)
		lspClient.SetServerState(lsp.StateReady)
	}

	logging.Info("LSP client initialized", "name", name)

	watchCtx, cancelFunc := context.WithCancel(ctx)
	watchCtx = context.WithValue(watchCtx, "serverName", name)

	workspaceWatcher := watcher.NewWorkspaceWatcher(lspClient)

	app.cancelFuncsMutex.Lock()
	app.watcherCancelFuncs = append(app.watcherCancelFuncs, cancelFunc)
	app.cancelFuncsMutex.Unlock()

	app.watcherWG.Add(1)

	app.clientsMutex.Lock()
	app.LSPClients[name] = lspClient
	app.clientsMutex.Unlock()

	go app.runWorkspaceWatcher(watchCtx, name, workspaceWatcher)
}

// runWorkspaceWatcher executes the workspace watcher for an LSP client
func (app *App) runWorkspaceWatcher(ctx context.Context, name string, workspaceWatcher *watcher.WorkspaceWatcher) {
	defer app.watcherWG.Done()
	defer logging.RecoverPanic("LSP-"+name, func() {
		app.restartLSPClient(ctx, name)
	})

	workspaceWatcher.WatchWorkspace(ctx, config.WorkingDirectory())
	logging.Info("Workspace watcher stopped", "client", name)
}

// restartLSPClient attempts to restart a crashed or failed LSP client
func (app *App) restartLSPClient(ctx context.Context, name string) {
	cfg := config.Get()
	servers := install.ResolveServers(cfg)
	server, exists := servers[name]
	if !exists {
		logging.Error("Cannot restart client, configuration not found", "client", name)
		return
	}

	app.clientsMutex.Lock()
	oldClient, exists := app.LSPClients[name]
	if exists {
		delete(app.LSPClients, name)
	}
	app.clientsMutex.Unlock()

	if exists && oldClient != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = oldClient.Shutdown(shutdownCtx)
		cancel()
	}

	app.startLSPServer(ctx, name, server)
	logging.Info("Successfully restarted LSP client", "client", name)
}
