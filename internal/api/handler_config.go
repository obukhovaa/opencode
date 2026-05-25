package api

import (
	"net/http"
	"sort"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/models"
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

// handleProvider returns providers in the dax opencode /provider format.
// OpenWork proxies /opencode/provider to this endpoint and expects
// {all: [...], default: {providerID: modelID}, connected: [...]}.
func (s *Server) handleProvider(w http.ResponseWriter, r *http.Request) {
	providers := ConvertProviders()

	// Build the default model map: {providerID: modelID}
	defaultModel := map[string]string{}
	agent := s.app.ActiveAgent()
	if agent != nil {
		m := agent.Model()
		defaultModel[string(m.Provider)] = string(m.ID)
	}

	// Connected = providers that have API keys configured.
	cfg := config.Get()
	connected := make([]string, 0)
	for providerID, providerCfg := range cfg.Providers {
		if !providerCfg.Disabled && providerCfg.APIKey != "" {
			connected = append(connected, string(providerID))
		}
	}
	// Also include providers that have models registered (env-based providers).
	seen := make(map[string]bool)
	for _, id := range connected {
		seen[id] = true
	}
	for _, m := range models.SupportedModels {
		pid := string(m.Provider)
		if !seen[pid] {
			if p, ok := cfg.Providers[m.Provider]; ok && !p.Disabled {
				connected = append(connected, pid)
				seen[pid] = true
			}
		}
	}

	sort.Strings(connected)

	writeJSON(w, http.StatusOK, map[string]any{
		"all":       providers,
		"default":   defaultModel,
		"connected": connected,
	})
}
