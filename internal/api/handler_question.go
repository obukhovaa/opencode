package api

import (
	"net/http"

	"github.com/opencode-ai/opencode/internal/logging"
)

// handleQuestionList returns pending question requests.
// Question requests are primarily delivered via SSE events in real time,
// but clients can also poll this endpoint to discover questions they may have missed.
func (s *Server) handleQuestionList(w http.ResponseWriter, _ *http.Request) {
	if s.app.Questions == nil {
		writeJSON(w, http.StatusOK, []APIQuestionRequest{})
		return
	}
	pending := s.app.Questions.List()
	result := make([]APIQuestionRequest, len(pending))
	for i, req := range pending {
		result[i] = ConvertQuestionRequest(req)
	}
	writeJSON(w, http.StatusOK, result)
}

// handleQuestionReply handles replying to a pending question request.
func (s *Server) handleQuestionReply(w http.ResponseWriter, r *http.Request) {
	if s.app.Questions == nil {
		writeError(w, http.StatusServiceUnavailable, "question service not available")
		return
	}

	requestID := r.PathValue("requestID")

	var req APIQuestionReply
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := s.app.Questions.Reply(requestID, req.Answers); err != nil {
		logging.Warn("Question reply failed", "requestID", requestID, "error", err)
		writeError(w, http.StatusNotFound, "question request not found")
		return
	}

	writeJSON(w, http.StatusOK, true)
}

// handleQuestionReject handles rejecting/dismissing a pending question request.
func (s *Server) handleQuestionReject(w http.ResponseWriter, r *http.Request) {
	if s.app.Questions == nil {
		writeError(w, http.StatusServiceUnavailable, "question service not available")
		return
	}

	requestID := r.PathValue("requestID")

	if err := s.app.Questions.Reject(requestID); err != nil {
		logging.Warn("Question reject failed", "requestID", requestID, "error", err)
		writeError(w, http.StatusNotFound, "question request not found")
		return
	}

	writeJSON(w, http.StatusOK, true)
}
