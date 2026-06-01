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

// applyPermissionAction maps a dax-style response verb to our permission service.
// "once" and "always" both map to Grant — we don't track ToolName/Path/SessionID
// at the reply endpoint, so a persistent rule would never match anything and
// would leak entries into sessionPermissions on long-running servers.
// Unknown verbs return false; the handler should reject the request.
func applyPermissionAction(svc permission.Service, id, verb string) bool {
	pr := permission.PermissionRequest{ID: id}
	switch verb {
	case "once", "always":
		svc.Grant(pr)
		return true
	case "reject":
		svc.Deny(pr)
		return true
	default:
		return false
	}
}

// handlePermissionReply handles granting or denying a pending permission request.
// Accepts both the legacy OpenWork shape ({"allow": bool}) and the dax SDK v2
// shape ({"reply": "once"|"always"|"reject"}). When both are sent, Reply wins.
//
// Wire contract note: an empty body now returns 400 instead of being treated
// as `{"allow": false}` (the old zero-value default). All real callers send an
// explicit field, so this is observably backward-compatible.
func (s *Server) handlePermissionReply(w http.ResponseWriter, r *http.Request) {
	requestID := r.PathValue("requestID")

	var req APIPermissionReply
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if req.Reply != "" {
		if !applyPermissionAction(s.app.Permissions, requestID, req.Reply) {
			writeError(w, http.StatusBadRequest, "invalid reply: must be one of once, always, reject")
			return
		}
		writeJSON(w, http.StatusOK, true)
		return
	}

	if req.Allow == nil {
		writeError(w, http.StatusBadRequest, "either 'allow' or 'reply' is required")
		return
	}

	permReq := permission.PermissionRequest{ID: requestID}
	if *req.Allow {
		s.app.Permissions.Grant(permReq)
	} else {
		s.app.Permissions.Deny(permReq)
	}
	writeJSON(w, http.StatusOK, true)
}

// handlePermissionRespond is the session-scoped permission reply endpoint
// (`POST /session/{sessionID}/permissions/{permissionID}` — dax SDK
// `permission.respond`). The sessionID path segment is accepted for SDK
// compatibility but not used — our permission service identifies requests
// by ID alone (`permission.PermissionRequest.ID` is a globally unique UUID).
func (s *Server) handlePermissionRespond(w http.ResponseWriter, r *http.Request) {
	permissionID := r.PathValue("permissionID")

	var req APIPermissionRespond
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if !applyPermissionAction(s.app.Permissions, permissionID, req.Response) {
		writeError(w, http.StatusBadRequest, "invalid response: must be one of once, always, reject")
		return
	}
	writeJSON(w, http.StatusOK, true)
}
