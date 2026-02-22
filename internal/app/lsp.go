package app

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/lsp"
	"github.com/opencode-ai/opencode/internal/lsp/install"
	"github.com/opencode-ai/opencode/internal/lsp/protocol"
	"github.com/opencode-ai/opencode/internal/lsp/watcher"
)

type serverNameContextKey string

const ServerNameContextKey serverNameContextKey = "server_name"

type lspService struct {
	clients   map[string]*lsp.Client
	clientsCh chan *lsp.Client
	mu        sync.RWMutex

	watcherCancelFuncs []context.CancelFunc
	cancelMu           sync.Mutex
	watcherWG          sync.WaitGroup
}

func NewLspService() lsp.LspService {
	return &lspService{
		clients:   make(map[string]*lsp.Client),
		clientsCh: make(chan *lsp.Client, 50),
	}
}

func (s *lspService) Init(ctx context.Context) {
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
			s.startLSPServer(ctx, name, server)
		}()
	}
	go func() {
		wg.Wait()
		logging.Info("LSP clients initialization completed")
		close(s.clientsCh)
	}()
	logging.Info("LSP clients initialization started in background")
}

func (s *lspService) Shutdown(ctx context.Context) {
	s.cancelMu.Lock()
	for _, cancel := range s.watcherCancelFuncs {
		cancel()
	}
	s.cancelMu.Unlock()
	s.watcherWG.Wait()

	s.mu.RLock()
	clients := make(map[string]*lsp.Client, len(s.clients))
	maps.Copy(clients, s.clients)
	s.mu.RUnlock()

	for name, client := range clients {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := client.Shutdown(shutdownCtx); err != nil {
			logging.Error("Failed to shutdown LSP client", "name", name, "error", err)
		}
		cancel()
	}
}

func (s *lspService) ForceShutdown() {
	s.cancelMu.Lock()
	for _, cancel := range s.watcherCancelFuncs {
		cancel()
	}
	s.cancelMu.Unlock()

	s.mu.RLock()
	clients := make(map[string]*lsp.Client, len(s.clients))
	maps.Copy(clients, s.clients)
	s.mu.RUnlock()

	for name, client := range clients {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		if err := client.Shutdown(shutdownCtx); err != nil {
			logging.Debug("Failed to gracefully shutdown LSP client, forcing close", "name", name, "error", err)
			client.Close()
		}
		cancel()
	}
}

func (s *lspService) Clients() map[string]*lsp.Client {
	s.mu.RLock()
	defer s.mu.RUnlock()
	snapshot := make(map[string]*lsp.Client, len(s.clients))
	maps.Copy(snapshot, s.clients)
	return snapshot
}

func (s *lspService) ClientsCh() <-chan *lsp.Client {
	return s.clientsCh
}

func (s *lspService) ClientsForFile(filePath string) []*lsp.Client {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ext := strings.ToLower(filepath.Ext(filePath))
	var matched []*lsp.Client
	for _, client := range s.clients {
		if slices.Contains(client.GetExtensions(), ext) {
			matched = append(matched, client)
		}
	}
	return matched
}

func (s *lspService) NotifyOpenFile(ctx context.Context, filePath string) {
	for _, client := range s.Clients() {
		_ = client.OpenFile(ctx, filePath)
	}
}

func (s *lspService) WaitForDiagnostics(ctx context.Context, filePath string) {
	clients := s.Clients()
	if len(clients) == 0 {
		return
	}

	diagChan := make(chan struct{}, 1)

	for _, client := range clients {
		originalDiags := make(map[protocol.DocumentUri][]protocol.Diagnostic)
		maps.Copy(originalDiags, client.GetDiagnostics())

		handler := func(params json.RawMessage) {
			lsp.HandleDiagnostics(client, params)
			var diagParams protocol.PublishDiagnosticsParams
			if err := json.Unmarshal(params, &diagParams); err != nil {
				return
			}

			if diagParams.URI.Path() == filePath || lsp.HasDiagnosticsChanged(client.GetDiagnostics(), originalDiags) {
				select {
				case diagChan <- struct{}{}:
				default:
				}
			}
		}

		client.RegisterNotificationHandler("textDocument/publishDiagnostics", handler)

		if client.IsFileOpen(filePath) {
			_ = client.NotifyChange(ctx, filePath)
		} else {
			_ = client.OpenFile(ctx, filePath)
		}
	}

	select {
	case <-diagChan:
	case <-time.After(5 * time.Second):
	case <-ctx.Done():
	}
}

func (s *lspService) FormatDiagnostics(filePath string) string {
	clients := s.Clients()
	return lsp.FormatDiagnostics(filePath, clients)
}

// hasMatchingFiles checks whether the working directory contains any files
// with extensions handled by the given server. It does a shallow walk
// (max 3 levels deep) to keep startup fast.
func hasMatchingFiles(rootDir string, extensions []string) bool {
	if len(extensions) == 0 {
		return true
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
			name := d.Name()
			if strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" || name == "dist" || name == "build" || name == "target" {
				return filepath.SkipDir
			}
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

func (s *lspService) startLSPServer(ctx context.Context, name string, server install.ResolvedServer) {
	cfg := config.Get()

	if !hasMatchingFiles(config.WorkingDirectory(), server.Extensions) {
		logging.Debug("No matching files found, skipping LSP server", "name", name, "extensions", server.Extensions)
		return
	}

	command, args, err := install.ResolveCommand(ctx, server, cfg.DisableLSPDownload)
	if err != nil {
		logging.Debug("LSP server not available, skipping", "name", name, "reason", err)
		return
	}

	s.createAndStartLSPClient(ctx, name, server, command, args...)
}

func (s *lspService) createAndStartLSPClient(ctx context.Context, name string, server install.ResolvedServer, command string, args ...string) {
	logging.Info("Creating LSP client", "name", name, "command", command, "args", args)

	lspClient, err := lsp.NewClient(ctx, command, server.Env, args...)
	if err != nil {
		logging.Error("Failed to create LSP client for", name, err)
		return
	}

	lspClient.SetExtensions(server.Extensions)

	initCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

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

	s.cancelMu.Lock()
	s.watcherCancelFuncs = append(s.watcherCancelFuncs, cancelFunc)
	s.cancelMu.Unlock()

	s.watcherWG.Add(1)

	s.mu.Lock()
	s.clients[name] = lspClient
	s.clientsCh <- lspClient
	s.mu.Unlock()

	go s.runWorkspaceWatcher(watchCtx, name, workspaceWatcher)
}

func (s *lspService) runWorkspaceWatcher(ctx context.Context, name string, workspaceWatcher *watcher.WorkspaceWatcher) {
	defer s.watcherWG.Done()
	defer logging.RecoverPanic("LSP-"+name, func() {
		s.restartLSPClient(ctx, name)
	})

	workspaceWatcher.WatchWorkspace(ctx, config.WorkingDirectory())
	logging.Info("Workspace watcher stopped", "client", name)
}

func (s *lspService) restartLSPClient(ctx context.Context, name string) {
	cfg := config.Get()
	servers := install.ResolveServers(cfg)
	server, exists := servers[name]
	if !exists {
		logging.Error("Cannot restart client, configuration not found", "client", name)
		return
	}

	s.mu.Lock()
	oldClient, exists := s.clients[name]
	if exists {
		delete(s.clients, name)
	}
	s.mu.Unlock()

	if exists && oldClient != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = oldClient.Shutdown(shutdownCtx)
		cancel()
	}

	s.startLSPServer(ctx, name, server)
	logging.Info("Successfully restarted LSP client", "client", name)
}
