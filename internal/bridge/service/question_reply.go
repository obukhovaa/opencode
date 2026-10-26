package service

// replyRequest settles exactly the named request in the expected session,
// never whichever question happens to offer the same label. Keep the pending
// map and stale-click cache in the router's critical section so its nudger and
// the next inbound observe a settled question when the HTTP call succeeds.
// Spec: bridge-http-api, "Request-bound question replies preserve router state".
func (r *QuestionRouter) replyRequest(sessionID, requestID string, answers [][]string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if pending := r.pending[sessionID]; pending != nil && pending.requestID != requestID {
		return false
	}
	// A live question can exist before fan-out has populated r.pending.
	// Validate the authoritative service's identity even in that recovery case.
	found := false
	for _, req := range r.svc.app.Questions.List() {
		if req.ID == requestID && req.SessionID == sessionID && len(req.Questions) == len(answers) {
			found = true
			break
		}
	}
	if !found {
		return false
	}
	if err := r.svc.app.Questions.Reply(requestID, answers); err != nil {
		return false
	}
	delete(r.pending, sessionID)
	r.rememberAnswers(sessionID, answers)
	return true
}
