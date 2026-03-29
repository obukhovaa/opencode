package cmd

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/format"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/pubsub"
	"github.com/opencode-ai/opencode/internal/tui"
	"github.com/opencode-ai/opencode/internal/version"
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "opencode",
	Short: "Terminal-based AI assistant for software development",
	Long: `OpenCode is a powerful terminal-based AI assistant that helps with software development tasks.
It provides an interactive chat interface with AI capabilities, code analysis, and LSP integration
to assist developers in writing, debugging, and understanding code directly from the terminal.`,
	Example: `
  # Run in interactive mode
  opencode

  # Run with debug logging
  opencode -d

  # Run with debug logging in a specific directory
  opencode -d -c /path/to/project

  # Print version
  opencode -v

  # Run with a specific agent
  opencode -a hivemind

  # Resume a specific session
  opencode -s <session-id>

  # Delete a session and start fresh with the same ID
  opencode -s <session-id> -D

  # Run a single non-interactive prompt
  opencode -p "Explain the use of context in Go"

  # Run a single non-interactive prompt with JSON output format
  opencode -p "Explain the use of context in Go" -f json

  # Run a non-interactive prompt with a 5-minute timeout
  opencode -p "Refactor this module" --timeout 5m

  # Run with a custom project ID to tag sessions
  opencode -P my-project-id

  # Run with auto-approve enabled (skip permission dialogs)
  opencode --auto-approve
  `,
	RunE: func(cmd *cobra.Command, args []string) error {
		// If the help flag is set, show the help message
		if cmd.Flag("help").Changed {
			cmd.Help()
			return nil
		}
		if cmd.Flag("version").Changed {
			fmt.Println(version.Version)
			return nil
		}

		// Load the config
		debug, _ := cmd.Flags().GetBool("debug")
		cwd, _ := cmd.Flags().GetString("cwd")
		prompt, _ := cmd.Flags().GetString("prompt")
		outputFormat, _ := cmd.Flags().GetString("output-format")
		quiet, _ := cmd.Flags().GetBool("quiet")
		agentID, _ := cmd.Flags().GetString("agent")
		sessionID, _ := cmd.Flags().GetString("session")
		deleteSession, _ := cmd.Flags().GetBool("delete")
		flowID, _ := cmd.Flags().GetString("flow")
		flowArgs, _ := cmd.Flags().GetStringArray("arg")
		argsFile, _ := cmd.Flags().GetString("args-file")
		timeoutStr, _ := cmd.Flags().GetString("timeout")
		projectID, _ := cmd.Flags().GetString("project-id")
		maxTurns, _ := cmd.Flags().GetInt("max-turns")
		autoApprove, _ := cmd.Flags().GetBool("auto-approve")

		if deleteSession && sessionID == "" && flowID == "" {
			return fmt.Errorf("--delete requires --session/-s or --flow/-F to be specified")
		}

		// Parse format option (may include schema)
		parsedOutputFormat, cliSchema, fmtErr := format.ParseWithSchema(outputFormat)
		if fmtErr != nil {
			return fmt.Errorf("invalid format option: %s\n%s", outputFormat, format.GetHelpText())
		}

		if cwd != "" {
			err := os.Chdir(cwd)
			if err != nil {
				return fmt.Errorf("failed to change directory: %v", err)
			}
		}
		if cwd == "" {
			c, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("failed to get current working directory: %v", err)
			}
			cwd = c
		}

		var spinner *format.Spinner
		if !quiet {
			spinner = format.NewSpinner("Starting...")
			spinner.Start()
		}

		_, err := config.Load(cwd, debug)
		if err != nil {
			if spinner != nil {
				spinner.Stop()
			}
			return err
		}

		if maxTurns < 0 {
			if spinner != nil {
				spinner.Stop()
			}
			return fmt.Errorf("--max-turns must be a positive integer")
		}
		if maxTurns > 0 {
			config.Get().MaxTurns = maxTurns
		}

		// Connect DB, this will also run migrations
		conn, err := db.Connect()
		if err != nil {
			if spinner != nil {
				spinner.Stop()
			}
			return err
		}

		// Create main context for the application
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		app, err := app.New(ctx, conn, cliSchema, projectID)
		if err != nil {
			if spinner != nil {
				spinner.Stop()
			}
			logging.Error("Failed to create app: %v", err)
			return err
		}
		defer app.Shutdown()

		// Set active agent if specified
		if agentID != "" {
			if _err := app.SetActiveAgent(config.AgentName(agentID)); _err != nil {
				if spinner != nil {
					spinner.Stop()
				}
				return fmt.Errorf("invalid agent: %w", _err)
			}
		}

		if autoApprove {
			if prompt != "" || flowID != "" {
				logging.Debug("--auto-approve ignored in non-interactive mode (already auto-approved)")
			} else {
				app.AutoApprove = true
			}
		}

		// Look up session if specified, or store ID for on-demand creation
		// Skip for flow mode — flows manage sessions internally
		if sessionID != "" && flowID == "" {
			sess, _err := app.Sessions.Get(ctx, sessionID)
			if _err != nil {
				logging.Info("Session not found, will create with provided ID", "session_id", sessionID)
			} else if deleteSession {
				if delErr := app.Sessions.Delete(ctx, sessionID); delErr != nil {
					if spinner != nil {
						spinner.Stop()
					}
					return fmt.Errorf("failed to delete session %q: %w", sessionID, delErr)
				}
				logging.Info("Deleted existing session, will recreate with same ID", "session_id", sessionID)
			} else {
				app.InitialSession = &sess
			}
			app.InitialSessionID = sessionID
		}

		if spinner != nil {
			spinner.Stop()
		}

		// Non-interactive flow mode
		if flowID != "" {
			nonInteractiveCtx := ctx
			var timeoutCancel context.CancelFunc
			if timeoutStr != "" {
				timeoutDuration, parseErr := time.ParseDuration(timeoutStr)
				if parseErr != nil {
					return fmt.Errorf("invalid --timeout value %q: %w (use formats like 10s, 30m, 1h)", timeoutStr, parseErr)
				}
				nonInteractiveCtx, timeoutCancel = context.WithTimeout(ctx, timeoutDuration)
				defer timeoutCancel()
			}
			_err := runFlowNonInteractive(nonInteractiveCtx, app, flowID, prompt, sessionID, deleteSession, flowArgs, argsFile, quiet)
			app.ForceShutdown()
			return _err
		}

		// Non-interactive mode
		if prompt != "" {
			nonInteractiveCtx := ctx
			var timeoutCancel context.CancelFunc
			if timeoutStr != "" {
				timeoutDuration, parseErr := time.ParseDuration(timeoutStr)
				if parseErr != nil {
					return fmt.Errorf("invalid --timeout value %q: %w (use formats like 10s, 30m, 1h)", timeoutStr, parseErr)
				}
				nonInteractiveCtx, timeoutCancel = context.WithTimeout(ctx, timeoutDuration)
				defer timeoutCancel()
			}
			_err := runNonInteractive(nonInteractiveCtx, app, prompt, parsedOutputFormat, quiet)
			app.ForceShutdown()
			return _err
		}

		// Interactive mode
		// Set up the TUI
		program := tea.NewProgram(
			tui.New(app),
		)

		// Setup the subscriptions, this will send services events to the TUI
		ch, permCh, cancelSubs := setupSubscriptions(app, ctx)

		// Create a context for the TUI message handler
		tuiCtx, tuiCancel := context.WithCancel(ctx)
		var tuiWg sync.WaitGroup
		tuiWg.Add(2)

		// Set up message handling for the TUI
		go func() {
			defer tuiWg.Done()
			defer logging.RecoverPanic("TUI-message-handler", func() {
				attemptTUIRecovery(program)
			})

			for {
				select {
				case <-tuiCtx.Done():
					logging.Info("TUI message handler shutting down")
					return
				case msg, ok := <-ch:
					if !ok {
						logging.Info("TUI message channel closed")
						return
					}
					program.Send(msg)
				}
			}
		}()

		// Dedicated handler for permission events — must never be dropped
		go func() {
			defer tuiWg.Done()
			defer logging.RecoverPanic("TUI-permission-handler", func() {
				attemptTUIRecovery(program)
			})

			for {
				select {
				case <-tuiCtx.Done():
					logging.Info("TUI permission handler shutting down")
					return
				case msg, ok := <-permCh:
					if !ok {
						logging.Info("TUI permission channel closed")
						return
					}
					program.Send(msg)
				}
			}
		}()

		// Cleanup function for when the program exits
		cleanup := func() {
			// Cancel TUI message handler and subscriptions immediately
			tuiCancel()
			cancelSubs()

			// Shutdown the app (LSP servers etc.) concurrently with waiting for TUI handler
			var cleanupWg sync.WaitGroup
			cleanupWg.Add(2)
			go func() {
				defer cleanupWg.Done()
				app.Shutdown()
			}()
			go func() {
				defer cleanupWg.Done()
				tuiWg.Wait()
			}()
			cleanupWg.Wait()

			logging.Info("All goroutines cleaned up")
		}

		// Run the TUI
		result, err := program.Run()
		cleanup()

		if err != nil {
			logging.Error("TUI error: %v", err)
			return fmt.Errorf("TUI error: %v", err)
		}

		logging.Info("TUI exited with result: %v", result)
		return nil
	},
}

