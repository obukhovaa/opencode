package service

import (
	"net/http"
)

// handleQuestionReply is a request-bound alternative to generic inbound for
// external question widgets. The API server applies the same Basic auth as
// every /router route. Older runners expose no such route: callers must never
// fall back to a label-only inbound or the raw question-service HTTP endpoint.
func (s *Service) handleQuestionReply(w http.ResponseWriter, req *http.Request) {
	var body questionReplyInput
	if err := readJSON(req, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Bad Request", "code": "invalid_json"})
		return
	}
	if s.questionRouter == nil || s.app == nil || s.app.Questions == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Service Unavailable", "code": "question_router_unavailable"})
		return
	}
	replayed, refusal := s.questionRouter.replyRequest(req.PathValue("requestID"), body)
	if refusal != "" {
		status := http.StatusConflict
		if refusal == "answer_shape" || refusal == "invalid_answer" {
			status = http.StatusBadRequest
		}
		writeJSON(w, status, map[string]string{"error": http.StatusText(status), "code": refusal})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true, "alreadyAnswered": replayed})
}
