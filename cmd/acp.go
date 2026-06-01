package cmd

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/opencode-ai/opencode/internal/acp"
	"github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/logging"
)

var acpCmd = &cobra.Command{
	Use:   "acp",
	Short: "Start the ACP server (JSON-RPC over stdio)",
	Long: `Start an Agent Client Protocol (ACP) server that communicates over stdio
using JSON-RPC 2.0 with newline-delimited JSON (NDJSON) framing.

This enables editor integration with AionUI, Zed, JetBrains, and other
ACP-compatible clients. All logging goes to stderr to avoid corrupting the
protocol stream.`,
	Example: `
  # Start the ACP server for the current directory
  opencode acp

  # Start for a specific project directory
  opencode acp --cwd /path/to/project`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, _ := cmd.Flags().GetString("cwd")
		debug, _ := cmd.Flags().GetBool("debug")
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

		_, err := config.Load(cwd, debug)
		if err != nil {
			return err
		}

		// ACP is headless: log to stderr so the user sees output and
		// stdout stays clean for the JSON-RPC protocol stream.
		level := slog.LevelInfo
		if debug {
			level = slog.LevelDebug
		}
		logging.SetupStderrLogging(level)

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

		// Auto-approve every session when --auto-approve is set. ACP clients
		// typically don't have a UI to surface permission requests, so this
		// is often required for the agent to make progress.
		if autoApprove {
			logging.Warn("ACP: auto-approve enabled — all permission requests will be granted automatically")
			enableAutoApprove(ctx, application)
		}

		// Pipe stdin through an io.Pipe so we can close the read end
		// on signal. Closing os.Stdin directly doesn't unblock a
		// blocking Read() on macOS, but closing a pipe writer does.
		pr, pw := io.Pipe()
		go func() {
			_, _ = io.Copy(pw, os.Stdin)
			pw.Close()
		}()

		transport := acp.NewTransport(pr, os.Stdout)
		server := acp.NewServer(application, transport)

		// Handle OS signals for graceful shutdown.
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		go func() {
			sig := <-sigCh
			logging.Info("ACP: received signal, shutting down", "signal", sig.String())
			cancel()
			pw.Close() // unblocks scanner.Scan() immediately
		}()

		err = server.Run(ctx)
		// Suppress context.Canceled — it's a clean shutdown, not an error.
		if err != nil && ctx.Err() != nil {
			return nil
		}
		return err
	},
}

func init() {
	acpCmd.Flags().StringP("cwd", "c", "", "Working directory for the project")
	acpCmd.Flags().BoolP("debug", "d", false, "Enable debug logging")
	acpCmd.Flags().Bool("auto-approve", false, "Auto-approve all permission requests (dangerous — no human in the loop)")

	rootCmd.AddCommand(acpCmd)
}
