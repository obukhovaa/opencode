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
}

// Server holds the HTTP server and application reference.
type Server struct {
	app        *app.App
	httpSrv    *http.Server
	port       int
	hostname   string
	password   string
	corsOrigin string
}

// NewServer creates a new API server.
func NewServer(application *app.App, opts ServerOptions) *Server {
	s := &Server{
		app:      application,
		port:     opts.Port,
		hostname: opts.Hostname,
		password: os.Getenv("OPENCODE_SERVER_PASSWORD"),
	}

	mux := http.NewServeMux()
	s.registerRoutes(mux)

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
	mux.HandleFunc("POST /session/{sessionID}/abort", s.handleSessionAbort)

	// Messages & prompts
	mux.HandleFunc("GET /session/{sessionID}/message", s.handleMessageList)
	mux.HandleFunc("GET /session/{sessionID}/message/{messageID}", s.handleMessageGet)
	mux.HandleFunc("POST /session/{sessionID}/message", s.handleSessionPrompt)
	mux.HandleFunc("POST /session/{sessionID}/prompt_async", s.handleSessionPromptAsync)
	mux.HandleFunc("POST /session/{sessionID}/summarize", s.handleSessionSummarize)

	// Config
	mux.HandleFunc("GET /config", s.handleConfigGet)
	mux.HandleFunc("GET /config/providers", s.handleConfigProviders)

	// Permissions
	mux.HandleFunc("POST /permission/{requestID}/reply", s.handlePermissionReply)

	// Agents
	mux.HandleFunc("GET /agent", s.handleAgentList)
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

	fmt.Fprintf(os.Stderr, "\n")
	fmt.Fprintf(os.Stderr, "  ⌬ OpenCode HTTP Server\n")
	fmt.Fprintf(os.Stderr, "  ──────────────────────\n")
	fmt.Fprintf(os.Stderr, "  Listening:  %s\n", url)
	fmt.Fprintf(os.Stderr, "  Version:    %s\n", version.Version)
	fmt.Fprintf(os.Stderr, "  Auth:       %s\n", auth)
	fmt.Fprintf(os.Stderr, "  CORS:       %s\n", s.corsOrigin)
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
