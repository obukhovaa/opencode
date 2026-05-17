package api

import (
	"net/http"

	"github.com/opencode-ai/opencode/internal/config"
)

// handleConfigGet returns the current configuration.
func (s *Server) handleConfigGet(w http.ResponseWriter, r *http.Request) {
	cfg := config.Get()

	var modelID string
	agent := s.app.ActiveAgent()
	if agent != nil {
		modelID = string(agent.Model().ID)
	}

	apiConfig := APIConfig{
		Model: modelID,
	}

	_ = cfg // cfg is available for future expansion
	writeJSON(w, http.StatusOK, apiConfig)
}

// handleConfigProviders returns all available providers with their models.
func (s *Server) handleConfigProviders(w http.ResponseWriter, r *http.Request) {
	providers := ConvertProviders()

	var defaultModel *APIModelInfo
	agent := s.app.ActiveAgent()
	if agent != nil {
		m := ConvertModelInfo(agent.Model())
		defaultModel = &m
	}

	writeJSON(w, http.StatusOK, APIProvidersResponse{
		Providers: providers,
		Default:   defaultModel,
	})
}
