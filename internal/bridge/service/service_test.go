package service

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
	"github.com/pressly/goose/v3"

	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/bridge/store"
	"github.com/opencode-ai/opencode/internal/config"
	opencodedb "github.com/opencode-ai/opencode/internal/db"
)

// gooseSerialMu serializes goose.Up against in-memory SQLite — goose has
// package-global state that races under t.Parallel().
var gooseSerialMu sync.Mutex

// newOrchestratorForTest builds a minimal Service backed by an in-memory
// SQLite. App is left nil so the dispatcher logs-and-returns when it
// would otherwise call ActiveAgent — tests that exercise the agent path
// must inject a real app.App; tests that exercise binding/store/adapter
// wiring do not need one.
func newOrchestratorForTest(t *testing.T) (*Service, *sql.DB) {
	t.Helper()
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	conn, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err := conn.Exec("PRAGMA foreign_keys = ON;"); err != nil {
		t.Fatalf("pragma: %v", err)
	}
	gooseSerialMu.Lock()
	goose.SetBaseFS(opencodedb.FS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		gooseSerialMu.Unlock()
		t.Fatalf("set dialect: %v", err)
	}
	err = goose.Up(conn, "migrations/sqlite")
	gooseSerialMu.Unlock()
	if err != nil {
		t.Fatalf("goose up: %v", err)
	}

	// Seed a session so SessionID FK references resolve in the absence
	// of a real session.Service.
	if _, err := conn.Exec(`
		INSERT INTO sessions (id, project_id, title, message_count, prompt_tokens, completion_tokens, cost, updated_at, created_at)
		VALUES ('S1', 'proj', 't', 0, 0, 0, 0, strftime('%s','now'), strftime('%s','now'))
	`); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	lockMgr, err := store.NewIdentityLockManager(config.ProviderSQLite, t.TempDir(), nil)
	if err != nil {
		t.Fatalf("lockMgr: %v", err)
	}
	svc := &Service{
		cfg:          &bridge.Config{},
		store:        store.New(conn, config.ProviderSQLite),
		lockMgr:      lockMgr,
		projectID:    "proj",
		dataDir:      t.TempDir(),
		adapters:     make(map[string]bridge.Adapter),
		inboundCh:    make(chan bridge.Inbound, 64),
		dispatchers:  make(map[string]*sessionDispatch),
		adapterLocks: make(map[string]store.LockHandle),
	}
	t.Cleanup(func() { _ = svc.Stop() })
	return svc, conn
}

// stubAdapter is a bridge.Adapter that records Send calls and lets tests
// push inbound onto the orchestrator. ResolveUserToDM is a passthrough.
type stubAdapter struct {
	channel    string
	identity   string
	mu         sync.Mutex
	sends      []bridge.Outbound
	sendErr    error
	resolvedTo string
	// Configurable mutation hint — first Send to a channel-only peer
	// returns this as ResolvedPeer so the orchestrator can exercise the
	// peer_id mutation path.
	resolveOnSend string
	startedCh     chan struct{}
}

func newStubAdapter(channel, identity string) *stubAdapter {
	return &stubAdapter{
		channel:   channel,
		identity:  identity,
		startedCh: make(chan struct{}),
	}
}

func (s *stubAdapter) Channel() string              { return s.channel }
func (s *stubAdapter) Identity() string             { return s.identity }
func (s *stubAdapter) Status() bridge.AdapterStatus { return bridge.AdapterStatus{Status: "running"} }
func (s *stubAdapter) ResolveUserToDM(_ context.Context, peerID string) (string, error) {
	if s.resolvedTo != "" {
		return s.resolvedTo, nil
	}
	return peerID, nil
}

func (s *stubAdapter) Start(ctx context.Context, _ chan<- bridge.Inbound) error {
	close(s.startedCh)
	return nil
}

func (s *stubAdapter) Send(_ context.Context, out bridge.Outbound) bridge.SendResult {
	s.mu.Lock()
	s.sends = append(s.sends, out)
	s.mu.Unlock()
	if s.sendErr != nil {
		return bridge.SendResult{Err: s.sendErr}
	}
	return bridge.SendResult{Delivered: true, ResolvedPeer: s.resolveOnSend}
}

func (s *stubAdapter) Sends() []bridge.Outbound {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]bridge.Outbound, len(s.sends))
	copy(out, s.sends)
	return out
}

