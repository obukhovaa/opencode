package service

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/bridge"
)

// TestRouterInbound_AcceptsValidBody verifies that a well-formed bridge.Inbound
// JSON POST routes through the orchestrator's shared inboundCh — the same
// path adapter pumps use internally. Phase A.4 scenario "valid POST + auth →
// routes through dispatcher". We assert the value lands on inboundCh; we do
// NOT start the orchestrator's runInboundLoop (that would couple this test
// to dispatch internals).
func TestRouterInbound_AcceptsValidBody(t *testing.T) {
	svc, _ := newOrchestratorForTest(t)
	mux := http.NewServeMux()
	svc.RegisterRoutes(mux)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	body, _ := json.Marshal(bridge.Inbound{
		Peer: bridge.PeerRef{
			Channel:  "slack",
			Identity: "default",
			PeerID:   "C0123FAKEXX|1700000001.000001",
		},
		Text:     "hello from the orchestrator",
		AuthorID: "U0123FAKEUU",
	})

	resp, err := http.Post(server.URL+"/router/inbound", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status %d, want 202", resp.StatusCode)
	}

	select {
	case got := <-svc.inboundCh:
		if got.Text != "hello from the orchestrator" {
			t.Errorf("text=%q, want %q", got.Text, "hello from the orchestrator")
		}
		if got.Peer.Channel != "slack" || got.Peer.Identity != "default" {
			t.Errorf("peer=%+v, want slack/default", got.Peer)
		}
		if got.ReceivedAt == 0 {
			t.Errorf("ReceivedAt should be filled when caller omits it")
		}
	case <-time.After(time.Second):
		t.Fatalf("inbound did not land on inboundCh")
	}
}

// TestRouterInbound_RejectsMissingPeer verifies that an Inbound without the
// required peer triple is rejected with 400. Phase A.4 scenario "malformed
// body → 400".
func TestRouterInbound_RejectsMissingPeer(t *testing.T) {
	svc, _ := newOrchestratorForTest(t)
	mux := http.NewServeMux()
	svc.RegisterRoutes(mux)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	body, _ := json.Marshal(bridge.Inbound{
		Text: "no peer",
	})
	resp, err := http.Post(server.URL+"/router/inbound", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", resp.StatusCode)
	}
}

// TestRouterInbound_RejectsEmptyBody covers the no-text/no-attachments case.
func TestRouterInbound_RejectsEmptyBody(t *testing.T) {
	svc, _ := newOrchestratorForTest(t)
	mux := http.NewServeMux()
	svc.RegisterRoutes(mux)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	body, _ := json.Marshal(bridge.Inbound{
		Peer: bridge.PeerRef{
			Channel:  "slack",
			Identity: "default",
			PeerID:   "C0123FAKEXX",
		},
	})
	resp, err := http.Post(server.URL+"/router/inbound", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", resp.StatusCode)
	}
}

// TestRouterInbound_MalformedJSON covers the parse-failure branch.
func TestRouterInbound_MalformedJSON(t *testing.T) {
	svc, _ := newOrchestratorForTest(t)
	mux := http.NewServeMux()
	svc.RegisterRoutes(mux)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	resp, err := http.Post(server.URL+"/router/inbound", "application/json", strings.NewReader("{not-json"))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", resp.StatusCode)
	}
}

// TestRouterInbound_BackpressureReturns429 verifies that when the shared
// inboundCh is full the handler responds with 429. Phase A.4 scenario
// "backpressure → 429".
func TestRouterInbound_BackpressureReturns429(t *testing.T) {
	svc, _ := newOrchestratorForTest(t)
	// Replace the channel with a length-1 capacity so we can fill it
	// without racing the test against a real dispatcher.
	svc.inboundCh = make(chan bridge.Inbound, 1)

	mux := http.NewServeMux()
	svc.RegisterRoutes(mux)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	// Fill the channel.
	svc.inboundCh <- bridge.Inbound{Peer: bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "C1"}, Text: "filler"}

	body, _ := json.Marshal(bridge.Inbound{
		Peer: bridge.PeerRef{
			Channel:  "slack",
			Identity: "default",
			PeerID:   "C0123FAKEXX",
		},
		Text: "second",
	})
	resp, err := http.Post(server.URL+"/router/inbound", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status %d, want 429", resp.StatusCode)
	}
}

// TestRouterInbound_AuthIsHandledByAPIMiddleware documents that the
// /router/inbound handler does NOT enforce auth on its own — that's the
// API server's authMiddleware (HTTP Basic via OPENCODE_SERVER_PASSWORD),
// which wraps the entire mux including all /router/* routes. The bridge's
// httptest setup mounts ONLY the bridge mux (no middleware), so a
// password-protected deployment is exercised by the api package's own
// middleware tests. This test exists to lock the contract.
func TestRouterInbound_AuthIsHandledByAPIMiddleware(t *testing.T) {
	// Sanity: a clean mounted handler (no middleware) accepts the POST.
	// This intentionally documents that the bridge's RegisterRoutes does
	// NOT layer its own auth — auth lives in internal/api/middleware.go.
	svc, _ := newOrchestratorForTest(t)
	mux := http.NewServeMux()
	svc.RegisterRoutes(mux)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	body, _ := json.Marshal(bridge.Inbound{
		Peer: bridge.PeerRef{Channel: "slack", Identity: "default", PeerID: "C0123FAKEXX"},
		Text: "hi",
	})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL+"/router/inbound", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// Deliberately omit basic-auth headers — bridge mux alone admits.
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status %d, want 202 (bridge mux mounts no auth on its own)", resp.StatusCode)
	}

	// Drain the inbound so the cleanup goroutine doesn't see a wedged
	// channel.
	select {
	case <-svc.inboundCh:
	default:
	}
}
