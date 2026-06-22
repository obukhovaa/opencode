// Package service is the bridge orchestrator. It owns adapter lifecycle,
// inbound message dispatch, multi-reviewer fan-out, the per-session
// dispatch goroutine, HTTP route registration under /router/*, and the
// router_send agent tool wiring.
//
// Phase 1.7 ships only the lifecycle skeleton (Start/Stop, lock manager,
// recover-wrapped goroutine launcher). Adapter wiring, inbound dispatch,
// and HTTP routes are filled in across later phases — search for "Phase N"
// markers in this package to find the in-progress slots.
package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"

	"github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/bridge/store"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/logging"
)

// Service is the bridge orchestrator. One Service runs per opencode
// process; it owns every chat-platform adapter, the binding store, the
// per-identity single-writer lock, and the chat-bridge HTTP surface.
//
// Construct via New and call Start to launch background goroutines. Stop
// (or canceling the parent context) shuts everything down — adapter
// goroutines exit cleanly, the identity lock manager releases its
// per-identity GET_LOCK or file lock, and Wait blocks until all
// background work has drained.
type Service struct {
	cfg       *bridge.Config
	app       *app.App
	store     store.Store
	lockMgr   store.IdentityLockManager
	projectID string
	dataDir   string

	mu      sync.Mutex
	started bool
	stopped bool
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup

	// cfgMu guards every concurrent read and mutation of cfg.Channels.*.
	// Writes happen from the HTTP identity / groups handlers when an
	// operator POSTs an upsert or toggle; reads happen from the same
	// handlers (GET), from send.go::BoundPeersSnapshot (called when an
	// agent uses the router_send tool), and from health snapshots.
	// The mutex MUST NOT be held across I/O — in particular, the
	// config.UpdateCfgFile callback runs without it. Mutations to
	// s.cfg happen under the write lock; the disk write happens after
	// the lock is released (consistent with the spec's
	// "in-memory-cfg-mutation-alongside-disk-write" contract).
	cfgMu sync.RWMutex

	// adapters keyed by "channel:identity" — populated lazily as Phase 2
	// (Mattermost), Phase 4 (Telegram), and Phase 5 (Slack) land.
	adapters map[string]bridge.Adapter

	// adapterLocks tracks the identity locks acquired by RegisterAdapter
	// so DeregisterAdapter can release them in the same lifecycle path.
	adapterMu    sync.Mutex
	adapterLocks map[string]store.LockHandle

	// inboundCh is the shared channel adapters push inbound messages onto.
	// One adapter goroutine per identity pushes Inbound values; one
	// orchestrator goroutine (dispatchLoop) drains and fans into the right
	// per-session dispatcher. Buffered so a momentary stall in
	// dispatchLoop doesn't block adapter goroutines.
	inboundCh chan bridge.Inbound

	// dispatchers map session_id → its per-session dispatch goroutine.
	// Created on first Bind(session) or first user-initiated inbound,
	// torn down on Unbind or session GC. Protected by dispatchMu so
	// adapter goroutines and HTTP handlers can race on creation without
	// duplicating dispatchers.
	dispatchMu  sync.Mutex
	dispatchers map[string]*sessionDispatch

	// questionRouter watches the question.Service broker and routes
	// agent-prompted questions through bound peers. nil when the bridge
	// is constructed without a question service.
	questionRouter *QuestionRouter

	// permissionRouter watches the permission.Service broker and
	// auto-resolves tool-permission requests for bridge-bound
	// sessions per cfg.Router.PermissionMode. nil when the bridge
	// is constructed without a permission service or with an empty
	// PermissionMode (in which case opencode's default permission
	// UI handles requests as usual).
	permissionRouter *PermissionRouter

	// cronOutputRouter watches the cron.Service broker and forwards
	// each successful run's result to every peer bound to the cron's
	// session. Without it the synthetic tool_call/tool_result pair
	// the scheduler writes is invisible to the chat surface — the
	// dispatcher's parts subscription only spans the lifetime of a
	// single inbound turn.
	cronOutputRouter *CronOutputRouter

	// adapterFactory builds adapters for each enabled identity. cmd/serve.go
	// installs one that imports the per-platform packages; tests use stubs
	// or leave it nil (LaunchAdapter becomes a no-op).
	adapterFactory AdapterFactory

	// remoteRegistrar, when non-nil, mirrors local bind/unbind
	// operations into the orchestrator's global binding index via
	// HTTP. Wired by cmd/serve.go when OPENCODE_BRIDGE_REGISTRAR_URL
	// is set (openspec change bridge-orchestrator-mediated-inbound,
	// Phase F). When nil, the bridge runs in legacy single-container
	// mode — local bind alone is sufficient because the runner pod
	// owns its own platform listener.
	remoteRegistrar bridge.RemoteRegistrar

	// remoteSelfHost / remoteSelfPort identify THIS container in
	// orchestrator-mediated mode. Stamped onto every Register call
	// so the orchestrator's forwarder knows where to POST inbound
	// events. Empty / zero → don't call Register (defensive — the
	// runner shouldn't have registrar wired without self-identity).
	remoteSelfHost  string
	remoteSelfPort  int
	remoteJobID     string
	remoteProjectID string
}

