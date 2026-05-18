package api

import (
	"net/http"

	"github.com/opencode-ai/opencode/internal/permission"
)

// handlePermissionList returns pending permission requests.
// Currently returns an empty array — permission requests are delivered via SSE
// events in real time, but OpenWork also polls this endpoint.
func (s *Server) handlePermissionList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, []struct{}{})
}

// handlePermissionReply handles granting or denying a pending permission request.
func (s *Server) handlePermissionReply(w http.ResponseWriter, r *http.Request) {
	requestID := r.PathValue("requestID")

	var req APIPermissionReply
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	permReq := permission.PermissionRequest{
		ID: requestID,
	}

	if req.Allow {
		s.app.Permissions.Grant(permReq)
	} else {
		s.app.Permissions.Deny(permReq)
	}

	writeJSON(w, http.StatusOK, true)
}
