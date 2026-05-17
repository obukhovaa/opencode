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
  opencode serve --cors-origin "http://localhost:3000"

  # Start with authentication
  OPENCODE_SERVER_PASSWORD=secret opencode serve`,
	RunE: func(cmd *cobra.Command, args []string) error {
		port, _ := cmd.Flags().GetInt("port")
		hostname, _ := cmd.Flags().GetString("hostname")
		corsOrigin, _ := cmd.Flags().GetString("cors-origin")
		debug, _ := cmd.Flags().GetBool("debug")
		cwd, _ := cmd.Flags().GetString("cwd")

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

func init() {
	serveCmd.Flags().Int("port", 4096, "Port to listen on")
	serveCmd.Flags().String("hostname", "127.0.0.1", "Hostname to bind to")
	serveCmd.Flags().String("cors-origin", "*", "Allowed CORS origin")
	serveCmd.Flags().BoolP("debug", "d", false, "Debug")
	serveCmd.Flags().StringP("cwd", "c", "", "Current working directory")

	rootCmd.AddCommand(serveCmd)
}