// Dependencies bundles the inputs Service needs at construction time.
// Wiring lives in cmd/serve.go where the App, DB, and project ID are
// already available.
type Dependencies struct {
	App          *app.App
	DB           *sql.DB
	ProviderType config.ProviderType
	ProjectID    string
	DataDir      string
	RouterCfg    *bridge.Config

	// RemoteRegistrar, when non-nil, enables the orchestrator-
	// mediated-inbound bind path: every OnInteractiveStepStart
	// mirrors the local bind into the orchestrator's global binding
	// index, and OnInteractiveStepComplete mirrors the unbind. See
	// openspec change `bridge-orchestrator-mediated-inbound`,
	// Phase F.
	//
	// RemoteSelfHost / RemoteSelfPort identify THIS container in
	// the orchestrator's binding row — they are stamped onto every
	// Register call so the forwarder knows where to POST inbound
	// events. RemoteJobID groups all of a job's bindings so the
	// orchestrator's DeregisterByJob hook can clear them en masse.
	// RemoteProjectID matches the orchestrator's bridge project
	// scope (defaults to "default" when empty).
	//
	// All fields are individually optional: if RemoteRegistrar is
	// nil OR RemoteSelfHost/RemoteSelfPort/RemoteJobID is unset, the
	// runner stays in single-container mode and only the local
	// binding row is written. cmd/serve.go gates the wiring on
	// OPENCODE_BRIDGE_REGISTRAR_URL being set.
	RemoteRegistrar bridge.RemoteRegistrar
	RemoteSelfHost  string
	RemoteSelfPort  int
	RemoteJobID     string
	RemoteProjectID string
}

// New constructs a Service from the given dependencies. It does NOT start
// any goroutines — call Start to do that. Returns an error if the
// dependencies are inconsistent (e.g. RouterCfg is nil, or DataDir is
// empty for SQLite).
func New(deps Dependencies) (*Service, error) {
	if deps.RouterCfg == nil {
		return nil, errors.New("bridge.service: RouterCfg is required (call only when cfg.Router != nil)")
	}
	if deps.App == nil {
		return nil, errors.New("bridge.service: App is required")
	}
	if deps.DB == nil {
		return nil, errors.New("bridge.service: DB is required")
	}
	if deps.ProjectID == "" {
		return nil, errors.New("bridge.service: ProjectID is required")
	}
	lockMgr, err := store.NewIdentityLockManager(deps.ProviderType, deps.DataDir, providerDB(deps.ProviderType, deps.DB))
	if err != nil {
		return nil, fmt.Errorf("bridge.service: identity lock manager: %w", err)
	}
	projectID := deps.RemoteProjectID
	if projectID == "" {
		projectID = "default"
	}
	return &Service{
		cfg:             deps.RouterCfg,
		app:             deps.App,
		store:           store.New(deps.DB, deps.ProviderType),
		lockMgr:         lockMgr,
		projectID:       deps.ProjectID,
		dataDir:         deps.DataDir,
		adapters:        make(map[string]bridge.Adapter),
		inboundCh:       make(chan bridge.Inbound, 64),
		dispatchers:     make(map[string]*sessionDispatch),
		remoteRegistrar: deps.RemoteRegistrar,
		remoteSelfHost:  deps.RemoteSelfHost,
		remoteSelfPort:  deps.RemoteSelfPort,
		remoteJobID:     deps.RemoteJobID,
		remoteProjectID: projectID,
	}, nil
}

