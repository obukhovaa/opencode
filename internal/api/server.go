package api

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/version"
)

// ServerOptions holds configuration for the API server.
type ServerOptions struct {
	Port       int
	Hostname   string
	CORSOrigin string
	// Bridge, when non-nil, registers its HTTP routes under /router/*
	// on the same mux. The orchestrator owns the handlers — the API
	// server only forwards mux registration. Routes outside /router/*
	// (e.g. /flow/*) follow the same pattern in future phases.
	Bridge RouteRegistrar
}

// RouteRegistrar is the contract bridge / flow subsystems satisfy to
// hook into the API mux. Keeping it as an interface avoids
// internal/api importing internal/bridge/service (which would drag the
// chat-platform deps into every binary that builds the API server).
type RouteRegistrar interface {
	RegisterRoutes(mux *http.ServeMux)
}

// Server holds the HTTP server and application reference.
type Server struct {
	app            *app.App
	httpSrv        *http.Server
	port           int
	hostname       string
	password       string
	corsOrigin     string
	healthReporter HealthReporter
	flowRunner     *flowRunner
}

// NewServer creates a new API server.
func NewServer(application *app.App, opts ServerOptions) *Server {
	s := &Server{
		app:      application,
		port:     opts.Port,
		hostname: opts.Hostname,
		password: os.Getenv("OPENCODE_SERVER_PASSWORD"),
	}

	// Flow runner: a single-flow-at-a-time tracker driven by /flow/*
	// HTTP routes. Created up-front so /flow handlers always have a
	// valid runner to delegate to; idle until a POST /flow.
	if application != nil && application.Flows != nil {
		s.flowRunner = newFlowRunner(application.Flows)
	}

	mux := http.NewServeMux()
	s.registerRoutes(mux)
	if opts.Bridge != nil {
		opts.Bridge.RegisterRoutes(mux)
		// If the bridge also satisfies HealthReporter (the bridge service
		// does), wire it so /global/health embeds the bridge snapshot.
		if hr, ok := opts.Bridge.(HealthReporter); ok {
			s.healthReporter = hr
		}
	}

	s.corsOrigin = opts.CORSOrigin
	if s.corsOrigin == "" {
		s.corsOrigin = "*"
	}
	corsOrigin := s.corsOrigin

	handler := chain(
		mux,
		recoveryMiddleware,
		loggingMiddleware,
		corsMiddleware(corsOrigin),
		authMiddleware(s.password),
		jsonContentTypeMiddleware,
	)

	s.httpSrv = &http.Server{
		Addr:              fmt.Sprintf("%s:%d", s.hostname, s.port),
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	return s
}

// registerRoutes sets up all API route handlers.
func (s *Server) registerRoutes(mux *http.ServeMux) {
	// Global
	mux.HandleFunc("GET /global/health", s.handleHealth)
	mux.HandleFunc("GET /event", s.handleEventSubscribe)
	mux.HandleFunc("GET /global/event", s.handleGlobalEvent)

	// Sessions
	mux.HandleFunc("GET /session", s.handleSessionList)
	mux.HandleFunc("POST /session", s.handleSessionCreate)
	mux.HandleFunc("GET /session/status", s.handleSessionStatus)
	mux.HandleFunc("GET /session/{sessionID}", s.handleSessionGet)
	mux.HandleFunc("DELETE /session/{sessionID}", s.handleSessionDelete)
	mux.HandleFunc("PATCH /session/{sessionID}", s.handleSessionUpdate)
	mux.HandleFunc("GET /session/{sessionID}/children", s.handleSessionChildren)
	mux.HandleFunc("POST /session/{sessionID}/abort", s.handleSessionAbort)
	mux.HandleFunc("POST /session/{sessionID}/permissions/{permissionID}", s.handlePermissionRespond)

	// Messages & prompts
	mux.HandleFunc("GET /session/{sessionID}/message", s.handleMessageList)
	mux.HandleFunc("GET /session/{sessionID}/message/{messageID}", s.handleMessageGet)
	mux.HandleFunc("POST /session/{sessionID}/message", s.handleSessionPrompt)
	mux.HandleFunc("POST /session/{sessionID}/prompt_async", s.handleSessionPromptAsync)
	mux.HandleFunc("POST /session/{sessionID}/summarize", s.handleSessionSummarize)

	// Todos
	mux.HandleFunc("GET /session/{sessionID}/todo", s.handleSessionTodo)

	// Config
	mux.HandleFunc("GET /config", s.handleConfigGet)
	mux.HandleFunc("GET /config/providers", s.handleConfigProviders)

	// Provider (OpenWork proxies /opencode/provider → /provider, expects dax format)
	mux.HandleFunc("GET /provider", s.handleProvider)

	// MCP servers
	mux.HandleFunc("GET /mcp", s.handleMCPList)

	// Instance lifecycle (used by OpenWork workspace activation)
	// Both paths point at the same no-op stub — dax SDK v2 uses /global/dispose,
	// older callers use /instance/dispose.
	mux.HandleFunc("POST /instance/dispose", s.handleInstanceDispose)
	mux.HandleFunc("POST /global/dispose", s.handleInstanceDispose)

	// Permissions
	mux.HandleFunc("GET /permission", s.handlePermissionList)
	mux.HandleFunc("POST /permission/{requestID}/reply", s.handlePermissionReply)

	// Questions
	mux.HandleFunc("GET /question", s.handleQuestionList)
	mux.HandleFunc("POST /question/{requestID}/reply", s.handleQuestionReply)
	mux.HandleFunc("POST /question/{requestID}/reject", s.handleQuestionReject)

	// Agents
	mux.HandleFunc("GET /agent", s.handleAgentList)
	mux.HandleFunc("POST /agent/select", s.handleAgentSelect)
	mux.HandleFunc("POST /agent/model/select", s.handleAgentModelSelect)

	// Skills
	mux.HandleFunc("GET /skill", s.handleSkillList)

	// Client log ingest (dax SDK forwards client errors here)
	mux.HandleFunc("POST /log", s.handleLogWrite)

	// Flow API (per flow-api spec). Routes 404 cleanly if flowRunner
	// is nil (application without flow.Service — e.g. tests).
	mux.HandleFunc("GET /flow", s.handleFlowList)
	mux.HandleFunc("POST /flow", s.handleFlowStart)
	mux.HandleFunc("GET /flow/status", s.handleFlowStatus)
	mux.HandleFunc("DELETE /flow", s.handleFlowAbort)
}

// Start starts the HTTP server. It blocks until the server is shut down.
func (s *Server) Start(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.httpSrv.Addr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", s.httpSrv.Addr, err)
	}

	addr := ln.Addr().String()
	url := fmt.Sprintf("http://%s", addr)

	auth := "none"
	if s.password != "" {
		auth = "Basic (password set)"
	}

	// Sentinel line on stdout — OpenWork and other launchers read stdout
	// for this exact prefix to detect that the server is ready.
	fmt.Fprintf(os.Stdout, "opencode server listening on %s\n", url)

	fmt.Fprintf(os.Stderr, "\n")
	fmt.Fprintf(os.Stderr, "  ⌬ OpenCode HTTP Server\n")
	fmt.Fprintf(os.Stderr, "  ──────────────────────\n")
	fmt.Fprintf(os.Stderr, "  Listening:  %s\n", url)
	fmt.Fprintf(os.Stderr, "  Version:    %s\n", version.Version)
	fmt.Fprintf(os.Stderr, "  Auth:       %s\n", auth)
	fmt.Fprintf(os.Stderr, "  CORS:       %s\n", s.corsOrigin)

	// Router/bridge status. Three cases:
	//   1. No bridge wired (cfg.Router == nil) → don't print anything.
	//   2. Bridge wired but no channel enabled → "Router: disabled".
	//   3. Bridge wired with adapters → print overall status + one
	//      line per adapter showing per-identity status + binding count.
	// This makes it obvious at a glance whether a chat consumer should
	// be able to reach the server.
	if bp, ok := s.healthReporter.(BannerProvider); ok && bp != nil {
		bridgeStatus, adapterLines := bp.BridgeBanner(ctx)
		fmt.Fprintf(os.Stderr, "  Router:     %s\n", bridgeStatus)
		for _, line := range adapterLines {
			fmt.Fprintf(os.Stderr, "                %s\n", line)
		}
	}

	fmt.Fprintf(os.Stderr, "\n")
	fmt.Fprintf(os.Stderr, "  Press Ctrl+C to stop\n")
	fmt.Fprintf(os.Stderr, "\n")

	logging.Info("API server started", "address", addr, "url", url)

	// Shut down gracefully when context is cancelled.
	go func() {
		<-ctx.Done()
		logging.Info("API server shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := s.httpSrv.Shutdown(shutdownCtx); err != nil {
			logging.Error("API server shutdown error", "error", err)
		}
	}()

	if err := s.httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("API server error: %w", err)
	}

	return nil
}

// Stop gracefully shuts down the server.
func (s *Server) Stop(ctx context.Context) error {
	return s.httpSrv.Shutdown(ctx)
}
