package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/opencode-ai/opencode/internal/api"
	"github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/langfuse"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/pubsub"
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the OpenCode HTTP API server",
	Long: `Start an HTTP REST API server that exposes OpenCode functionality.
The server provides endpoints for sessions, messages, and other operations
compatible with the @opencode-ai/sdk/v2 TypeScript SDK.

Authentication can be enabled by setting the OPENCODE_SERVER_PASSWORD environment variable.`,
	Example: `
  # Start the server on default port 4096
  opencode serve

  # Start on a custom port and hostname
  opencode serve --port 8080 --hostname 0.0.0.0

  # Start with a specific CORS origin
  opencode serve --cors "http://localhost:3000"

  # Start with authentication
  OPENCODE_SERVER_PASSWORD=secret opencode serve

  # Auto-approve all permission requests (no human in the loop)
  opencode serve --auto-approve`,
	RunE: func(cmd *cobra.Command, args []string) error {
		port, _ := cmd.Flags().GetInt("port")
		hostname, _ := cmd.Flags().GetString("hostname")
		corsOrigin, _ := cmd.Flags().GetString("cors")
		if corsOrigin == "" || !cmd.Flags().Changed("cors") {
			if alias, _ := cmd.Flags().GetString("cors-origin"); alias != "" {
				corsOrigin = alias
			}
		}
		debug, _ := cmd.Flags().GetBool("debug")
		cwd, _ := cmd.Flags().GetString("cwd")
		autoApprove, _ := cmd.Flags().GetBool("auto-approve")

		if cwd != "" {
			if err := os.Chdir(cwd); err != nil {
				return fmt.Errorf("failed to change directory: %w", err)
			}
		}
		if cwd == "" {
			c, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("failed to get current working directory: %w", err)
			}
			cwd = c
		}

		cfg, err := config.Load(cwd, debug)
		if err != nil {
			return err
		}

		// Headless mode: log to stderr so the user sees output.
		level := slog.LevelInfo
		if debug {
			level = slog.LevelDebug
		}
		logging.SetupStderrLogging(level)

		// Initialize Langfuse tracing if enabled
		if cfg.Telemetry != nil && cfg.Telemetry.Langfuse != nil && cfg.Telemetry.Langfuse.Enabled {
			lf := cfg.Telemetry.Langfuse
			if langfuse.Init(lf.PublicKey, lf.SecretKey, lf.BaseURL) {
				defer langfuse.ShutdownGlobal()
				logging.Info("Langfuse tracing enabled", "url", lf.BaseURL)
			} else {
				logging.Warn("Langfuse enabled in config but credentials resolved to empty — tracing disabled")
			}
		}

		conn, err := db.Connect()
		if err != nil {
			return err
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		application, err := app.New(ctx, conn, nil, "")
		if err != nil {
			logging.Error("Failed to create app", "error", err)
			return err
		}
		defer application.Shutdown()

		// Auto-approve every session created in the server when --auto-approve is set.
		// Useful for headless deployments where no human is present to grant permissions.
		// DANGEROUS in shared/networked deployments — every tool call is silently allowed.
		if autoApprove {
			logging.Warn("Auto-approve enabled — all permission requests will be granted automatically")
			enableAutoApprove(ctx, application)
		}

		server := api.NewServer(application, api.ServerOptions{
			Port:       port,
			Hostname:   hostname,
			CORSOrigin: corsOrigin,
		})

		// Handle OS signals for graceful shutdown
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		go func() {
			sig := <-sigCh
			logging.Info("Received signal, shutting down", "signal", sig.String())
			cancel()
		}()

		return server.Start(ctx)
	},
}

// enableAutoApprove marks all current and future sessions as auto-approved.
// The subscription is established BEFORE listing existing sessions, so any
// session created between list and subscribe is captured by the channel
// rather than lost. Used by headless modes (serve, acp) when --auto-approve
// is set.
func enableAutoApprove(ctx context.Context, application *app.App) {
	// Subscribe first so we don't miss CreatedEvent for sessions made
	// between the list call returning and the goroutine entering its loop.
	sesCh := application.Sessions.Subscribe(ctx)

	// Mark existing sessions. sync.Map.Store is idempotent, so racing with
	// a CreatedEvent for the same ID is harmless.
	sessions, err := application.Sessions.List(ctx)
	if err != nil {
		logging.Warn("Failed to list sessions for auto-approve", "error", err)
	} else {
		for _, sess := range sessions {
			application.Permissions.AutoApproveSession(sess.ID)
		}
		logging.Debug("Auto-approved existing sessions", "count", len(sessions))
	}

	go func() {
		defer logging.RecoverPanic("autoApproveSessions", nil)
		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-sesCh:
				if !ok {
					return
				}
				if event.Type != pubsub.CreatedEvent {
					continue
				}
				application.Permissions.AutoApproveSession(event.Payload.ID)
				logging.Debug("Auto-approved session", "session_id", event.Payload.ID)
			}
		}
	}()
}

func init() {
	serveCmd.Flags().Int("port", 4096, "Port to listen on")
	serveCmd.Flags().String("hostname", "127.0.0.1", "Hostname to bind to")
	serveCmd.Flags().String("cors", "*", "Allowed CORS origin")
	serveCmd.Flags().String("cors-origin", "", "Alias for --cors (deprecated)")
	_ = serveCmd.Flags().MarkHidden("cors-origin")
	serveCmd.Flags().Bool("auto-approve", false, "Auto-approve all permission requests (dangerous — no human in the loop)")
	serveCmd.Flags().BoolP("debug", "d", false, "Debug")
	serveCmd.Flags().StringP("cwd", "c", "", "Current working directory")

	rootCmd.AddCommand(serveCmd)
}
