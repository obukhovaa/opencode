package api

import (
	"net/http"

	"github.com/opencode-ai/opencode/internal/version"
)

type healthResponse struct {
	Healthy bool   `json:"healthy"`
	Version string `json:"version"`
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{
		Healthy: true,
		Version: version.Version,
	})
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
