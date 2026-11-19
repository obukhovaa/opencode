package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	agentpkg "github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/session"
)

// shouldAutoApprove reports whether a permission ruleset asks for blanket
// auto-approval. Our permission service is binary per session, so we only
// honor the wildcard-allow shape (`{permission:"*", pattern:"*", action:"allow"}`)
// that the openwork router emits. Any other rule shape is silently ignored —
// callers can still resolve individual permission requests via the
// /session/{id}/permissions/{id} or /permission/{id}/reply endpoints.
func shouldAutoApprove(rules []APIPermissionRule) bool {
	for _, rule := range rules {
		if rule.Permission == "*" && rule.Pattern == "*" && rule.Action == "allow" {
			return true
		}
	}
	return false
}

// handleSessionList preserves the SDK's array body and exposes continuation headers.
func (s *Server) handleSessionList(w http.ResponseWriter, r *http.Request) {
	options, err := sessionPageOptions(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	page, err := s.app.Sessions.Page(r.Context(), options)
	if err != nil {
		writeSessionPageError(w, err)
		return
	}
	sessionPageHeaders(w, r, page.NextCursor)
	writeJSON(w, http.StatusOK, ConvertSessionsWithDir(page.Sessions, resolveDirectory(r)))
}

// handleSessionCreate creates a new session.
func (s *Server) handleSessionCreate(w http.ResponseWriter, r *http.Request) {
	var req APISessionCreateRequest
	if r.Body != nil && r.ContentLength != 0 {
		if err := readJSON(r, &req); err != nil {
			// Tolerate genuinely empty bodies (chunked with no data),
			// but reject malformed JSON even on chunked requests.
			if r.ContentLength < 0 && isEmptyBodyError(err) {
				req = APISessionCreateRequest{}
			} else {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
		}
	}

	session, err := s.app.Sessions.Create(r.Context(), req.Title)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create session")
		return
	}
	if shouldAutoApprove(req.Permission) {
		s.app.Permissions.AutoApproveSession(session.ID)
	}
	writeJSON(w, http.StatusCreated, ConvertSessionWithDir(session, resolveDirectory(r)))
}

// handleSessionGet returns a single session by ID.
func (s *Server) handleSessionGet(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("sessionID")
	session, err := s.app.Sessions.Get(r.Context(), sessionID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to get session")
		return
	}
	writeJSON(w, http.StatusOK, ConvertSessionWithDir(session, resolveDirectory(r)))
}

// handleSessionDelete deletes a session by ID.
func (s *Server) handleSessionDelete(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("sessionID")
	err := s.app.Sessions.Delete(r.Context(), sessionID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to delete session")
		return
	}
	if s.app.Todos != nil {
		s.app.Todos.Delete(sessionID)
	}
	writeJSON(w, http.StatusOK, true)
}

// handleSessionUpdate updates a session's title and/or permission rules.
// Title and Permission are both optional — a permission-only PATCH must NOT
// clobber the existing title, so the rename is skipped when Title is nil.
// A title update goes through Sessions.Rename, which marks the session
// user-titled (so automatic title generation won't overwrite it) and rejects
// an empty/whitespace title with 400.
func (s *Server) handleSessionUpdate(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("sessionID")

	var req APISessionUpdateRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	sess, err := s.app.Sessions.Get(r.Context(), sessionID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to get session")
		return
	}

	if req.Title != nil {
		updated, err := s.app.Sessions.Rename(r.Context(), sessionID, *req.Title)
		if err != nil {
			if errors.Is(err, session.ErrEmptyTitle) {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			writeError(w, http.StatusInternalServerError, "failed to update session")
			return
		}
		sess = updated
	}

	if shouldAutoApprove(req.Permission) {
		s.app.Permissions.AutoApproveSession(sessionID)
	}

	writeJSON(w, http.StatusOK, ConvertSessionWithDir(sess, resolveDirectory(r)))
}

// handleSessionStatus reports active work only; absent entries are idle. The
// SDK map shape stays intact without materializing every historical idle row.
func (s *Server) handleSessionStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	statuses := map[string]APISessionStatus{}
	for _, id := range agentpkg.BusySessionIDs() {
		page, err := s.app.Sessions.Page(ctx, session.PageOptions{OnlyID: id, Limit: 1})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to read session status")
			return
		}
		if len(page.Sessions) > 0 {
			statuses[id] = APISessionStatus{Type: "busy"}
		}
	}
	writeJSON(w, http.StatusOK, statuses)
}

// handleSessionTodo returns the todo list for a session.
func (s *Server) handleSessionTodo(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("sessionID")
	if s.app.Todos != nil {
		if items := s.app.Todos.Get(sessionID); items != nil {
			writeJSON(w, http.StatusOK, items)
			return
		}
	}
	writeJSON(w, http.StatusOK, []struct{}{})
}

// handleSessionChildren returns descendant sessions of the given session.
// Sessions.ListChildren returns the whole subtree (matches by root_session_id,
// which equals the session's own ID for top-level sessions), so we filter out
// the parent itself — the SDK endpoint is "children", not "tree".
func (s *Server) handleSessionChildren(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("sessionID")
	options, err := sessionPageOptions(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	options.TreeRoot = sessionID
	options.ExcludeID = sessionID
	page, err := s.app.Sessions.Page(r.Context(), options)
	if err != nil {
		writeSessionPageError(w, err)
		return
	}
	sessionPageHeaders(w, r, page.NextCursor)
	writeJSON(w, http.StatusOK, ConvertSessionsWithDir(page.Sessions, resolveDirectory(r)))
}

// handleSessionAbort cancels the active agent run for a session.
func (s *Server) handleSessionAbort(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("sessionID")
	if s.app.FlowBusy(sessionID) {
		if err := s.app.ControlFlow(r.Context(), sessionID, "", "cancel"); err != nil {
			writeSessionFlowError(w, err)
			return
		}
	}
	agent := s.app.ActiveAgent()
	if agent != nil {
		agent.Cancel(sessionID)
	}
	writeJSON(w, http.StatusOK, true)
}