// providerDB returns the *sql.DB the MySQL lock manager needs, or nil for
// SQLite (where the manager doesn't use it).
func providerDB(t config.ProviderType, db *sql.DB) *sql.DB {
	if t == config.ProviderMySQL {
		return db
	}
	return nil
}

// Start launches the bridge in the background. The supplied context is
// used as the parent for every adapter goroutine and the orchestrator's
// internal loops — canceling it triggers a clean shutdown. Start is
// idempotent; calling it twice returns an error on the second call.
//
// Per the chat-bridge spec, Start MUST NOT block opencode's other
// subsystems if any individual adapter fails to come up. Per-identity
// failures (bad token, auth rejected, identity lock contention) are
// logged and surfaced via /router/health but the API server is unaffected.
func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return errors.New("bridge.service: already started")
	}
	s.started = true
	s.ctx, s.cancel = context.WithCancel(ctx)
	s.mu.Unlock()

	logging.Info("Bridge orchestrator starting",
		"project_id", s.projectID,
		"telegram_enabled", s.cfg.Channels.Telegram != nil && s.cfg.Channels.Telegram.Enabled,
		"slack_enabled", s.cfg.Channels.Slack != nil && s.cfg.Channels.Slack.Enabled,
		"mattermost_enabled", s.cfg.Channels.Mattermost != nil && s.cfg.Channels.Mattermost.Enabled,
	)

	// Inbound dispatch loop: reads from the shared inboundCh that
	// adapters push onto, resolves each to a per-session dispatcher, and
	// forwards. The loop exits when ctx is cancelled. Subsequent phases
	// (adapter registration in 3.3, /router/bind in 6.2) reuse this same
	// channel-pump model.
	s.launchSupervised("orchestrator-inbound-dispatch", s.runInboundLoop)

	// Question router: subscribes to question.Service so agent-prompted
	// questions fan out across bound peers via the chat surface.
	s.questionRouter = s.newQuestionRouter()

	// Permission router: subscribes to permission.Service so
	// agent-requested tool permissions are auto-resolved per
	// cfg.Router.PermissionMode ("allow" / "deny") for bridge-bound
	// sessions. A no-op for sessions not bound to chat peers — those
	// continue to use opencode's interactive permission UI.
	s.permissionRouter = s.newPermissionRouter()

	// Cron output router: subscribes to cron.Service so each
	// successful run's result is forwarded back to every peer bound
	// to the cron's session. Without it the synthetic messages the
	// scheduler writes are only visible in the TUI / cron-jobs page.
	s.cronOutputRouter = s.newCronOutputRouter()

	// Boot-time adapter launch: iterate every enabled identity in the
	// router config and call LaunchAdapter. Per-identity failures are
	// logged via /router/health but do NOT prevent other identities or
	// the API server from coming up (chat-bridge spec "Per-identity
	// startup isolation").
	s.launchEnabledAdapters(s.ctx)
	return nil
}

