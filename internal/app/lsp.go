package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/lsp"
	"github.com/opencode-ai/opencode/internal/lsp/install"
	"github.com/opencode-ai/opencode/internal/lsp/watcher"
)

type serverNameContextKey string

const ServerNameContextKey serverNameContextKey = "server_name"

func (app *App) initLSPClients(ctx context.Context) {
	cfg := config.Get()
	wg := sync.WaitGroup{}
	for name, server := range install.ResolveServers(cfg) {
		wg.Add(1)
		go func() {
			lspName := "LSP-" + name
			defer logging.RecoverPanic(lspName, func() {
				logging.ErrorPersist(fmt.Sprintf("Panic while starting %s", lspName))
			})
			defer wg.Done()
			app.startLSPServer(ctx, name, server)
		}()
	}
	go func() {
		wg.Wait()
		logging.Info("LSP clients initialization completed")
		close(app.LSPClientsCh)
	}()
	logging.Info("LSP clients initialization started in background")
}

// hasMatchingFiles checks whether the working directory contains any files
// with extensions handled by the given server. It does a shallow walk
// (max 3 levels deep) to keep startup fast.
func hasMatchingFiles(rootDir string, extensions []string) bool {
	if len(extensions) == 0 {
		return true // no extensions specified, assume relevant
	}

	extSet := make(map[string]struct{}, len(extensions))
	for _, ext := range extensions {
		extSet[ext] = struct{}{}
	}

	found := false
	_ = filepath.WalkDir(rootDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return filepath.SkipDir
		}

		if d.IsDir() {
			// Skip hidden dirs and common non-source dirs
			name := d.Name()
			if strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" || name == "dist" || name == "build" || name == "target" {
				return filepath.SkipDir
			}
			// Limit depth to 3 levels
			rel, _ := filepath.Rel(rootDir, path)
			if strings.Count(rel, string(filepath.Separator)) >= 3 {
				return filepath.SkipDir
			}
			return nil
		}

		ext := filepath.Ext(path)
		if _, ok := extSet[ext]; ok {
			found = true
			return filepath.SkipAll
		}
		return nil
	})

	return found
}

// startLSPServer resolves the binary (auto-installing if needed), then creates and starts the LSP client
func (app *App) startLSPServer(ctx context.Context, name string, server install.ResolvedServer) {
	cfg := config.Get()

	// Skip servers whose file extensions aren't present in the project
	if !hasMatchingFiles(config.WorkingDirectory(), server.Extensions) {
		logging.Debug("No matching files found, skipping LSP server", "name", name, "extensions", server.Extensions)
		return
	}

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

	watchCtx, cancelFunc := context.WithCancel(ctx)
	watchCtx = context.WithValue(watchCtx, ServerNameContextKey, name)

	workspaceWatcher := watcher.NewWorkspaceWatcher(lspClient)

	app.lspCancelFuncsMutex.Lock()
	app.lspWatcherCancelFuncs = append(app.lspWatcherCancelFuncs, cancelFunc)
	app.lspCancelFuncsMutex.Unlock()

	app.lspWatcherWG.Add(1)

	app.lspClientsMutex.Lock()
	app.LSPClients[name] = lspClient
	app.LSPClientsCh <- lspClient
	app.lspClientsMutex.Unlock()

	go app.runWorkspaceWatcher(watchCtx, name, workspaceWatcher)
}

// runWorkspaceWatcher executes the workspace watcher for an LSP client
func (app *App) runWorkspaceWatcher(ctx context.Context, name string, workspaceWatcher *watcher.WorkspaceWatcher) {
	defer app.lspWatcherWG.Done()
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

	app.lspClientsMutex.Lock()
	oldClient, exists := app.LSPClients[name]
	if exists {
		delete(app.LSPClients, name)
	}
	app.lspClientsMutex.Unlock()

	if exists && oldClient != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = oldClient.Shutdown(shutdownCtx)
		cancel()
	}

	app.startLSPServer(ctx, name, server)
	logging.Info("Successfully restarted LSP client", "client", name)
}