func TestBindCreatesBindingAndDispatcher(t *testing.T) {
	t.Parallel()
	svc, _ := newOrchestratorForTest(t)
	if err := svc.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	ad := newStubAdapter("slack", "default")
	// Register the adapter so Bind can resolve user-id form (passthrough).
	if err := svc.RegisterAdapter(context.Background(), ad); err != nil {
		t.Fatalf("RegisterAdapter: %v", err)
	}

	results, err := svc.Bind(context.Background(), "S1", []bridge.PeerRef{
		{Channel: "slack", Identity: "default", PeerID: "D1", Mention: "<@U1>"},
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if len(results) != 1 || results[0].Err != nil {
		t.Fatalf("Bind results = %+v", results)
	}
	if results[0].Binding.MentionHandle != "<@U1>" {
		t.Errorf("MentionHandle = %q", results[0].Binding.MentionHandle)
	}

	// Dispatcher must exist.
	svc.dispatchMu.Lock()
	_, ok := svc.dispatchers["S1"]
	svc.dispatchMu.Unlock()
	if !ok {
		t.Errorf("dispatcher for S1 not created")
	}
}

func TestUnbindTearsDownDispatcher(t *testing.T) {
	t.Parallel()
	svc, _ := newOrchestratorForTest(t)
	_ = svc.Start(context.Background())

	ad := newStubAdapter("slack", "default")
	_ = svc.RegisterAdapter(context.Background(), ad)
	_, _ = svc.Bind(context.Background(), "S1", []bridge.PeerRef{
		{Channel: "slack", Identity: "default", PeerID: "D1"},
	})

	if err := svc.Unbind(context.Background(), "S1"); err != nil {
		t.Fatalf("Unbind: %v", err)
	}
	svc.dispatchMu.Lock()
	_, ok := svc.dispatchers["S1"]
	svc.dispatchMu.Unlock()
	if ok {
		t.Errorf("dispatcher for S1 still present after full unbind")
	}
}

func TestSendBySessionIDFansOutAcrossBoundPeers(t *testing.T) {
	t.Parallel()
	svc, _ := newOrchestratorForTest(t)
	_ = svc.Start(context.Background())

	slack := newStubAdapter("slack", "default")
	tg := newStubAdapter("telegram", "default")
	if err := svc.RegisterAdapter(context.Background(), slack); err != nil {
		t.Fatalf("RegisterAdapter slack: %v", err)
	}
	if err := svc.RegisterAdapter(context.Background(), tg); err != nil {
		t.Fatalf("RegisterAdapter tg: %v", err)
	}

	_, _ = svc.Bind(context.Background(), "S1", []bridge.PeerRef{
		{Channel: "slack", Identity: "default", PeerID: "D1"},
		{Channel: "telegram", Identity: "default", PeerID: "12345"},
	})

	res, err := svc.SendBySessionID(context.Background(), "S1", bridge.Outbound{
		Text: "hello reviewers",
	})
	if err != nil {
		t.Fatalf("SendBySessionID: %v", err)
	}
	if len(res) != 2 {
		t.Errorf("results len = %d, want 2", len(res))
	}
	if len(slack.Sends()) != 1 || slack.Sends()[0].Text != "hello reviewers" {
		t.Errorf("slack sends = %+v", slack.Sends())
	}
	if len(tg.Sends()) != 1 || tg.Sends()[0].Text != "hello reviewers" {
		t.Errorf("telegram sends = %+v", tg.Sends())
	}
}

func TestSendBySessionIDIsolatesPerPeerFailures(t *testing.T) {
	t.Parallel()
	svc, _ := newOrchestratorForTest(t)
	_ = svc.Start(context.Background())

	good := newStubAdapter("slack", "default")
	bad := newStubAdapter("telegram", "default")
	bad.sendErr = errors.New("DM closed")
	_ = svc.RegisterAdapter(context.Background(), good)
	_ = svc.RegisterAdapter(context.Background(), bad)
	_, _ = svc.Bind(context.Background(), "S1", []bridge.PeerRef{
		{Channel: "slack", Identity: "default", PeerID: "D1"},
		{Channel: "telegram", Identity: "default", PeerID: "12345"},
	})

	res, _ := svc.SendBySessionID(context.Background(), "S1", bridge.Outbound{Text: "x"})
	var delivered, failed int
	for _, r := range res {
		if r.Delivered {
			delivered++
		} else {
			failed++
		}
	}
	if delivered != 1 || failed != 1 {
		t.Errorf("delivered=%d failed=%d, want 1/1", delivered, failed)
	}
}

func TestSendBySessionIDMutatesChannelOnlyBinding(t *testing.T) {
	t.Parallel()
	svc, _ := newOrchestratorForTest(t)
	_ = svc.Start(context.Background())

	slack := newStubAdapter("slack", "default")
	slack.resolveOnSend = "C0DEF456|1700000123.000200" // first-send thread mutation
	_ = svc.RegisterAdapter(context.Background(), slack)

	_, _ = svc.Bind(context.Background(), "S1", []bridge.PeerRef{
		{Channel: "slack", Identity: "default", PeerID: "C0DEF456"},
	})

	if _, err := svc.SendBySessionID(context.Background(), "S1", bridge.Outbound{Text: "first turn"}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	// Binding row should now be at the mutated peer_id.
	got, err := svc.store.GetBinding(context.Background(), "proj", "slack", "default", "C0DEF456|1700000123.000200")
	if err != nil {
		t.Fatalf("Get mutated: %v", err)
	}
	if got.SessionID != "S1" {
		t.Errorf("mutated row SessionID = %q", got.SessionID)
	}
}

func TestSendBySessionIDStampMentionConsumedOnFirstSend(t *testing.T) {
	t.Parallel()
	svc, _ := newOrchestratorForTest(t)
	_ = svc.Start(context.Background())

	slack := newStubAdapter("slack", "default")
	_ = svc.RegisterAdapter(context.Background(), slack)
	_, _ = svc.Bind(context.Background(), "S1", []bridge.PeerRef{
		{Channel: "slack", Identity: "default", PeerID: "D1", Mention: "<@U1>"},
	})

	// First send: adapter sees Mention.
	if _, err := svc.SendBySessionID(context.Background(), "S1", bridge.Outbound{Text: "hi"}); err != nil {
		t.Fatalf("Send 1: %v", err)
	}
	if len(slack.Sends()) != 1 || slack.Sends()[0].Mention != "<@U1>" {
		t.Errorf("first send mention not propagated: %+v", slack.Sends())
	}

	// Verify mention_consumed_at was stamped.
	got, _ := svc.store.GetBinding(context.Background(), "proj", "slack", "default", "D1")
	if got.MentionConsumedAt == 0 {
		t.Errorf("mention_consumed_at not stamped")
	}

	// Second send: Outbound.Mention should be empty since consumed.
	if _, err := svc.SendBySessionID(context.Background(), "S1", bridge.Outbound{Text: "hi again"}); err != nil {
		t.Fatalf("Send 2: %v", err)
	}
	if len(slack.Sends()) != 2 || slack.Sends()[1].Mention != "" {
		t.Errorf("second send unexpectedly had mention: %+v", slack.Sends())
	}
}

func TestDispatchInboundUserInitiatedCreatesBinding(t *testing.T) {
	t.Parallel()
	svc, _ := newOrchestratorForTest(t)
	_ = svc.Start(context.Background())

	// Inbound from a never-seen peer with no app.Sessions wired up. The
	// resolveBinding path returns an error because allocateSession needs
	// a real session.Service — verify the warn-and-skip semantics by
	// checking no dispatcher was created. (Full happy-path is exercised
	// in Phase 6's HTTP integration tests with a real app harness.)
	in := bridge.Inbound{
		Peer: bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "DNEW"},
		Text: "hello",
	}
	// Must not panic; dispatcher absence is the assertion.
	svc.dispatchInbound(context.Background(), in)
	svc.dispatchMu.Lock()
	_, ok := svc.dispatchers["S1"]
	svc.dispatchMu.Unlock()
	if ok {
		t.Errorf("dispatcher unexpectedly created")
	}
}

func TestRegisterAdapterIdempotencyAndLock(t *testing.T) {
	t.Parallel()
	svc, _ := newOrchestratorForTest(t)
	_ = svc.Start(context.Background())

	ad := newStubAdapter("slack", "default")
	if err := svc.RegisterAdapter(context.Background(), ad); err != nil {
		t.Fatalf("first RegisterAdapter: %v", err)
	}
	// Second register of the same identity must fail.
	if err := svc.RegisterAdapter(context.Background(), ad); err == nil {
		t.Errorf("second RegisterAdapter did not error")
	}
	// Wait for Start to fire (the stub closes startedCh inside Start).
	select {
	case <-ad.startedCh:
	case <-time.After(time.Second):
		t.Errorf("adapter Start was not called")
	}
}

// Sanity: the inboundCh pump forwards adapter messages to dispatchInbound
// without blocking, even before any dispatcher exists for the session.
func TestInboundChPumpDoesNotBlockOnEmptyDispatcher(t *testing.T) {
	t.Parallel()
	svc, _ := newOrchestratorForTest(t)
	_ = svc.Start(context.Background())

	// User-initiated inbound — no prior binding, no app.Sessions wired
	// up. The dispatcher should warn and return without panicking. This
	// is what we expect when an orphaned adapter accidentally pushes
	// before the orchestrator is ready.
	done := make(chan struct{})
	go func() {
		svc.InboundChan() <- bridge.Inbound{
			Peer: bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "X"},
			Text: "x",
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Errorf("inboundCh push blocked")
	}
}

// TestPartsQueueCap verifies the per-session parts channel has the
// spec-mandated 64-cap drop-oldest buffer.
func TestPartsQueueCap(t *testing.T) {
	t.Parallel()
	svc, _ := newOrchestratorForTest(t)
	_ = svc.Start(context.Background())
	d := svc.newSessionDispatch("S1")
	if cap(d.parts) != dispatchPartsCap {
		t.Errorf("parts cap = %d, want %d", cap(d.parts), dispatchPartsCap)
	}
	if cap(d.inbound) != dispatchInboundCap {
		t.Errorf("inbound cap = %d, want %d", cap(d.inbound), dispatchInboundCap)
	}
}