// launchEnabledAdapters walks cfg.Router and constructs an adapter for
// every enabled identity. Called once from Start; subsequent hot-adds
// route through LaunchAdapter via the identity-CRUD HTTP handler.
func (s *Service) launchEnabledAdapters(ctx context.Context) {
	if s.cfg == nil {
		return
	}
	if t := s.cfg.Channels.Telegram; t != nil && t.Enabled {
		for _, b := range t.Bots {
			if !b.Enabled {
				continue
			}
			if err := s.LaunchAdapter(ctx, "telegram", b.ID); err != nil {
				logging.Warn("bridge: telegram adapter launch failed",
					"identity", b.ID, "err", err)
			}
		}
	}
	if sl := s.cfg.Channels.Slack; sl != nil && sl.Enabled {
		for _, a := range sl.Apps {
			if !a.Enabled {
				continue
			}
			s.seedIdentityAllowlist(ctx, "slack", a.ID, a.Access, a.PeerAllowlist)
			if err := s.LaunchAdapter(ctx, "slack", a.ID); err != nil {
				logging.Warn("bridge: slack adapter launch failed",
					"identity", a.ID, "err", err)
			}
		}
	}
	if m := s.cfg.Channels.Mattermost; m != nil && m.Enabled {
		for _, mm := range m.Instances {
			if !mm.Enabled {
				continue
			}
			s.seedIdentityAllowlist(ctx, "mattermost", mm.ID, mm.Access, mm.PeerAllowlist)
			if err := s.LaunchAdapter(ctx, "mattermost", mm.ID); err != nil {
				logging.Warn("bridge: mattermost adapter launch failed",
					"identity", mm.ID, "err", err)
			}
		}
	}
}

// seedIdentityAllowlist upserts the operator-configured `peerAllowlist`
// for a private identity into `bridge_allowlist`. No-op when the
// identity is public OR has an empty list. Existing rows are preserved
// — config is additive.
//
// The seed uses remoteProjectID rather than the local projectID
// because the allowlist is consulted by the orchestrator's forwarder
// (in mediated-inbound deployments), which keys on the SAME
// remoteProjectID the runner already uses for POST /router/bindings/register.
// If we seeded with the local s.projectID (db.GetProjectID(cwd) →
// e.g. "gitlab.com/org/repo") the orchestrator would query with
// "default" (its hardcoded bridgeProjectID) and never find a match,
// silently dropping every inbound from peers the operator allowlisted.
// For single-process (non-mediated) deployments where the runner does
// the allowlist check itself, remoteProjectID defaults to "default"
// anyway — same value either way, so single-process behaviour is
// preserved.
func (s *Service) seedIdentityAllowlist(ctx context.Context, channel, identityID, access string, peers []string) {
	if access != "private" || len(peers) == 0 || s.store == nil {
		return
	}
	inserted, err := s.store.SeedAllowlist(ctx, s.remoteProjectID, channel, identityID, peers)
	if err != nil {
		logging.Warn("bridge: allowlist seed failed",
			"channel", channel, "identity", identityID, "err", err)
		return
	}
	logging.Info("bridge: allowlist seeded",
		"channel", channel, "identity", identityID, "project_id", s.remoteProjectID, "inserted", inserted, "total_config", len(peers))
}

// runInboundLoop drains the shared inboundCh and forwards each message
// through Service.dispatchInbound (which handles session resolution,
// attribution, and dispatcher creation).
func (s *Service) runInboundLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case in, ok := <-s.inboundCh:
			if !ok {
				return
			}
			s.dispatchInbound(ctx, in)
		}
	}
}

// InboundChan returns the channel adapters push inbound messages onto.
// Exposed for tests and (later) for per-adapter goroutine wiring in
// Phase 3.3.
func (s *Service) InboundChan() chan<- bridge.Inbound {
	return s.inboundCh
}

// Stop cancels every adapter goroutine, releases the identity lock
// manager, and waits for all background work to drain. Idempotent and
// safe to call from any goroutine.
func (s *Service) Stop() error {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return nil
	}
	s.stopped = true
	cancel := s.cancel
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	s.tearDownDispatchers()
	s.releaseAdapterLocks()
	s.wg.Wait()

	var err error
	if s.lockMgr != nil {
		err = s.lockMgr.Close()
	}
	logging.Info("Bridge orchestrator stopped")
	return err
}

