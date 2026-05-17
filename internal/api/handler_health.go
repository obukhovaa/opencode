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
