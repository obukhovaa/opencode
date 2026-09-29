package service

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/opencode-ai/opencode/internal/app"
	"github.com/opencode-ai/opencode/internal/bridge"
	"github.com/opencode-ai/opencode/internal/bridge/store"
	"github.com/opencode-ai/opencode/internal/permission"
)

func TestRouterInbound_PoolModeOwnership(t *testing.T) {
	tests := []struct {
		name       string
		poolMode   bool
		peerID     string
		markS1     bool
		wantStatus int
	}{
		{name: "pool mode, no binding", poolMode: true, peerID: "D2", wantStatus: http.StatusConflict},
		{name: "pool mode, binding without marker (container restarted)", poolMode: true, peerID: "D1", wantStatus: http.StatusConflict},
		{name: "pool mode, owned interactive session", poolMode: true, peerID: "D1", markS1: true, wantStatus: http.StatusAccepted},
		{name: "non-pool, no binding", poolMode: false, peerID: "D2", wantStatus: http.StatusAccepted},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, _ := newOrchestratorForTest(t)
			svc.poolMode = tt.poolMode
			svc.app = &app.App{Permissions: permission.NewPermissionService()}
			if _, err := svc.store.UpsertBinding(context.Background(), store.Binding{
				ProjectID: "proj", Channel: "slack", IdentityID: "default",
				PeerID: "D1", SessionID: "S1",
			}); err != nil {
				t.Fatalf("UpsertBinding: %v", err)
			}
			if tt.markS1 {
				svc.app.Permissions.MarkInteractiveSession("S1")
			}

			mux := http.NewServeMux()
			svc.RegisterRoutes(mux)
			server := httptest.NewServer(mux)
			defer server.Close()
			body, _ := json.Marshal(bridge.Inbound{
				Peer: bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: tt.peerID},
				Text: "please reword bullet 1",
			})
			resp, err := http.Post(server.URL+"/router/inbound", "application/json", bytes.NewReader(body))
			if err != nil {
				t.Fatalf("POST: %v", err)
			}
			defer resp.Body.Close()
			var out map[string]any
			_ = json.NewDecoder(resp.Body).Decode(&out)

			if resp.StatusCode != tt.wantStatus {
				t.Fatalf("status %d, want %d (body %v)", resp.StatusCode, tt.wantStatus, out)
			}
			wantEnqueued := tt.wantStatus == http.StatusAccepted
			if !wantEnqueued && out["sessionNotOwned"] != true {
				t.Errorf("sessionNotOwned = %v, want true", out["sessionNotOwned"])
			}
			select {
			case <-svc.inboundCh:
				if !wantEnqueued {
					t.Error("refused inbound was enqueued")
				}
			default:
				if wantEnqueued {
					t.Error("accepted inbound was not enqueued")
				}
			}
		})
	}
}

// An inbound that reaches dispatch for a session no live step owns (the step
// ended after the HTTP check) must never start the default agent.
func TestDispatchInbound_PoolModeUnownedSessionNeverRunsAgent(t *testing.T) {
	t.Parallel()
	ag := &busyRetryStubAgent{}
	svc, _ := newDispatchTestSvc(t, ag)
	svc.app.Permissions = permission.NewPermissionService()
	svc.poolMode = true

	svc.dispatchInbound(context.Background(), testInbound("please reword bullet 1"))

	if n := ag.runCallCount(); n != 0 {
		t.Errorf("ActiveAgent().Run called %d times, want 0", n)
	}
	svc.dispatchMu.Lock()
	defer svc.dispatchMu.Unlock()
	if n := len(svc.dispatchers); n != 0 {
		t.Errorf("%d per-session dispatchers created, want 0", n)
	}
}