// attemptTUIRecovery tries to recover the TUI after a panic
func attemptTUIRecovery(program *tea.Program) {
	logging.Info("Attempting to recover TUI after panic")

	// We could try to restart the TUI or gracefully exit
	// For now, we'll just quit the program to avoid further issues
	program.Quit()
}

func setupSubscriber[T any](
	ctx context.Context,
	wg *sync.WaitGroup,
	name string,
	subscriber func(context.Context) <-chan pubsub.Event[T],
	outputCh chan<- tea.Msg,
) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer logging.RecoverPanic(fmt.Sprintf("subscription-%s", name), nil)

		subCh := subscriber(ctx)

		for {
			select {
			case event, ok := <-subCh:
				if !ok {
					logging.Info("subscription channel closed", "name", name)
					return
				}

				var msg tea.Msg = event

				select {
				case outputCh <- msg:
				case <-time.After(2 * time.Second):
					logging.Warn("message dropped due to slow consumer", "name", name)
				case <-ctx.Done():
					logging.Info("subscription cancelled", "name", name)
					return
				}
			case <-ctx.Done():
				logging.Info("subscription cancelled", "name", name)
				return
			}
		}
	}()
}

// setupBlockingSubscriber sets up a subscriber that blocks until the event is
// delivered. Used for critical events like permissions that must never be dropped.
func setupBlockingSubscriber[T any](
	ctx context.Context,
	wg *sync.WaitGroup,
	name string,
	subscriber func(context.Context) <-chan pubsub.Event[T],
	outputCh chan<- tea.Msg,
) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer logging.RecoverPanic(fmt.Sprintf("subscription-%s", name), nil)

		subCh := subscriber(ctx)

		for {
			select {
			case event, ok := <-subCh:
				if !ok {
					logging.Info("subscription channel closed", "name", name)
					return
				}

				var msg tea.Msg = event

				select {
				case outputCh <- msg:
				case <-ctx.Done():
					logging.Info("subscription cancelled", "name", name)
					return
				}
			case <-ctx.Done():
				logging.Info("subscription cancelled", "name", name)
				return
			}
		}
	}()
}

