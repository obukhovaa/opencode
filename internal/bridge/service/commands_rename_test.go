package service

import (
	"context"
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/bridge/store"
	opencodedb "github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/session"
)

// TestCmdRename exercises the full bridge rename path: a bound peer sends
// /rename, and the peer's session title is updated (and marked user-titled).
func TestCmdRename(t *testing.T) {
	svc, conn := newOrchestratorForTest(t)
	svc.app = &app.App{Sessions: session.NewService(opencodedb.New(conn), "proj")}

	ctx := context.Background()
	peer := bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "D1"}
	if _, err := svc.store.UpsertBinding(ctx, store.Binding{
		ProjectID: "proj", Channel: "slack", IdentityID: "default", PeerID: "D1", SessionID: "S1",
	}); err != nil {
		t.Fatalf("UpsertBinding: %v", err)
	}

	reply := svc.cmdRename(ctx, bridge.Inbound{Peer: peer, CommandArgs: "  Release triage  "})
	if reply == nil || !strings.Contains(reply.Text, "Release triage") {
		t.Fatalf("rename reply = %+v, want confirmation containing 'Release triage'", reply)
	}

	got, err := svc.app.Sessions.Get(ctx, "S1")
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if got.Title != "Release triage" || !got.UserSetTitle {
		t.Errorf("session = {%q, userSet=%v}, want {Release triage, true}", got.Title, got.UserSetTitle)
	}
}

// TestCmdRenameEmptyArg: no argument replies with usage and touches nothing
// (returns before resolving the binding, so a bare Service is enough).
func TestCmdRenameEmptyArg(t *testing.T) {
	s := &Service{}
	reply := s.cmdRename(context.Background(), bridge.Inbound{CommandArgs: "   "})
	if reply == nil || !strings.Contains(strings.ToLower(reply.Text), "usage") {
		t.Fatalf("empty-arg reply = %+v, want a usage message", reply)
	}
}

func TestCmdRenameRegistered(t *testing.T) {
	s := &Service{}
	if s.ChatCommands()["rename"] == nil {
		t.Fatal("/rename is not registered in ChatCommands")
	}
}
