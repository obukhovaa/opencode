package api

import (
	"errors"
	"net/http"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/llm/models"
)

// handleAgentList returns all registered agents.
//
// Optional `?mode=agent` or `?mode=subagent` filters the response to a
// single mode. Any other value returns 400. Subagents are common in
// chat UIs that surface only user-selectable primary agents.
//
// Each returned APIAgent carries an `active` flag set to true iff the
// agent's ID matches App.ActiveAgentName() — i.e. it is the primary
// agent that handles the next prompt. Subagents always serialize as
// `active: false`.
func (s *Server) handleAgentList(w http.ResponseWriter, r *http.Request) {
	modeFilter := r.URL.Query().Get("mode")
	if modeFilter != "" &&
		modeFilter != string(config.AgentModeAgent) &&
		modeFilter != string(config.AgentModeSubagent) {
		writeError(w, http.StatusBadRequest, "invalid mode filter: must be 'agent' or 'subagent'")
		return
	}

	agents := s.app.Registry.List()
	activeID := string(s.app.ActiveAgentName())

	result := make([]APIAgent, 0, len(agents))
	for _, a := range agents {
		mode := string(a.Mode)
		if mode == "" {
			mode = string(config.AgentModeSubagent)
		}

		if modeFilter != "" && mode != modeFilter {
			continue
		}

		result = append(result, APIAgent{
			ID:          a.ID,
			Name:        a.Name,
			Description: a.Description,
			Mode:        mode,
			Model:       a.Model,
			Active:      mode == string(config.AgentModeAgent) && a.ID == activeID,
		})
	}

	writeJSON(w, http.StatusOK, result)
}

// handleAgentSelect switches the currently active primary agent.
//
// Body: {"id": "<agent-id>"}. Returns 200 with the updated APIAgent on
// success. Returns 404 if the ID is not a primary agent (subagents can
// not be activated — `app.SetActiveAgent` only searches PrimaryAgentKeys).
//
// State race note: App.ActiveAgentIdx / activeAgent are mutated without
// synchronization (pre-existing limitation; see
// spec/20260517T120000-server-api-and-acp.md "App Struct Thread Safety").
// Concurrent selects from multiple callers can interleave. Callers
// expected to serialize at the application layer if this matters.
func (s *Server) handleAgentSelect(w http.ResponseWriter, r *http.Request) {
	var req APIAgentSelectRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.ID == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}

	if err := s.app.SetActiveAgent(config.AgentName(req.ID)); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	activeID := string(s.app.ActiveAgentName())
	for _, a := range s.app.Registry.List() {
		if a.ID == activeID {
			mode := string(a.Mode)
			if mode == "" {
				mode = string(config.AgentModeSubagent)
			}
			writeJSON(w, http.StatusOK, APIAgent{
				ID:          a.ID,
				Name:        a.Name,
				Description: a.Description,
				Mode:        mode,
				Model:       a.Model,
				Active:      true,
			})
			return
		}
	}
	// Should be unreachable: SetActiveAgent succeeded, so the agent is in
	// the registry. Defensive 500 in case the registry has been mutated
	// between SetActiveAgent and the read above.
	writeError(w, http.StatusInternalServerError, "active agent not found in registry after select")
}

// handleAgentModelSelect switches the model used by the currently active
// primary agent.
//
// Body: {"providerID": "<provider>", "modelID": "<model>"}.
//   - 400 if either field is empty.
//   - 400 if the model's recorded provider does not match providerID
//     (mismatched pair — typically a caller bug).
//   - 404 if the modelID is not in models.SupportedModels.
//   - 409 if the agent is currently processing a request (agent.Update
//     refuses to swap models mid-run).
//   - 500 on persistence errors propagated from config.UpdateAgentModel.
//
// Side effect: the new model is persisted to the user's config file via
// config.UpdateAgentModel, matching TUI behavior.
func (s *Server) handleAgentModelSelect(w http.ResponseWriter, r *http.Request) {
	var req APIAgentModelSelectRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.ProviderID == "" || req.ModelID == "" {
		writeError(w, http.StatusBadRequest, "providerID and modelID are required")
		return
	}

	modelID := models.ModelID(req.ModelID)
	model, ok := models.SupportedModels[modelID]
	if !ok {
		writeError(w, http.StatusNotFound, "unknown model")
		return
	}
	if string(model.Provider) != req.ProviderID {
		writeError(w, http.StatusBadRequest, "providerID does not match model's provider")
		return
	}

	active := s.app.ActiveAgent()
	if active == nil {
		writeError(w, http.StatusConflict, "no active agent")
		return
	}

	updated, err := active.Update(s.app.ActiveAgentName(), modelID)
	if err != nil {
		// agent.Update returns ErrAgentBusy when called mid-request; surface
		// that as 409 so callers can retry rather than treating it as a hard
		// failure.
		if errors.Is(err, agent.ErrAgentBusy) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, ConvertModelInfo(updated))
}