func setupSubscriptions(app *app.App, parentCtx context.Context) (chan tea.Msg, chan tea.Msg, func()) {
	ch := make(chan tea.Msg, 100)
	permCh := make(chan tea.Msg, 10)

	wg := sync.WaitGroup{}
	ctx, cancel := context.WithCancel(parentCtx) // Inherit from parent context

	setupSubscriber(ctx, &wg, "logging", logging.Subscribe, ch)
	setupSubscriber(ctx, &wg, "sessions", app.Sessions.Subscribe, ch)
	setupSubscriber(ctx, &wg, "messages", app.Messages.Subscribe, ch)
	setupBlockingSubscriber(ctx, &wg, "permissions", app.Permissions.Subscribe, permCh)
	for name, primaryAgent := range app.PrimaryAgents {
		setupSubscriber(ctx, &wg, fmt.Sprintf("agent-%s", name), primaryAgent.Subscribe, ch)
	}

	cleanupFunc := func() {
		logging.Info("Cancelling all subscriptions")
		cancel() // Signal all goroutines to stop

		waitCh := make(chan struct{})
		go func() {
			defer logging.RecoverPanic("subscription-cleanup", nil)
			wg.Wait()
			close(waitCh)
		}()

		select {
		case <-waitCh:
			logging.Info("All subscription goroutines completed successfully")
			close(ch)
			close(permCh)
		case <-time.After(5 * time.Second):
			logging.Warn("Timed out waiting for some subscription goroutines to complete")
			close(ch)
			close(permCh)
		}
	}
	return ch, permCh, cleanupFunc
}

