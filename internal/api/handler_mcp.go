package api

import (
	"net/http"

	"github.com/opencode-ai/opencode/internal/config"
)

// handleMCPList returns the status of all configured MCP servers.
// The response is a map of server name to status object, matching the
// dax opencode SDK schema: {"serverName": {"status": "connected"|"disabled"|"failed"}}.
func (s *Server) handleMCPList(w http.ResponseWriter, r *http.Request) {
	cfg := config.Get()

	// Build status map from config + loaded server state.
	loaded := s.app.MCPRegistry.LoadedServers()

	result := make(map[string]map[string]string, len(cfg.MCPServers))
	for name, srv := range cfg.MCPServers {
		if srv.Disabled {
			result[name] = map[string]string{"status": "disabled"}
			continue
		}
		if loaded[name] {
			result[name] = map[string]string{"status": "connected"}
		} else {
			result[name] = map[string]string{"status": "failed"}
		}
	}

	writeJSON(w, http.StatusOK, result)
}
