package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/bridge/store"
)

// TestHasPermissionResolverModeGating verifies that HasPermissionResolver
// returns true only for modes that actually resolve requests today
// ("allow" / "deny"). Modes "" and "ask" must return false so the cron
// scheduler keeps deferring to the TUI active-session gate — otherwise
// it would call permissions.Request() and block forever waiting for an
// answer the router never provides.
func TestHasPermissionResolverModeGating(t *testing.T) {
	t.Parallel()
	svc, conn := newOrchestratorForTest(t)
	// Seed a binding so the router's isBridgeOwnedSession returns true
	// for session "S1". The test session row is already seeded by
	// newOrchestratorForTest.
	if _, err := conn.Exec(`
		INSERT INTO bridge_sessions (project_id, channel, identity_id, peer_id, session_id, created_at, updated_at)
		VALUES ('proj', 'telegram', 'bot1', 'peer1', 'S1', strftime('%s','now'), strftime('%s','now'))
	`); err != nil {
		t.Fatalf("seed binding: %v", err)
	}
	// Construct the router with a non-empty mode so newPermissionRouter
	// initialises it; we'll flip cfg.PermissionMode per case below to
	// exercise the gate's switch.
	svc.cfg = &bridge.Config{PermissionMode: "allow"}
	svc.permissionRouter = &PermissionRouter{svc: svc}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cases := []struct {
		mode      string
		sessionID string
		want      bool
		reason    string
	}{
		{"allow", "S1", true, "bound session + resolving mode"},
		{"deny", "S1", true, "bound session + resolving mode"},
		{"ask", "S1", false, "ask is reserved; router would hang"},
		{"", "S1", false, "no mode means bridge stays out of permissions"},
		{"unknown-mode", "S1", false, "unknown modes fail closed"},
		{"allow", "not-bound", false, "unbound session is not bridge-owned"},
		{"allow", "", false, "empty session id never resolves"},
	}
	for _, c := range cases {
		t.Run(c.mode+"/"+c.sessionID, func(t *testing.T) {
			svc.cfg.PermissionMode = c.mode
			// Fresh router each subcase so the ownedSessions cache
			// from a previous subcase can't bleed in.
			svc.permissionRouter = &PermissionRouter{svc: svc}
			got := svc.HasPermissionResolver(ctx, c.sessionID)
			if got != c.want {
				t.Errorf("HasPermissionResolver(mode=%q, session=%q) = %v, want %v (%s)",
					c.mode, c.sessionID, got, c.want, c.reason)
			}
		})
	}
}

// TestOwnedSessionsCacheInvalidatedOnBindingChange locks in the fix for
// the "crons on a re-bound session never fire until restart" bug: the
// PermissionRouter caches is-this-session-bridge-owned verdicts, and
// binding mutations must invalidate that cache. Before the fix a session
// that was probed while unbound cached `false` forever — switching the
// chat back to it via /session never revived its cron jobs because the
// scheduler's HasPermissionResolver gate kept reading the stale verdict.
func TestOwnedSessionsCacheInvalidatedOnBindingChange(t *testing.T) {
	t.Parallel()
	svc, conn := newOrchestratorForTest(t)
	// Second session row so bindings can move between S1 and S2.
	if _, err := conn.Exec(`
		INSERT INTO sessions (id, project_id, title, message_count, prompt_tokens, completion_tokens, cost, updated_at, created_at)
		VALUES ('S2', 'proj', 't2', 0, 0, 0, 0, strftime('%s','now'), strftime('%s','now'))
	`); err != nil {
		t.Fatalf("seed session S2: %v", err)
	}
	svc.cfg = &bridge.Config{PermissionMode: "allow"}
	svc.permissionRouter = &PermissionRouter{svc: svc}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	bind := func(sessionID string) {
		t.Helper()
		if _, err := svc.store.UpsertBinding(ctx, store.Binding{
			ProjectID:  "proj",
			Channel:    "telegram",
			IdentityID: "bot1",
			PeerID:     "peer1",
			SessionID:  sessionID,
		}); err != nil {
			t.Fatalf("upsert binding -> %s: %v", sessionID, err)
		}
	}

	// Chat bound to S2; S1 is probed (e.g. by a cron tick) and the
	// unowned verdict lands in the cache.
	bind("S2")
	if svc.HasPermissionResolver(ctx, "S1") {
		t.Fatal("S1 should not be resolver-covered while the chat is bound to S2")
	}
	if !svc.HasPermissionResolver(ctx, "S2") {
		t.Fatal("S2 should be resolver-covered while bound")
	}

	// User runs `/session S1` — the row repoints. Without invalidation
	// both answers below would come from the stale cache.
	bind("S1")
	svc.invalidateSessionScopeCaches()

	if !svc.HasPermissionResolver(ctx, "S1") {
		t.Fatal("S1 must be resolver-covered after re-binding (cron jobs revive)")
	}
	if svc.HasPermissionResolver(ctx, "S2") {
		t.Fatal("S2 must lose resolver coverage after the chat moved to S1")
	}
}

