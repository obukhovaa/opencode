package api

import (
	"net/http"

	"github.com/opencode-ai/opencode/internal/config"
)

// handleAgentList returns all registered agents.
func (s *Server) handleAgentList(w http.ResponseWriter, r *http.Request) {
	agents := s.app.Registry.List()

	result := make([]APIAgent, 0, len(agents))
	for _, a := range agents {
		mode := string(a.Mode)
		if mode == "" {
			mode = string(config.AgentModeSubagent)
		}

		result = append(result, APIAgent{
			ID:          a.ID,
			Name:        a.Name,
			Description: a.Description,
			Mode:        mode,
			Model:       a.Model,
		})
	}

	writeJSON(w, http.StatusOK, result)
}
