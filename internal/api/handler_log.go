package api

import (
	"net/http"

	"github.com/opencode-ai/opencode/internal/logging"
)

// APILogRequest is the request body for POST /log — dax SDK forwards client-side
// log lines to the server. We treat it as a thin passthrough to our logging
// package. `extra` keys are flattened into structured-log key/value pairs.
//
// Note: extra is not sanitized. Anything serializable is written to the log.
// Acceptable for same-trust deployments since the API is already auth-gated.
type APILogRequest struct {
	Service string         `json:"service"`
	Level   string         `json:"level"`
	Message string         `json:"message"`
	Extra   map[string]any `json:"extra,omitempty"`
}

// handleLogWrite ingests a client log line.
func (s *Server) handleLogWrite(w http.ResponseWriter, r *http.Request) {
	var req APILogRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Message == "" {
		writeError(w, http.StatusBadRequest, "message is required")
		return
	}

	args := make([]any, 0, 2+2*len(req.Extra))
	if req.Service != "" {
		args = append(args, "service", req.Service)
	}
	for k, v := range req.Extra {
		args = append(args, k, v)
	}

	switch req.Level {
	case "debug":
		logging.Debug(req.Message, args...)
	case "warn":
		logging.Warn(req.Message, args...)
	case "error":
		logging.Error(req.Message, args...)
	default: // includes "info" and unknown — default to info
		logging.Info(req.Message, args...)
	}

	writeJSON(w, http.StatusOK, true)
}
