package api

import (
	"context"
	"net/http"

	"github.com/opencode-ai/opencode/internal/version"
)

type healthResponse struct {
	Healthy bool   `json:"healthy"`
	Version string `json:"version"`
	// Bridge, when present, carries the chat-bridge orchestrator's
	// per-identity status (status/lastError/lastInboundAt/lastFailureAt/
	// boundSessions per identity). Absent when the bridge is disabled.
	// Shape is dictated by the bridge-http-api spec's "Extended /health
	// reports per-identity bridge status" requirement.
	Bridge any `json:"bridge,omitempty"`
}

// HealthReporter is the contract the bridge service satisfies to embed
// its health snapshot in /global/health. Decoupled via interface so the
// API package doesn't import internal/bridge/service.
type HealthReporter interface {
	HealthSnapshot(r *http.Request) any
}

// BannerProvider is an optional interface a HealthReporter MAY satisfy
// so the API server's startup banner can include a one-line summary of
// the bridge's per-adapter state. Implementing it is opt-in — the
// banner falls back to nothing if the bridge doesn't satisfy it. Used
// only at boot, so the implementation is allowed to do best-effort
// store lookups for binding counts.
type BannerProvider interface {
	BridgeBanner(ctx context.Context) (status string, lines []string)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	resp := healthResponse{
		Healthy: true,
		Version: version.Version,
	}
	if s.healthReporter != nil {
		resp.Bridge = s.healthReporter.HealthSnapshot(r)
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleInstanceDispose is a no-op stub for the dax opencode /instance/dispose endpoint.
// OpenWork calls this during workspace activation to hot-reload the engine.
// Our fork is single-project, so there's nothing to dispose. If we add
// multi-workspace support, this should tear down and reinitialize the project
// context (LSP clients, MCP servers, config) for the new directory.
// TODO: implement real dispose/reload when multi-workspace is supported.
func (s *Server) handleInstanceDispose(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
