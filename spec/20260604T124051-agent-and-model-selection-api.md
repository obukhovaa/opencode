# Agent & Model Selection — API Surface for External Clients

**Date**: 2026-06-04
**Status**: Implemented
**Author**: AI-assisted

## Problem

External clients (the openwork bridge, future TUI/dashboard alternatives) need a way to:

1. Discover which primary agents are configured and which one is currently active.
2. Switch the active primary agent without going through the TUI's index-based `SwitchAgent` cycle.
3. Switch the model used by the currently active agent without editing config files.

Today our API exposes `GET /agent` (list) and `GET /config/providers` (list models with `Default` set to the active agent's current model). It does **not** expose:

- A way to tell from the agent list which one is active (clients have to call `GET /config/providers` and reverse-match by model — ugly).
- A way to hide subagents from a list intended for human selection (they can't be activated).
- Endpoints to mutate the active agent or its model.

The openwork bridge has historically worked around this with a hardcoded `MODEL_PRESETS` map (`/opus`, `/codex` shortcuts), a per-peer `userModelOverrides` map, and a `messagingAgent.selectedAgent` override threaded through every prompt. That whole layer fights opencode's actual model-of-the-world — we want to remove it and route everything through the canonical opencode endpoints.

## Goals

1. Make the currently active primary agent observable directly from `GET /agent` via an `Active` field.
2. Allow filtering `GET /agent` to primary agents only via `?mode=agent`.
3. Add `POST /agent/select` (body `{id}`) to switch the active primary agent.
4. Add `POST /agent/model/select` (body `{providerID, modelID}`) to switch the active agent's model.
5. Keep all existing behavior intact — additive change, no breaking modifications.

## Non-Goals

- **Per-session agent or model binding.** Today `app.ActiveAgent()` and the per-agent model are app-global state. Two concurrent chats on the same opencode process share the same active agent. Threading session-scoped agent overrides through `agent.Run` is a separate, larger change with cascading implications for the agent registry, prompt caching, and tool resolution. The bridge surfaces the global nature in its confirmation messages instead.
- **Persistent rollback / undo of switches.** Selection writes through `config.UpdateAgentModel`, which persists to disk. Reverting is just "select the previous values again."
- **Validation of model compatibility with agent.** Agent ↔ model compatibility is a config concern; the API trusts what the caller asks for and surfaces errors from `agent.Update` if any.
- **Provider authentication flow.** Out of scope. The caller is expected to consult `connected` (already exposed via `GET /provider`) and not attempt selection on a disconnected provider.

## Design

### 1. `APIAgent.Active` field

`internal/api/types.go`:

```go
type APIAgent struct {
    ID          string `json:"id"`
    Name        string `json:"name"`
    Description string `json:"description,omitempty"`
    Mode        string `json:"mode"`
    Model       string `json:"model,omitempty"`
    Active      bool   `json:"active"` // new: true iff this agent is currently active
}
```

`handler_agent.go` derives `Active` by comparing the agent's ID against `s.app.ActiveAgentName()`. Subagents always serialize `Active: false` since they cannot be active.

### 2. `?mode=agent` filter on `GET /agent`

Add a query parameter that filters the response to a single mode. Accept `agent` and `subagent` as values; reject anything else with `400`. Omitted = return all (preserves existing behavior).

```go
func (s *Server) handleAgentList(w http.ResponseWriter, r *http.Request) {
    modeFilter := r.URL.Query().Get("mode")
    if modeFilter != "" && modeFilter != string(config.AgentModeAgent) && modeFilter != string(config.AgentModeSubagent) {
        writeError(w, http.StatusBadRequest, "invalid mode filter")
        return
    }
    // ... iterate, filter, attach Active ...
}
```

### 3. `POST /agent/select`

Body: `{"id": "<agent-id>"}`. Maps to `app.SetActiveAgent(config.AgentName(id))` — which already exists at `app.go:118`. Returns the updated `APIAgent` shape so the caller can confirm.

```go
type APIAgentSelectRequest struct {
    ID string `json:"id"`
}

func (s *Server) handleAgentSelect(w http.ResponseWriter, r *http.Request) {
    var req APIAgentSelectRequest
    if err := readJSON(r, &req); err != nil { ... }
    if req.ID == "" { writeError(w, http.StatusBadRequest, "id is required"); return }

    if err := s.app.SetActiveAgent(config.AgentName(req.ID)); err != nil {
        writeError(w, http.StatusNotFound, err.Error())
        return
    }

    // Build the new active agent's APIAgent payload (Active: true).
    active := s.app.ActiveAgent()
    info := s.app.Registry.Get(s.app.ActiveAgentName())
    writeJSON(w, http.StatusOK, toAPIAgent(info, true))
}
```

**Subagent guard.** `app.SetActiveAgent` already only searches `PrimaryAgentKeys`, so subagent IDs return `not found`. No additional check needed.

**Concurrency.** `App.SwitchAgent`/`SetActiveAgent` mutate `ActiveAgentIdx` and `activeAgent` without synchronization today (see `20260517T120000-server-api-and-acp.md:588` — known issue). This spec does not fix that; the new endpoint inherits the same race window as the TUI. Document and defer.

### 4. `POST /agent/model/select`

Body: `{"providerID": "<provider>", "modelID": "<model>"}`. Validates the pair via `models.SupportedModels[modelID]` (existing global) and that the model's provider matches `providerID`. Then calls `app.ActiveAgent().Update(activeName, modelID)` — which already exists at `agent.go:1237` and writes through `config.UpdateAgentModel`.

```go
type APIAgentModelSelectRequest struct {
    ProviderID string `json:"providerID"`
    ModelID    string `json:"modelID"`
}

func (s *Server) handleAgentModelSelect(w http.ResponseWriter, r *http.Request) {
    var req APIAgentModelSelectRequest
    if err := readJSON(r, &req); err != nil { ... }
    if req.ProviderID == "" || req.ModelID == "" {
        writeError(w, http.StatusBadRequest, "providerID and modelID are required")
        return
    }
    model, ok := models.SupportedModels[models.ModelID(req.ModelID)]
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
    updated, err := active.Update(s.app.ActiveAgentName(), models.ModelID(req.ModelID))
    if err != nil {
        writeError(w, http.StatusInternalServerError, err.Error())
        return
    }

    writeJSON(w, http.StatusOK, ConvertModelInfo(updated))
}
```

**Busy guard.** `agent.Update` already refuses to switch model while the agent is processing (`agent.go:1239` returns `cannot change model while processing requests`). The endpoint surfaces that as `409 Conflict`.

**Persistence.** `config.UpdateAgentModel` persists the choice to the config file. This matches how the TUI behaves today.

### 5. No deprecations

Existing endpoints (`GET /agent`, `GET /config/providers`, `GET /provider`) keep their existing behavior. New field `Active` is additive and defaults to its zero value for agents that aren't active. New query param `?mode=` is optional. The two new POST endpoints are net-new routes.

## Route table additions

`internal/api/server.go`:

```go
mux.HandleFunc("POST /agent/select", s.handleAgentSelect)
mux.HandleFunc("POST /agent/model/select", s.handleAgentModelSelect)
```

(`GET /agent` already at L124 — modified in place to support `Active` and `?mode=`.)

## Wire format examples

`GET /agent?mode=agent` →
```json
[
  {"id":"coder","name":"Coder","mode":"agent","model":"claude-sonnet-4-7","active":true},
  {"id":"writer","name":"Writer","mode":"agent","model":"gpt-5.2-codex","active":false}
]
```

`POST /agent/select` body `{"id":"writer"}` → `200` with the updated agent's `APIAgent`.

`POST /agent/model/select` body `{"providerID":"anthropic","modelID":"claude-opus-4-5-20251101"}` → `200` with `APIModelInfo` of the newly bound model.

Errors map to `400` (validation), `404` (unknown), `409` (busy/conflict), `500` (config write failure).

## Implementation plan

- [x] Extend `APIAgent` with `Active bool` field in `internal/api/types.go`.
- [x] Update `handleAgentList` to support `?mode=` filter and set `Active` per row.
- [x] Add `POST /agent/select` handler + route.
- [x] Add `POST /agent/model/select` handler + route.
- [x] Unit tests for both handlers in `internal/api/handler_agent_test.go` (new file).
- [x] Introduce `agent.ErrAgentBusy` sentinel so the handler maps busy→409 via `errors.Is`.

## Caller-side impact (openwork bridge)

For reference only — implemented in a separate change to the openwork repo.

- Reinstate `/agent` chat command: `GET /agent?mode=agent` → list with active marker.
- `/agent <id-prefix>`: `POST /agent/select` → confirm + warn about prompt-cache reset on continued conversations.
- Rewrite `/model` (no arg): `GET /config/providers` + `GET /provider` → group by provider, hide non-connected, mark current.
- `/model <model-id>`: resolve provider via the provider list, `POST /agent/model/select`. Hint at ambiguity if a bare ID matches multiple providers.
- Remove the hardcoded `MODEL_PRESETS` map (`/opus`, `/codex` shortcuts) and the per-peer `userModelOverrides` cache — both became redundant once selection is server-authoritative.

## Risks

| Risk | Mitigation |
|---|---|
| Race in `ActiveAgentIdx`/`activeAgent` mutation under concurrent selects | Pre-existing issue (`20260517T120000-server-api-and-acp.md:588`). Not introduced here. Both endpoints inherit the same constraint as the TUI. |
| Caller selects model whose provider is disconnected | We do not pre-check `connected`. Selection still writes through; the next prompt that uses the agent will error. The bridge filters by `connected` client-side to keep selection coherent. |
| Persisted `config.UpdateAgentModel` clobbers a user's manually edited config | Same behavior as the TUI today. No change. |
| Subagents emitted via `GET /agent` without `?mode=` filter confuse callers | Active flag is always false on subagents; `Mode` field already disambiguates. Callers that want primaries only should pass the filter. |
