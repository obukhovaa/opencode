package service

import (
	"net/http"
	"strings"
)

// handleQuestionReply is a request-bound alternative to generic inbound for
// external question widgets. The API server applies the same Basic auth as
// every /router route. Older runners expose no such route: callers must never
// fall back to a label-only inbound or the raw question-service HTTP endpoint.
func (s *Service) handleQuestionReply(w http.ResponseWriter, req *http.Request) {
	var body struct {
		SessionID string     `json:"sessionId"`
		Answers   [][]string `json:"answers"`
	}
	if err := readJSON(req, &body); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid question reply")
		return
	}
	if strings.TrimSpace(body.SessionID) == "" || len(body.Answers) == 0 {
		writeAPIError(w, http.StatusBadRequest, "sessionId and answers are required")
		return
	}
	for _, row := range body.Answers {
		if len(row) == 0 {
			writeAPIError(w, http.StatusBadRequest, "answers must not contain empty selections")
			return
		}
		for _, value := range row {
			if strings.TrimSpace(value) == "" {
				writeAPIError(w, http.StatusBadRequest, "answers must not contain empty values")
				return
			}
		}
	}
	if s.questionRouter == nil || s.app == nil || s.app.Questions == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "question router unavailable")
		return
	}
	if !s.questionRouter.replyRequest(body.SessionID, req.PathValue("requestID"), body.Answers) {
		writeAPIError(w, http.StatusConflict, "question request is no longer pending in this session")
		return
	}
	writeJSON(w, http.StatusOK, true)
}