func Execute() {
	err := rootCmd.Execute()
	if err != nil {
		os.Exit(1)
	}
}

func init() {
	rootCmd.Flags().BoolP("help", "h", false, "Help")
	rootCmd.Flags().BoolP("version", "v", false, "Version")
	rootCmd.Flags().BoolP("debug", "d", false, "Debug")
	rootCmd.Flags().StringP("cwd", "c", "", "Current working directory")
	rootCmd.Flags().StringP("prompt", "p", "", "Prompt to run in non-interactive mode")
	rootCmd.Flags().StringP("agent", "a", "", "Agent ID to use (e.g. coder, hivemind)")
	rootCmd.Flags().StringP("session", "s", "", "Session ID to resume or create")
	rootCmd.Flags().BoolP("delete", "D", false, "Delete the session specified by --session/-s before starting")

	// Add format flag with validation logic
	rootCmd.Flags().StringP("output-format", "f", format.Text.String(),
		"Output format for non-interactive mode (text, json, json_schema='{...}' or json_schema=/path/to/schema.json)")

	// Add quiet flag to hide spinner in non-interactive mode
	rootCmd.Flags().BoolP("quiet", "q", false, "Hide spinner in non-interactive mode")

	// Add flow execution flags
	rootCmd.Flags().StringP("flow", "F", "", "Flow ID to execute (non-interactive only)")
	rootCmd.Flags().StringArrayP("arg", "A", nil, "Flow argument as key=value (repeatable, used with --flow)")
	rootCmd.Flags().String("args-file", "", "JSON file with flow arguments (used with --flow)")

	// Add timeout flag for non-interactive mode
	rootCmd.Flags().StringP("timeout", "t", "", "Timeout for non-interactive mode (e.g. 10s, 30m, 1h)")

	// Add project ID flag
	rootCmd.Flags().StringP("project-id", "P", "", "Custom project ID (overrides auto-detected Git/directory-based ID)")

	// Add max-turns flag
	rootCmd.Flags().Int("max-turns", 0, "Maximum number of agent tool-use turns per request (default 100)")

	// Add auto-approve flag
	rootCmd.Flags().Bool("auto-approve", false, "Start with auto-approve enabled (skip permission dialogs for ask rules)")

	// Register custom validation for the format flag
	rootCmd.RegisterFlagCompletionFunc("output-format", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		return format.SupportedFormats, cobra.ShellCompDirectiveNoFileComp
	})
}
