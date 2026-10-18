package service

import (
	"context"
	"testing"

	"github.com/opencode-ai/opencode/internal/bridge/store"
)

// SessionPeers answers the peers bound to a session, and none for a
// session without bindings, so router_send can tell which conversations a
// turn with no inbound peer belongs to.
func TestSessionPeers(t *testing.T) {
	svc, sqlDB := newOrchestratorForTest(t)
	ctx := context.Background()
	if _, err := sqlDB.Exec(`
		INSERT INTO sessions (id, project_id, title, message_count, prompt_tokens, completion_tokens, cost, updated_at, created_at)
		VALUES ('S2', 'proj', 't', 0, 0, 0, 0, strftime('%s','now'), strftime('%s','now'))`); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	for _, b := range []store.Binding{
		{ProjectID: "proj", Channel: "external", IdentityID: "default", PeerID: "app1:d1:c1", SessionID: "S1"},
		{ProjectID: "proj", Channel: "slack", IdentityID: "default", PeerID: "C1", SessionID: "S1", MentionHandle: "<@U1>"},
		{ProjectID: "proj", Channel: "external", IdentityID: "default", PeerID: "app2:d1:c9", SessionID: "S2"},
	} {
		if _, err := svc.store.UpsertBinding(ctx, b); err != nil {
			t.Fatalf("UpsertBinding: %v", err)
		}
	}

	got, err := svc.SessionPeers(ctx, "S1")
	if err != nil {
		t.Fatalf("SessionPeers: %v", err)
	}
	want := map[string]bool{"external:default:app1:d1:c1": true, "slack:default:C1": true}
	if len(got) != len(want) {
		t.Fatalf("SessionPeers(S1) = %v, want %d peers", got, len(want))
	}
	for _, p := range got {
		if !want[p.Channel+":"+p.Identity+":"+p.PeerID] {
			t.Errorf("SessionPeers(S1) returned %v, bound to another session", p)
		}
	}

	if got, err := svc.SessionPeers(ctx, "S3"); err != nil || len(got) != 0 {
		t.Errorf("SessionPeers(unbound session) = (%v, %v), want none", got, err)
	}
}
