package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/opencode-ai/opencode/internal/api"
	"github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/bridge/mattermost"
	bridgesvc "github.com/opencode-ai/opencode/internal/bridge/service"
	"github.com/opencode-ai/opencode/internal/bridge/slack"
	"github.com/opencode-ai/opencode/internal/bridge/telegram"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/flow"
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
  opencode serve --auto-approve

  # Pick a specific agent (built-in or custom .opencode/agents/<name>.md)
  opencode serve -a hivemind`,
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
		agentID, _ := cmd.Flags().GetString("agent")

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

		// Pin the active agent if one was named on the command line. Without this,
		// `serve` always boots with whatever agent app.New picks first; OpenWork
		// and other SDK clients don't have a way to switch the active agent at
		// runtime, so the only way to host a non-default agent is via this flag.
		if agentID != "" {
			if err := application.SetActiveAgent(config.AgentName(agentID)); err != nil {
				return fmt.Errorf("invalid agent: %w", err)
			}
			logging.Info("Active agent pinned via --agent flag", "agent", agentID)
		}

		// Auto-approve every session created in the server when --auto-approve is set.
		// Useful for headless deployments where no human is present to grant permissions.
		// DANGEROUS in shared/networked deployments — every tool call is silently allowed.
		if autoApprove {
			logging.Warn("Auto-approve enabled — all permission requests will be granted automatically")
			enableAutoApprove(ctx, application)
		}

		// Conditionally start the chat-bridge orchestrator. The bridge
		// stays disabled unless an operator opted in by adding a
		// `router` section to .opencode.json with at least one enabled
		// channel + identity. When disabled, no chat-platform code is
		// loaded and /router/* routes return 404 (per the
		// chat-bridge-http-api spec).
		var bridgeSvc *bridgesvc.Service
		if cfg.Router != nil && cfg.Router.AnyChannelEnabled() {
			// Orchestrator-mediated-inbound (openspec Phase F): when
			// OPENCODE_BRIDGE_REGISTRAR_URL is set, mirror local
			// bind/unbind into the orchestrator's global binding
			// index via HTTP. The orchestrator's forwarder uses that
			// index to route inbound events back to THIS pod. Self-
			// identity (host:port the orchestrator should POST to)
			// + jobID come from env vars the orchestrator stamps on
			// the runner Pod spec.
			var (
				registrar     bridge.RemoteRegistrar
				selfHost      = os.Getenv("OPENCODE_BRIDGE_SELF_HOST")
				selfPortStr   = os.Getenv("OPENCODE_BRIDGE_SELF_PORT")
				registrarURL  = os.Getenv("OPENCODE_BRIDGE_REGISTRAR_URL")
				registrarPass = os.Getenv("OPENCODE_BRIDGE_REGISTRAR_PASSWORD")
				remoteJobID   = os.Getenv("OPENCODE_BRIDGE_JOB_ID")
				remoteProj    = os.Getenv("OPENCODE_BRIDGE_PROJECT_ID")
			)
			selfPort := 0
			if selfPortStr != "" {
				if p, err := strconv.Atoi(selfPortStr); err == nil {
					selfPort = p
				}
			}
			if registrarURL != "" {
				r, err := bridge.NewHTTPRegistrar(bridge.HTTPRegistrarConfig{
					BaseURL:  registrarURL,
					Password: registrarPass,
				})
				if err != nil {
					logging.Error("Bridge remote registrar init failed", "error", err)
					return err
				}
				registrar = r
				logging.Info("Bridge remote registrar wired",
					"url", registrarURL, "selfHost", selfHost, "selfPort", selfPort, "jobID", remoteJobID)
			}

			s, err := bridgesvc.New(bridgesvc.Dependencies{
				App:             application,
				DB:              conn,
				ProviderType:    cfg.SessionProvider.Type,
				ProjectID:       db.GetProjectID(cwd),
				DataDir:         cfg.Data.Directory,
				RouterCfg:       cfg.Router,
				RemoteRegistrar: registrar,
				RemoteSelfHost:  selfHost,
				RemoteSelfPort:  selfPort,
				RemoteJobID:     remoteJobID,
				RemoteProjectID: remoteProj,
			})
			if err != nil {
				logging.Error("Bridge orchestrator init failed", "error", err)
				return err
			}
			s.SetAdapterFactory(newBridgeAdapterFactory(cfg.Data.Directory, s))
			if err := s.Start(ctx); err != nil {
				logging.Error("Bridge orchestrator start failed", "error", err)
				return err
			}
			defer func() {
				if err := s.Stop(); err != nil {
					logging.Warn("Bridge orchestrator stop returned error", "error", err)
				}
			}()
			bridgeSvc = s

			// Wire the bridge into the flow engine so interactive: true
			// steps auto-bind their session to the resolved peer(s) before
			// agent.Run and auto-unbind on struct_output. Flow steps
			// without `interactive: true` are untouched.
			if hookSetter, ok := application.Flows.(flow.InteractiveHookSetter); ok {
				hookSetter.SetInteractiveHook(bridgeSvc.InteractiveHook())
			}

			// Wire the bridge into the agent factory so the router_send
			// agent tool registers conditionally per the
			// chat-bridge-agent-tool spec. Mid-flight config changes are
			// seen by NEW agents (constructed after the mutation), matching
			// the spec's "already-running agents do NOT gain the tool"
			// requirement.
			if application.AgentFactory != nil {
				mediaRoot := filepathJoin(cfg.Data.Directory, "bridge", "media")
				application.AgentFactory.SetBridgeSender(bridgeSvc, cfg.Router, mediaRoot)
			}
		}

		serverOpts := api.ServerOptions{
			Port:       port,
			Hostname:   hostname,
			CORSOrigin: corsOrigin,
		}
		if bridgeSvc != nil {
			serverOpts.Bridge = bridgeSvc
		}
		server := api.NewServer(application, serverOpts)

		// --flow / --flow-args / --flow-exit support (the k8s Job
		// entrypoint pattern). The server boots healthy first, THEN the
		// flow run is kicked off; --flow-exit causes the process to
		// terminate after the run completes (success or failure).
		if flowID, _ := cmd.Flags().GetString("flow"); flowID != "" {
			flowArgsPath, _ := cmd.Flags().GetString("flow-args")
			flowExit, _ := cmd.Flags().GetBool("flow-exit")
			flowExitGrace, _ := cmd.Flags().GetDuration("flow-exit-grace")
			flowFresh, _ := cmd.Flags().GetBool("flow-fresh")
			flowStartDelay, _ := cmd.Flags().GetDuration("flow-start-delay")
			if flowExit {
				if flowExitGrace < 0 {
					return fmt.Errorf("--flow-exit-grace must be ≥ 0 (got %s)", flowExitGrace)
				}
				if flowExitGrace > 60*time.Second {
					return fmt.Errorf("--flow-exit-grace must be ≤ 60s (got %s)", flowExitGrace)
				}
			}
			if flowStartDelay < 0 {
				return fmt.Errorf("--flow-start-delay must be ≥ 0 (got %s)", flowStartDelay)
			}
			if flowStartDelay > 30*time.Second {
				return fmt.Errorf("--flow-start-delay must be ≤ 30s (got %s)", flowStartDelay)
			}
			if err := scheduleAutoFlow(ctx, cancel, server, flowID, flowArgsPath, flowExit, flowExitGrace, flowFresh, flowStartDelay); err != nil {
				return err
			}
		}

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

// newBridgeAdapterFactory returns a bridgesvc.AdapterFactory closure that
// constructs the right platform adapter for an identity. cmd/serve.go is
// the only place in the codebase that imports all three platform packages
// together — the bridge service itself stays decoupled.
//
// The dataDir is the project-level data directory; the bridge media store
// lives at <dataDir>/bridge/media/. The svc reference lets the factory
// build store-backed callbacks for adapters that need them (Telegram's
// private-mode allowlist).
func newBridgeAdapterFactory(dataDir string, svc *bridgesvc.Service) bridgesvc.AdapterFactory {
	mediaDir := filepathJoin(dataDir, "bridge", "media")
	return func(_ context.Context, channel, identityID string, cfg *bridge.Config) (bridge.Adapter, error) {
		switch channel {
		case "telegram":
			if cfg.Channels.Telegram == nil {
				return nil, fmt.Errorf("telegram channel not configured")
			}
			for _, b := range cfg.Channels.Telegram.Bots {
				if b.ID != identityID {
					continue
				}
				access := telegram.AccessPublic
				if b.Access == "private" {
					access = telegram.AccessPrivate
				}
				// Private-mode bots require allowlist callbacks; build
				// them against the bridge store so /pair persists and
				// future inbound is gated correctly. Public-mode bots
				// don't need them but we wire them up unconditionally
				// — the constructor only validates them when Access ==
				// AccessPrivate.
				opts := telegram.Options{MediaDir: mediaDir}
				if svc != nil && svc.Store() != nil {
					store := svc.Store()
					// Use remoteProjectID so single-process AND mediated-
					// inbound deployments key on the SAME project_id as
					// seedIdentityAllowlist. See Service.RemoteProjectID
					// docstring for the contract.
					projectID := svc.RemoteProjectID()
					opts.Allowlisted = func(ctx context.Context, peerID string) (bool, error) {
						return store.IsAllowlisted(ctx, projectID, "telegram", b.ID, peerID)
					}
					opts.AddAllowlist = func(ctx context.Context, peerID string) error {
						return store.AddAllowlistEntry(ctx, projectID, "telegram", b.ID, peerID)
					}
				}
				return telegram.New(telegram.Identity{
					ID:              b.ID,
					Token:           b.Token,
					Access:          access,
					PairingCodeHash: b.PairingCodeHash,
					GroupsEnabled:   b.GroupsEnabled,
					Inbound:         b.Inbound,
				}, opts)
			}
		case "slack":
			if cfg.Channels.Slack == nil {
				return nil, fmt.Errorf("slack channel not configured")
			}
			for _, a := range cfg.Channels.Slack.Apps {
				if a.ID != identityID {
					continue
				}
				opts := slack.Options{MediaDir: mediaDir}
				if svc != nil && svc.Store() != nil && a.Access == slack.AccessPrivate {
					store := svc.Store()
					// remoteProjectID — see telegram branch above.
					projectID := svc.RemoteProjectID()
					opts.Allowlisted = func(ctx context.Context, identifier string) (bool, error) {
						return store.IsAllowlisted(ctx, projectID, "slack", a.ID, identifier)
					}
				}
				return slack.New(slack.Identity{
					ID:            a.ID,
					BotToken:      a.BotToken,
					AppToken:      a.AppToken,
					GroupsEnabled: a.GroupsEnabled,
					Access:        a.Access,
					Inbound:       a.Inbound,
				}, opts)
			}
		case "mattermost":
			if cfg.Channels.Mattermost == nil {
				return nil, fmt.Errorf("mattermost channel not configured")
			}
			for _, m := range cfg.Channels.Mattermost.Instances {
				if m.ID != identityID {
					continue
				}
				opts := mattermost.Options{MediaDir: mediaDir}
				if svc != nil && svc.Store() != nil && m.Access == mattermost.AccessPrivate {
					store := svc.Store()
					// remoteProjectID — see telegram branch above.
					projectID := svc.RemoteProjectID()
					opts.Allowlisted = func(ctx context.Context, identifier string) (bool, error) {
						return store.IsAllowlisted(ctx, projectID, "mattermost", m.ID, identifier)
					}
				}
				return mattermost.New(mattermost.Identity{
					ID:            m.ID,
					ServerURL:     m.ServerURL,
					AccessToken:   m.AccessToken,
					GroupsEnabled: m.GroupsEnabled,
					Access:        m.Access,
					Inbound:       m.Inbound,
				}, opts)
			}
		}
		return nil, fmt.Errorf("identity %s:%s not found in cfg", channel, identityID)
	}
}

// filepathJoin is a tiny indirection so the imports block doesn't drag in
// path/filepath solely for this one call.
func filepathJoin(parts ...string) string {
	return filepath.Join(parts...)
}

// scheduleAutoFlow boots the requested flow once the server is healthy.
// Per the flow-api spec scenario "Server boots, becomes healthy, then
// auto-starts flow", we kick the flow off in a background goroutine
// AFTER api.NewServer returns (the server starts listening in Start;
// the goroutine waits for a brief window then POSTs).
//
// flowArgsPath, when non-empty, points at a JSON file the flow
// arguments are loaded from. The k8s-Job entrypoint pattern populates
// this file with the reviewer PeerRef(s) etc.
//
// flowExit, when true, cancels the parent context (triggering server
// shutdown) once the flow terminates.
func scheduleAutoFlow(ctx context.Context, cancel context.CancelFunc, server *api.Server, flowID, flowArgsPath string, flowExit bool, flowExitGrace time.Duration, flowFresh bool, flowStartDelay time.Duration) error {
	args := map[string]any{}
	if flowArgsPath != "" {
		data, err := os.ReadFile(flowArgsPath)
		if err != nil {
			return fmt.Errorf("flow-args: read %s: %w", flowArgsPath, err)
		}
		if err := api.UnmarshalFlowArgs(data, &args); err != nil {
			return fmt.Errorf("flow-args: parse %s: %w", flowArgsPath, err)
		}
	}

	// Default wait so the server starts listening before we kick the
	// flow. flowStartDelay (if positive) overrides — it gives external
	// SSE subscribers (orchestrators) time to connect and start
	// consuming flow.* events BEFORE the auto-flow emits its first
	// event. Without this, the orchestrator's reconnect-backoff often
	// misses the early flow.waiting_for_input / flow.step.started
	// events because the flow starts within ~500ms of API boot — way
	// faster than the orchestrator's 1s→2s→4s exponential reconnect
	// can bridge the gap.
	startWait := 250 * time.Millisecond
	if flowStartDelay > startWait {
		startWait = flowStartDelay
	}

	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(startWait):
		}

		runID, err := server.StartFlow(flowID, args, flowFresh)
		if err != nil {
			logging.Error("auto-flow start failed", "flow", flowID, "err", err)
			if flowExit {
				cancel()
			}
			return
		}
		logging.Info("auto-flow started", "flow", flowID, "runID", runID)

		if flowExit {
			// Watch for the run to terminate, then exit after the grace
			// window. The grace gives an external reconciliation reader
			// (orchestrator GET /flow/status) time to land its request
			// before the HTTP server shuts down — see openspec change
			// c2-agent-flow-http-migration design.md R3.
			go server.WaitFlowTerminal(ctx, runID, flowExitGrace, cancel)
		}
	}()
	return nil
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
	serveCmd.Flags().StringP("agent", "a", "", "Agent ID to use (e.g. coder, hivemind, or a custom one from .opencode/agents/)")
	serveCmd.Flags().String("flow", "", "Auto-start the named flow once the server is healthy (k8s Job entrypoint pattern)")
	serveCmd.Flags().String("flow-args", "", "Path to a JSON file with flow arguments (e.g. reviewers, ticket IDs)")
	serveCmd.Flags().Bool("flow-exit", false, "Exit the process when the auto-started flow completes (only honored with --flow)")
	serveCmd.Flags().Duration("flow-exit-grace", 5*time.Second, "Hold the HTTP server up this long after the auto-flow terminates so an external reconciler (e.g. orchestrator GET /flow/status) can land before shutdown. Capped at 60s. Only honored with --flow-exit. Default 5s.")
	serveCmd.Flags().Bool("flow-fresh", false, "Discard any existing per-step session state when auto-starting the flow (equivalent to `opencode -F <flow> -D`).")
	serveCmd.Flags().Duration("flow-start-delay", 0, "Wait this long after the HTTP server is healthy BEFORE auto-starting the flow. Gives external SSE subscribers (e.g. orchestrators) time to connect and start consuming flow.* events from the very first one. Capped at 30s. Default 0 (no extra delay beyond the 250ms boot wait).")

	rootCmd.AddCommand(serveCmd)
}
