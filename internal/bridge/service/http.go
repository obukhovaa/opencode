package service

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/opencode-ai/opencode/internal/logging"
)

// RegisterRoutes wires the bridge's HTTP surface onto an existing mux.
// Routes live under /router/*; bare paths (/send, /identities/*,
// /config/groups) are NOT registered per the chat-bridge-http-api spec
// requirement "Bare paths return 404 — no aliases".
func (s *Service) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /router/send", s.handleSend)
	mux.HandleFunc("POST /router/bind", s.handleBind)
	mux.HandleFunc("POST /router/unbind", s.handleUnbind)
	mux.HandleFunc("GET /router/health", s.handleHealth)
	mux.HandleFunc("GET /router/config/groups", s.handleGroupsGet)
	mux.HandleFunc("POST /router/config/groups", s.handleGroupsSet)
	mux.HandleFunc("GET /router/identities/telegram", s.handleIdentitiesList("telegram"))
	mux.HandleFunc("POST /router/identities/telegram", s.handleIdentityUpsert("telegram"))
	mux.HandleFunc("DELETE /router/identities/telegram/{id}", s.handleIdentityDelete("telegram"))
	mux.HandleFunc("GET /router/identities/slack", s.handleIdentitiesList("slack"))
	mux.HandleFunc("POST /router/identities/slack", s.handleIdentityUpsert("slack"))
	mux.HandleFunc("DELETE /router/identities/slack/{id}", s.handleIdentityDelete("slack"))
	mux.HandleFunc("GET /router/identities/mattermost", s.handleIdentitiesList("mattermost"))
	mux.HandleFunc("POST /router/identities/mattermost", s.handleIdentityUpsert("mattermost"))
	mux.HandleFunc("DELETE /router/identities/mattermost/{id}", s.handleIdentityDelete("mattermost"))
}

// writeJSON is the bridge's local response helper. We don't import
// internal/api's writeJSON because the api package shouldn't be a
// dependency of the bridge (avoiding cycles).
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeAPIError writes a standard error response envelope: { error,
// message }. status 4xx / 5xx semantics. Match the api/errors.go shape
// so external consumers see uniform error JSON regardless of which
// subsystem returned it.
func writeAPIError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{
		"error":   http.StatusText(status),
		"message": message,
	})
}

// readJSON decodes one JSON request body into target. Used by every
// /router/* handler. 1 MiB cap on body size — chat-bridge requests are
// small (tokens, peerIDs, short text). File uploads use the agent tool's
// in-process path, not HTTP.
func readJSON(r *http.Request, target any) error {
	if r.Body == nil {
		return errors.New("request body is empty")
	}
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return err
	}
	if len(body) == 0 {
		return errors.New("request body is empty")
	}
	return json.Unmarshal(body, target)
}

// logHandlerError writes the response and logs a server-side trace. Used
// by handlers to bubble unexpected errors without leaking internals to
// the client.
func logHandlerError(w http.ResponseWriter, route string, err error) {
	logging.Warn("bridge http: "+route, "err", err)
	writeAPIError(w, http.StatusInternalServerError, "internal error")
}
