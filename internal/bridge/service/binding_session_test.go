package service

import (
	"context"
	"testing"

	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/bridge/store"
)

// BindingSessionID answers the owning session of a bound peer, and false
// for an unbound peer, so router_send can refuse a send it cannot place.
func TestBindingSessionID(t *testing.T) {
	svc, _ := newOrchestratorForTest(t)
	ctx := context.Background()
	if _, err := svc.store.UpsertBinding(ctx, store.Binding{
		ProjectID: "proj", Channel: "external", IdentityID: "default", PeerID: "app1:d1:c1", SessionID: "S1",
	}); err != nil {
		t.Fatalf("UpsertBinding: %v", err)
	}

	got, ok := svc.BindingSessionID(ctx, bridge.PeerRef{Channel: "external", Identity: "default", PeerID: "app1:d1:c1"})
	if !ok || got != "S1" {
		t.Errorf("bound peer = (%q, %v), want (S1, true)", got, ok)
	}
	if got, ok := svc.BindingSessionID(ctx, bridge.PeerRef{Channel: "external", Identity: "default", PeerID: "app2:d1:c9"}); ok {
		t.Errorf("unbound peer = (%q, true), want false", got)
	}
}
