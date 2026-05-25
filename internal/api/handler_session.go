package api

import (
	"database/sql"
	"errors"
	"net/http"
)

// handleSessionList returns all sessions.
func (s *Server) handleSessionList(w http.ResponseWriter, r *http.Request) {
	sessions, err := s.app.Sessions.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list sessions")
		return
	}
	writeJSON(w, http.StatusOK, ConvertSessionsWithDir(sessions, resolveDirectory(r)))
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

// handleSessionUpdate updates a session's title.
func (s *Server) handleSessionUpdate(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("sessionID")

	var req APISessionUpdateRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	session, err := s.app.Sessions.Get(r.Context(), sessionID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to get session")
		return
	}

	session.Title = req.Title
	updated, err := s.app.Sessions.Save(r.Context(), session)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update session")
		return
	}
	writeJSON(w, http.StatusOK, ConvertSessionWithDir(updated, resolveDirectory(r)))
}

// handleSessionStatus returns the busy/idle status of all sessions.
func (s *Server) handleSessionStatus(w http.ResponseWriter, r *http.Request) {
	sessions, err := s.app.Sessions.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list sessions")
		return
	}

	statuses := make(map[string]APISessionStatus, len(sessions))
	agent := s.app.ActiveAgent()
	for _, sess := range sessions {
		status := "idle"
		if agent != nil && agent.IsSessionBusy(sess.ID) {
			status = "busy"
		}
		statuses[sess.ID] = APISessionStatus{Type: status}
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

// handleSessionAbort cancels the active agent run for a session.
func (s *Server) handleSessionAbort(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("sessionID")
	agent := s.app.ActiveAgent()
	if agent != nil {
		agent.Cancel(sessionID)
	}
	writeJSON(w, http.StatusOK, true)
}