// TestUnbindInvalidatesOwnedSessionsCache exercises a real mutation path
// end-to-end: Service.Unbind must clear the cached `true` verdict, so
// the router stops auto-resolving permissions for a session the chat no
// longer owns. Fails on pre-fix code where the cached verdict outlived
// the binding row.
func TestUnbindInvalidatesOwnedSessionsCache(t *testing.T) {
	t.Parallel()
	svc, _ := newOrchestratorForTest(t)
	svc.cfg = &bridge.Config{PermissionMode: "allow"}
	svc.permissionRouter = &PermissionRouter{svc: svc}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := svc.store.UpsertBinding(ctx, store.Binding{
		ProjectID:  "proj",
		Channel:    "telegram",
		IdentityID: "bot1",
		PeerID:     "peer1",
		SessionID:  "S1",
	}); err != nil {
		t.Fatalf("upsert binding: %v", err)
	}
	if !svc.HasPermissionResolver(ctx, "S1") {
		t.Fatal("S1 should be resolver-covered while bound")
	}

	if err := svc.Unbind(ctx, "S1"); err != nil {
		t.Fatalf("unbind: %v", err)
	}
	if svc.HasPermissionResolver(ctx, "S1") {
		t.Fatal("S1 must lose resolver coverage immediately after Unbind, not at process restart")
	}
}

// TestOwnedSessionsCacheRaceDropsStaleCommit locks in the generation-guard
// fix for the invalidation TOCTOU. If a binding mutation (and its cache
// invalidation) lands *while* isBridgeOwnedSession is mid-computation, the
// verdict read before the mutation must NOT be written back over the Clear() —
// otherwise a session that flips bound during the read window caches the
// pre-bind `false` forever (the defer-forever bug, behind a race). The
// beforeCommitHook drops the rebind+invalidation into the commit window
// deterministically. On pre-fix code the second probe returns the stale
// cached `false`.
func TestOwnedSessionsCacheRaceDropsStaleCommit(t *testing.T) {
	t.Parallel()
	svc, _ := newOrchestratorForTest(t)
	svc.cfg = &bridge.Config{PermissionMode: "allow"}
	r := &PermissionRouter{svc: svc}
	svc.permissionRouter = r

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	bind := func(sessionID string) {
		t.Helper()
		if _, err := svc.store.UpsertBinding(ctx, store.Binding{
			ProjectID:  "proj",
			Channel:    "telegram",
			IdentityID: "bot1",
			PeerID:     "peer1",
			SessionID:  sessionID,
		}); err != nil {
			t.Fatalf("upsert binding -> %s: %v", sessionID, err)
		}
	}

	// One-shot: during the FIRST ownership computation for S1 (while S1 still
	// reads as unbound), the chat binds to S1 and invalidates the cache —
	// exactly the mutation-lands-mid-read window the generation guard closes.
	var once sync.Once
	r.beforeCommitHook = func() {
		once.Do(func() {
			bind("S1")
			svc.invalidateSessionScopeCaches()
		})
	}

	// First probe reads S1 as unbound, then the hook binds+invalidates; the
	// stale `false` must be dropped rather than cached.
	if svc.HasPermissionResolver(ctx, "S1") {
		t.Fatal("first probe reads pre-bind state; expected not-owned")
	}
	// Second probe must recompute against the now-bound row and see ownership.
	if !svc.HasPermissionResolver(ctx, "S1") {
		t.Fatal("stale verdict survived invalidation: S1 should be owned after the mid-read rebind")
	}
}

func TestHasPermissionResolverNilGuards(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Nil receiver — defensive; the cron scheduler may invoke before
	// the bridge has finished wiring itself.
	var nilSvc *Service
	if nilSvc.HasPermissionResolver(ctx, "S1") {
		t.Error("nil service should return false")
	}

	// Constructed service with no permissionRouter and no cfg → false.
	svc := &Service{}
	if svc.HasPermissionResolver(ctx, "S1") {
		t.Error("no permissionRouter + no cfg should return false")
	}

	// Has cfg but no permissionRouter → false.
	svc2 := &Service{cfg: &bridge.Config{PermissionMode: "allow"}}
	if svc2.HasPermissionResolver(ctx, "S1") {
		t.Error("missing permissionRouter should return false")
	}
}