// releaseAdapterLocks releases every per-identity lock the orchestrator
// acquired via RegisterAdapter. Called during Stop; tolerant of partial
// state if Stop was called before any adapter registered.
func (s *Service) releaseAdapterLocks() {
	s.adapterMu.Lock()
	locks := s.adapterLocks
	s.adapterLocks = nil
	s.adapterMu.Unlock()
	for _, lk := range locks {
		_ = lk.Release()
	}
}

// tearDownDispatchers closes every per-session dispatcher. Called by Stop
// before draining background goroutines so each dispatcher's run loop
// observes its inbound channel close and exits promptly.
func (s *Service) tearDownDispatchers() {
	s.dispatchMu.Lock()
	dispatchers := s.dispatchers
	s.dispatchers = make(map[string]*sessionDispatch)
	s.dispatchMu.Unlock()
	for _, d := range dispatchers {
		d.close()
	}
}

// Adapter returns the adapter for the given (channel, identity) tuple, or
// nil if no such adapter is registered. Exposed for tests and the
// router_send agent tool wiring (Phase 9).
func (s *Service) Adapter(channel, identityID string) bridge.Adapter {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.adapters[adapterKey(channel, identityID)]
}

// Store returns the bridge's persistence layer. Exposed for HTTP handlers
// under /router/* and tests.
func (s *Service) Store() store.Store {
	return s.store
}

// ProjectID returns the project ID the service is scoped to. Exposed so
// factory code in cmd/serve.go can build per-(channel, identity) store
// callbacks (e.g. Telegram's private-mode allowlist) without owning a
// separate copy of the project resolution logic.
func (s *Service) ProjectID() string {
	return s.projectID
}

// RemoteProjectID returns the project ID the bridge uses for entries
// the orchestrator's forwarder consults (allowlist, bindings register).
// In mediated-inbound deployments this is the OPENCODE_BRIDGE_PROJECT_ID
// env value the orchestrator stamps on runner pods (defaults to
// "default" when unset, matching the orchestrator's hardcoded
// bridgeProjectID). Callers that need the projectID a downstream
// consumer (orchestrator) will key on — NOT the local cwd hash —
// should use this method.
func (s *Service) RemoteProjectID() string {
	return s.remoteProjectID
}

// Config returns the bridge configuration snapshot the service was
// constructed with. Note: this is a snapshot — mutations to .opencode.json
// via the bridge HTTP CRUD endpoints update cfg.Router in-memory but the
// orchestrator's cached snapshot does NOT auto-refresh. Hot-reload of
// identity additions is handled by tearing down and re-launching the
// adapter; see Phase 6.3.
func (s *Service) Config() *bridge.Config {
	return s.cfg
}

// launchSupervised runs fn on a background goroutine with defer recover()
// + logging. Every adapter goroutine MUST go through this helper rather
// than calling `go` directly — a single panic in chat-platform handling
// MUST NOT kill the opencode API server.
func (s *Service) launchSupervised(name string, fn func(context.Context)) {
	s.launchSupervisedCtx(name, s.ctx, fn)
}

// launchSupervisedCtx is the launchSupervised variant for short-lived
// helper goroutines whose lifetime is tied to a narrower context than
// the service's own (e.g. drainParts is tied to a single agent.Run
// invocation). Registration with s.wg still ensures Stop() waits for
// the goroutine to exit cleanly.
func (s *Service) launchSupervisedCtx(name string, ctx context.Context, fn func(context.Context)) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			if r := recover(); r != nil {
				logging.Error("bridge: supervised goroutine panicked", "name", name, "panic", r)
			}
		}()
		fn(ctx)
	}()
}

func adapterKey(channel, identityID string) string {
	return channel + ":" + identityID
}

// EnsureDBProjectID returns the project ID for the working directory,
// using opencode's existing GetProjectID helper. Exposed here so
// cmd/serve.go can compute it once and pass it via Dependencies.
func EnsureDBProjectID(workingDir string) string {
	return db.GetProjectID(workingDir)
}
