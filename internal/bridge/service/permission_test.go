package service

import (
	"context"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/bridge"
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
		c := c
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
